import { randomUUID } from "node:crypto";
import { lstat, readFile } from "node:fs/promises";
import { dirname, isAbsolute } from "node:path";
import { beforeAll, expect, it, vi } from "vitest";
import { z } from "zod";
import * as actions from "../../src/app/api/staff/orders/[id]/actions/route";
import * as detail from "../../src/app/api/staff/orders/[id]/route";
import * as units from "../../src/app/api/staff/orders/[id]/summaries/[summary_id]/units/route";
import * as list from "../../src/app/api/staff/orders/route";
import {
  orderDetailSchema,
  orderIdSchema,
  orderListSchema,
  orderUnitsSchema,
  orderVersionSchema,
} from "../../src/contracts/staff-orders";
import { localOrigin } from "../../src/server/auth/config";

// Only this frontend auth boundary is synthetic. No BetterAuth runtime, real
// cookie/session issuance, fetch mock, HTTP server or database is created here.
// Go owns the live verifier/dispatcher/store/PG and keeps them alive for this child.
const state = vi.hoisted(() => ({
  apiOrigin: "",
  webOrigin: "",
  token: "",
  authEvents: Array<string>(),
}));
vi.mock("server-only", () => ({}));
vi.mock("../../src/server/auth/session", () => ({
  authoritativeSession: async () => {
    state.authEvents.push("session");
    return { user: { name: "Synthetic Transport Staff" } };
  },
}));
vi.mock("@/server/auth/runtime", () => ({
  runtimeAuth: async () => ({
    origin: state.webOrigin,
    apiOrigin: state.apiOrigin,
    auth: {
      api: {
        getToken: async () => {
          state.authEvents.push("token");
          return { token: state.token };
        },
      },
    },
  }),
}));

const fixtureSchema = z.strictObject({
  go_origin: z.string().refine((value) => {
    try {
      return (
        localOrigin(value) === value && new URL(value).hostname === "127.0.0.1"
      );
    } catch {
      return false;
    }
  }),
  web_origin: z.literal("http://localhost:3000"),
  token: z.string().regex(/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/),
  order_id: orderIdSchema,
  summary_id: orderIdSchema,
  initial_version: orderVersionSchema,
});

async function readFixture() {
  const path = process.env.STAFF_ORDERS_BFF_FIXTURE;
  if (!path)
    throw new Error(
      "STAFF_ORDERS_BFF_FIXTURE is required; run through the Go-owned BFF bridge.",
    );
  // A positive private /tmp file channel, never a default path or env-file read.
  // All errors are deliberately generic: never print JWT, JSON, or auth headers.
  try {
    if (
      !isAbsolute(path) ||
      !path.startsWith("/tmp/") ||
      path.split("/").includes("..")
    )
      throw new Error("Invalid channel");
    const [file, parent] = await Promise.all([
      lstat(path),
      lstat(dirname(path)),
    ]);
    const uid = process.getuid?.();
    if (
      uid === undefined ||
      !file.isFile() ||
      file.isSymbolicLink() ||
      file.uid !== uid ||
      (file.mode & 0o777) !== 0o600 ||
      file.size > 16 * 1024 ||
      !parent.isDirectory() ||
      parent.isSymbolicLink() ||
      parent.uid !== uid ||
      (parent.mode & 0o777) !== 0o700
    )
      throw new Error("Invalid channel");
    const value: unknown = JSON.parse(await readFile(path, "utf8"));
    const parsed = fixtureSchema.safeParse(value);
    if (!parsed.success) throw new Error("Invalid fixture");
    return parsed.data;
  } catch {
    throw new Error("Invalid private Go-owned BFF fixture channel.");
  }
}

let fixture: Awaited<ReturnType<typeof readFixture>>;
beforeAll(async () => {
  fixture = await readFixture();
  state.apiOrigin = fixture.go_origin;
  state.webOrigin = fixture.web_origin;
  state.token = fixture.token;
});

function request(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  headers.set("origin", fixture.web_origin);
  return new Request(`${fixture.web_origin}${path}`, { ...init, headers });
}

async function successfulJSON(response: Response): Promise<unknown> {
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(response.headers.get("content-type")).toMatch(/^application\/json\b/);
  expect(response.headers.has("set-cookie")).toBe(false);
  expect(response.headers.has("location")).toBe(false);
  return response.json();
}

