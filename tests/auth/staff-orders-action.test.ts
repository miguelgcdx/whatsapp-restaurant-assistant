import { createServer } from "node:http";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import * as route from "../../src/app/api/staff/orders/[id]/actions/route";
import {
  envelope,
  orderDetail,
  orderId,
  summaryId,
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
  status = 200,
  raw = "",
  key: string | undefined,
  authorization: string | undefined;
const ack = JSON.stringify(
  envelope({
    ...orderDetail,
    version: "1",
    fulfillment_status: "accepted",
    permitted_actions: ["start_preparing"],
  }),
  null,
  2,
);
let responseBody = ack;
const fake = createServer((request, response) => {
  calls++;
  key = request.headers["idempotency-key"]?.toString();
  authorization = request.headers.authorization;
  const chunks: Buffer[] = [];
  request.on("data", (chunk) => chunks.push(chunk));
  request.on("end", () => {
    raw = Buffer.concat(chunks).toString("utf8");
    response.writeHead(status, {
      "content-type": "application/json",
      "set-cookie": "synthetic=never-forward",
      location: "https://foreign.invalid",
    });
    response.end(responseBody);
  });
});
beforeAll(async () => {
  await new Promise<void>((resolve) => fake.listen(0, "127.0.0.1", resolve));
  const address = fake.address();
  if (!address || typeof address === "string")
    throw new Error("Fixture missing");
  state.apiOrigin = `http://127.0.0.1:${address.port}`;
});
afterAll(async () => {
  fake.closeAllConnections();
  await new Promise<void>((resolve) => fake.close(() => resolve()));
});
const body = '{"action":"accept","expected_version":"0"}';
it("accepts exact 64KiB UTF-8 JSON and boundary key lengths without clipping", async () => {
  const padded = body.padEnd(65536, " ");
  for (const length of [1, 128]) {
    const response = await invoke(padded, {
      "idempotency-key": "k".repeat(length),
      "content-length": "65536",
    });
    expect(response.status).toBe(200);
    expect(await response.text()).toBe(ack);
    expect(raw).toBe(padded);
    expect(key).toBe("k".repeat(length));
  }
});
const invoke = (
  value: BodyInit = body,
  headers: HeadersInit = {},
  id = orderId,
  suffix = "",
) =>
  route.POST(
    new Request(
      `http://127.0.0.1:3000/api/staff/orders/${id}/actions${suffix}`,
      {
        method: "POST",
        headers: {
          origin: "http://127.0.0.1:3000",
          "content-type": "application/json",
          "idempotency-key": "same-key",
          ...headers,
        },
        body: value,
      },
    ),
    { params: Promise.resolve({ id }) },
  );
it("forwards only exact intent/key plus server JWT, preserving original committed bytes even on old replay", async () => {
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await invoke();
    expect(response.status).toBe(200);
    expect(await response.text()).toBe(ack);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(response.headers.has("set-cookie")).toBe(false);
    expect(response.headers.has("location")).toBe(false);
    expect(raw).toBe(body);
    expect(key).toBe("same-key");
    expect(authorization).toBe("Bearer synthetic-test-token");
  }
});
it("rejects invalid authority/query/framing/media/body/key before any Go request", async () => {
  const before = calls;
  const snapshot = JSON.stringify(orderDetail);
  const codes: Record<number, string> = {
    400: "invalid_input",
    403: "forbidden",
    413: "payload_too_large",
    415: "unsupported_media_type",
  };
  const cases: [BodyInit, HeadersInit, string, string, number][] = [
    [body, { origin: "https://foreign.invalid" }, orderId, "", 403],
    [body, {}, "bad", "", 400],
    [body, {}, orderId, "?unexpected=x", 400],
    [body, { "idempotency-key": "bad key" }, orderId, "", 400],
    [body, { "idempotency-key": "" }, orderId, "", 400],
    [body, { "idempotency-key": "k".repeat(129) }, orderId, "", 400],
    [body, { "content-length": "1" }, orderId, "", 400],
    [body, { trailer: "x" }, orderId, "", 400],
    [body, {}, orderId, "?", 400],
    [
      '{"action":"accept","expected_version":"0","actor":"browser"}',
      {},
      orderId,
      "",
      400,
    ],
    [body, { "content-type": "text/plain" }, orderId, "", 415],
    [
      '{"action":"accept","expected_version":"0","price":"1"}',
      {},
      orderId,
      "",
      400,
    ],
    [
      '{"action":"accept","action":"reject","expected_version":"0"}',
      {},
      orderId,
      "",
      400,
    ],
    ['{"action":"accept","expected_version":0}', {}, orderId, "", 400],
    [new Uint8Array([255]), {}, orderId, "", 400],
    [" ".repeat(65537), {}, orderId, "", 413],
  ];
  for (const [value, headers, id, suffix, expected] of cases) {
    const response = await invoke(value, headers, id, suffix);
    expect(response.status).toBe(expected);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect((await response.json()).error.code).toBe(codes[expected]);
    expect(calls).toBe(before);
    expect(JSON.stringify(orderDetail)).toBe(snapshot);
  }
  state.signedIn = false;
  expect((await invoke()).status).toBe(401);
  state.signedIn = true;
  expect(calls).toBe(before);
});
it("keeps stale/state/policy/conflict errors distinct and rejects foreign order acknowledgements", async () => {
  for (const [value, code] of [
    [409, "version_conflict"],
    [409, "invalid_transition"],
    [409, "idempotency_conflict"],
    [422, "policy_unresolved"],
    [503, "temporarily_unavailable"],
  ] as const) {
    status = value;
    responseBody = JSON.stringify({
      error: { code, message: "SYNTHETIC_UNSAFE_TEXT" },
      request_id: "a".repeat(32),
    });
    const response = await invoke();
    expect(response.status).toBe(value);
    const result = await response.json();
    expect(result.error.code).toBe(code);
    expect(result.error.message).not.toContain("SYNTHETIC_UNSAFE_TEXT");
  }
  status = 200;
  responseBody = JSON.stringify(
    envelope({
      ...orderDetail,
      id: summaryId,
      confirmed: {
        ...orderDetail.confirmed,
        units_href: `/api/staff/orders/${summaryId}/summaries/${summaryId}/units`,
      },
    }),
  );
  expect((await invoke()).status).toBe(503);
  responseBody = ack;
});
