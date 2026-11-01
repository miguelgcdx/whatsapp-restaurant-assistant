import { createServer } from "node:http";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import * as detail from "../../src/app/api/staff/orders/[id]/route";
import * as units from "../../src/app/api/staff/orders/[id]/summaries/[summary_id]/units/route";
import * as list from "../../src/app/api/staff/orders/route";
import {
  envelope,
  orderDetail,
  orderId,
  orderSummary,
  summaryId,
  unitPage,
} from "../helpers/pickup-fixtures";

const state = vi.hoisted(() => ({ apiOrigin: "", signedIn: true }));
vi.mock("server-only", () => ({}));
vi.mock("../../src/server/auth/session", () => ({
  authoritativeSession: async () =>
    state.signedIn ? { user: { name: "Synthetic Staff" } } : null,
}));
vi.mock("@/server/auth/runtime", () => ({
  runtimeAuth: async () => ({
    origin: "http://127.0.0.1:3000",
    apiOrigin: state.apiOrigin,
    auth: {
      api: { getToken: async () => ({ token: "synthetic-test-token" }) },
    },
  }),
}));
let calls = 0,
  path = "",
  authorization: string | undefined;
let payload: unknown;
const fake = createServer((request, response) => {
  calls++;
  path = request.url ?? "";
  authorization = request.headers.authorization;
  const data = path.includes("/units")
    ? unitPage
    : path.startsWith("/v1/staff/orders/")
      ? orderDetail
      : { items: [orderSummary], next_cursor: null };
  response.setHeader("content-type", "application/json");
  response.end(JSON.stringify(payload ?? envelope(data)));
});
beforeAll(async () => {
  await new Promise<void>((resolve) => fake.listen(0, "127.0.0.1", resolve));
  const address = fake.address();
  if (!address || typeof address === "string")
    throw new Error("Fixture listener missing");
  state.apiOrigin = `http://127.0.0.1:${address.port}`;
});
afterAll(async () => {
  fake.closeAllConnections();
  await new Promise<void>((resolve) => fake.close(() => resolve()));
});
const request = (suffix = "", headers: HeadersInit = {}) =>
  new Request("http://127.0.0.1:3000/api/staff/orders" + suffix, { headers });
it("reads only allowlisted order paths with server token and no-store output", async () => {
  const response = await list.GET(request("?limit=1"));
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(await response.json()).toEqual(
    envelope({ items: [orderSummary], next_cursor: null }),
  );
  expect(path).toBe("/v1/staff/orders?limit=1");
  expect(authorization).toBe("Bearer synthetic-test-token");
  expect(
    (
      await detail.GET(request("/" + orderId), {
        params: Promise.resolve({ id: orderId }),
      })
    ).status,
  ).toBe(200);
  const page = await units.GET(
    request("/" + orderId + "/summaries/" + summaryId + "/units"),
    { params: Promise.resolve({ id: orderId, summary_id: summaryId }) },
  );
  expect(page.status).toBe(200);
  expect(await page.json()).toEqual(envelope(unitPage));
});
it("rejects anonymous, foreign-origin, invalid query and invalid IDs before any Go call", async () => {
  const before = calls;
  state.signedIn = false;
  expect((await list.GET(request())).status).toBe(401);
  state.signedIn = true;
  expect(
    (await list.GET(request("", { origin: "https://foreign.invalid" }))).status,
  ).toBe(403);
  expect((await list.GET(request("?limit=1&limit=2"))).status).toBe(400);
  expect(
    (
      await detail.GET(request("/bad"), {
        params: Promise.resolve({ id: "bad" }),
      })
    ).status,
  ).toBe(400);
  expect(calls).toBe(before);
});
it("rejects mismatched upstream order and immutable-summary identities", async () => {
  payload = envelope({ ...orderDetail, id: summaryId });
  expect(
    (
      await detail.GET(request("/" + orderId), {
        params: Promise.resolve({ id: orderId }),
      })
    ).status,
  ).toBe(503);
  payload = envelope({ ...unitPage, summary_id: orderId });
  expect(
    (
      await units.GET(request(), {
        params: Promise.resolve({ id: orderId, summary_id: summaryId }),
      })
    ).status,
  ).toBe(503);
  payload = undefined;
});