it("transports r3 reads, immutable unit pagination and accept/readback/manual replay to real Go", async () => {
  const orderPath = `/api/staff/orders/${fixture.order_id}`;
  const context = { params: Promise.resolve({ id: fixture.order_id }) };

  // Strict canonical schemas also reject capability/header widening on the list.
  const listing = orderListSchema.parse(
    await successfulJSON(
      await list.GET(request("/api/staff/orders?limit=100")),
    ),
  );
  const header = listing.data.items.find(
    (item) => item.id === fixture.order_id,
  );
  expect(header).toBeDefined();
  expect(header).not.toHaveProperty("permitted_actions");
  expect(header).not.toHaveProperty("confirmed_summary_id");
  expect(header).toMatchObject({
    version: fixture.initial_version,
    fulfillment_status: "submitted",
    payment_status: "unpaid",
    fulfillment_kind: "pickup",
  });

  const initial = orderDetailSchema.parse(
    await successfulJSON(await detail.GET(request(orderPath), context)),
  ).data;
  expect(initial.id).toBe(fixture.order_id);
  expect(initial.version).toBe(fixture.initial_version);
  expect(initial.fulfillment_status).toBe("submitted");
  expect(initial.payment_status).toBe("unpaid");
  expect(initial.payment).toEqual({ state: "unpaid", collected_at: null });
  expect(initial.confirmed.id).toBe(fixture.summary_id);
  expect(initial.permitted_actions).toContain("accept");
  expect(initial.total).toEqual(header?.total);
  const unitsPath = `${orderPath}/summaries/${fixture.summary_id}/units`;
  expect(initial.confirmed.units_href).toBe(unitsPath);

  // The bridge seeds a multi-unit order: limit=1 must exercise a real next_cursor.
  // Count and money come from the full immutable summary, never from a page total.
  expect(initial.confirmed.unit_count).toBeGreaterThan(1);
  const seenUnitIds = new Set<string>();
  const seenCursors = new Set<string>();
  let cursor: string | null = null;
  let pages = 0;
  do {
    const query = new URLSearchParams({ limit: "1" });
    if (cursor !== null) query.set("cursor", cursor);
    const page = orderUnitsSchema.parse(
      await successfulJSON(
        await units.GET(request(`${unitsPath}?${query}`), {
          params: Promise.resolve({
            id: fixture.order_id,
            summary_id: fixture.summary_id,
          }),
        }),
      ),
    ).data;
    expect(page.summary_id).toBe(initial.confirmed.id);
    expect(page.unit_count).toBe(initial.confirmed.unit_count);
    expect(page.items).toHaveLength(1);
    for (const unit of page.items) {
      expect(seenUnitIds.has(unit.id)).toBe(false);
      seenUnitIds.add(unit.id);
    }
    pages++;
    expect(pages).toBeLessThanOrEqual(initial.confirmed.unit_count);
    cursor = page.next_cursor;
    if (cursor !== null) {
      expect(seenCursors.has(cursor)).toBe(false);
      seenCursors.add(cursor);
    }
  } while (cursor !== null);
  expect(pages).toBeGreaterThan(1);
  expect(seenUnitIds.size).toBe(initial.confirmed.unit_count);

  const body = JSON.stringify({
    action: "accept",
    expected_version: initial.version,
  });
  const key = `synthetic-bff-${randomUUID()}`;
  // Each invocation creates a fresh Request with identical intent bytes/key.
  // Nothing automatically retries; the second call is an explicit manual replay.
  const accept = () =>
    actions.POST(
      request(`${orderPath}/actions`, {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": key },
        body,
      }),
      context,
    );
  const committed = orderDetailSchema.parse(
    await successfulJSON(await accept()),
  ).data;
  expect(committed.id).toBe(initial.id);
  expect(committed.confirmed).toEqual(initial.confirmed);
  expect(committed.fulfillment_status).toBe("accepted");
  expect(BigInt(committed.version)).toBeGreaterThan(BigInt(initial.version));
  expect(committed.payment).toEqual(initial.payment);
  expect(committed.payment_status).toBe("unpaid");
  expect(committed.permitted_actions).not.toContain("accept");

  const fresh = orderDetailSchema.parse(
    await successfulJSON(await detail.GET(request(orderPath), context)),
  ).data;
  expect(fresh).toEqual(committed);
  const replay = orderDetailSchema.parse(
    await successfulJSON(await accept()),
  ).data;
  // Compare DATA only: a per-request request_id may legitimately differ.
  expect(replay).toEqual(committed);
  const afterReplay = orderDetailSchema.parse(
    await successfulJSON(await detail.GET(request(orderPath), context)),
  ).data;
  expect(afterReplay).toEqual(fresh);

  // Every real wrapper call must first consult the synthetic authoritative session.
  expect(state.authEvents).toEqual(
    Array.from({ length: pages + 6 }, () => ["session", "token"]).flat(),
  );
});
