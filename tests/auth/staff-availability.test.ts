import { randomBytes } from "node:crypto";
import { createServer, type IncomingHttpHeaders } from "node:http";
import { getMigrations } from "better-auth/db/migration";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import * as itemRoute from "../../src/app/api/staff/catalog/items/[id]/availability/route.ts";
import * as restaurantRoute from "../../src/app/api/staff/restaurant/availability/route.ts";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { staffAvailabilityRequest } from "../../src/server/staff-availability.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

const boundary = vi.hoisted((): { config: unknown } => ({ config: undefined }));
vi.mock("server-only", () => ({}));
vi.mock("@/server/auth/runtime", () => ({
  runtimeAuth: async () => boundary.config,
}));
const origin = "http://127.0.0.1:3000";
const id = "123e4567-e89b-42d3-a456-426614174000";
const target = { kind: "item", id } as const;
const item = {
  id,
  name: "Food",
  price: { amount_minor: "9223372036854775807", currency: "KWD", exponent: 3 },
  available: false,
  version: "1",
};
let pg: Awaited<ReturnType<typeof startAuthPostgres>>;
let owner: ReturnType<typeof createAuth>;
let cookie = "",
  apiOrigin = "",
  calls = 0,
  status = 200,
  hang = false;
let body: string | Buffer =
  `${JSON.stringify({ data: item, request_id: "a".repeat(32) })}\n`;
let outbound: {
  path?: string;
  method?: string;
  headers: IncomingHttpHeaders;
  body: string;
} = { headers: {}, body: "" };
const upstream = createServer(async (req, res) => {
  calls++;
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  outbound = {
    path: req.url,
    method: req.method,
    headers: req.headers,
    body: Buffer.concat(chunks).toString(),
  };
  if (hang) return;
  res.writeHead(status, {
    "content-type": "application/json",
    location: "http://foreign.invalid/secret",
  });
  res.end(body);
});
beforeAll(async () => {
  pg = await startAuthPostgres();
  owner = createAuth({
    target: databaseTarget(pg.dsn),
    origin,
    secret: randomBytes(48).toString("base64url"),
    setup: true,
  });
  await owner.pool.query("CREATE SCHEMA auth");
  await (await getMigrations(owner.auth.options)).runMigrations();
  const response = await owner.auth.api.signUpEmail({
    body: {
      name: "Availability Staff",
      email: "availability@example.test",
      password: randomBytes(32).toString("base64url"),
    },
    asResponse: true,
  });
  cookie = response.headers
    .getSetCookie()
    .map((value) => value.split(";")[0])
    .join("; ");
  await new Promise<void>((done) => upstream.listen(0, "127.0.0.1", done));
  const address = upstream.address();
  if (!address || typeof address === "string")
    throw new Error("Listener unavailable");
  apiOrigin = `http://127.0.0.1:${address.port}`;
  boundary.config = { auth: owner.auth, origin, apiOrigin };
});
afterAll(async () => {
  upstream.closeAllConnections();
  await new Promise<void>((done) => upstream.close(() => done()));
  await owner?.pool.end();
  await pg?.close();
});
const config = () => ({ auth: owner.auth, origin, apiOrigin });
const request = (
  raw: BodyInit = '{"expected_version":"0","available":false}',
  headers: HeadersInit = {},
  suffix = "",
) =>
  new Request(`${origin}/api/staff/catalog/items/${id}/availability${suffix}`, {
    method: "PATCH",
    headers: {
      cookie,
      origin,
      "content-type": "application/json",
      "idempotency-key": "intent-one",
      ...headers,
    },
    body: raw,
  });
const invoke = (req = request()) =>
  staffAvailabilityRequest(req, target, config());
async function failure(response: Response, expected: number, code: string) {
  expect(response.status).toBe(expected);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(response.headers.has("set-cookie")).toBe(false);
  const json = await response.json();
  const messages: Record<string, string> = {
    invalid_input: "Invalid request.",
    unauthenticated: "Authentication is required.",
    forbidden: "Staff access is not available.",
    not_found: "Catalog item was not found.",
    method_not_allowed: "The method is not allowed.",
    version_conflict: "Refresh availability before trying again.",
    idempotency_conflict:
      "Idempotency key was already used for a different request.",
    payload_too_large: "Request body is too large.",
    unsupported_media_type: "Content-Type must be application/json.",
    internal_error: "Availability could not be completed.",
    temporarily_unavailable: "Availability is temporarily unavailable.",
  };
  expect(json).toEqual({
    error: { code, message: messages[code] },
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
  });
  expect(JSON.stringify(json)).not.toMatch(
    /SQL|Bearer|provider-secret|foreign/,
  );
}
it("requires configured matching Origin, authoritative session and valid framing before outbound work", async () => {
  const before = calls;
  const sessions = await owner.auth.api.listSessions({
    headers: new Headers({ cookie }),
  });
  for (const supplied of [null, "http://foreign.invalid"]) {
    const req = request();
    if (supplied === null) req.headers.delete("origin");
    else req.headers.set("origin", supplied);
    req.headers.set("sec-fetch-site", "same-origin");
    await failure(await invoke(req), 403, "forbidden");
  }
  const anonymous = request();
  anonymous.headers.delete("cookie");
  anonymous.headers.set("authorization", "Bearer forged");
  await failure(await invoke(anonymous), 401, "unauthenticated");
  for (const suffix of ["?", "?role=admin"])
    await failure(
      await invoke(request(undefined, {}, suffix)),
      400,
      "invalid_input",
    );
  for (const key of ["", "has space", "has\ttab", "x".repeat(129)]) {
    await failure(
      await invoke(request(undefined, { "idempotency-key": key })),
      400,
      "invalid_input",
    );
  }
  const missingKey = request();
  missingKey.headers.delete("idempotency-key");
  await failure(await invoke(missingKey), 400, "invalid_input");
  const duplicateKey = request();
  duplicateKey.headers.append("idempotency-key", "another");
  await failure(await invoke(duplicateKey), 400, "invalid_input");
  for (const length of ["1", "01", "invalid"])
    await failure(
      await invoke(request(undefined, { "content-length": length })),
      400,
      "invalid_input",
    );
  await failure(
    await invoke(request(undefined, { "transfer-encoding": "chunked" })),
    400,
    "invalid_input",
  );
  await failure(
    await invoke(request(undefined, { "content-type": "text/plain" })),
    415,
    "unsupported_media_type",
  );
  await failure(
    await invoke(request(undefined, { trailer: "x" })),
    400,
    "invalid_input",
  );
  for (const raw of [
    '{"expected_version":"0","available":true,"available":false}',
    '{"expected_version":"0","available":true,"availa\\u0062le":false}',
    '{"expected_version":0,"available":false}',
    '{"expected_version":"0","available":false,"role":"admin"}',
    Buffer.from([0xff]),
    "x".repeat(65537),
  ])
    await failure(
      await invoke(request(raw)),
      typeof raw === "string" && raw.length > 65536 ? 413 : 400,
      typeof raw === "string" && raw.length > 65536
        ? "payload_too_large"
        : "invalid_input",
    );
  await failure(
    await staffAvailabilityRequest(
      request(),
      { kind: "item", id: id.toUpperCase() },
      config(),
    ),
    400,
    "invalid_input",
  );
  expect(calls).toBe(before);
  expect(
    JSON.stringify(
      await owner.auth.api.listSessions({ headers: new Headers({ cookie }) }),
    ) === JSON.stringify(sessions),
  ).toBe(true);
});
it("forwards original bounded JSON and only server JWT/key to a fixed PATCH, preserving exact Go bytes", async () => {
  const raw = ' { "available":false, "expected_version":"0" } ';
  const response = await invoke(
    request(raw, {
      authorization: "Bearer browser-secret",
      "x-role": "admin",
      "idempotency-key": "x".repeat(128),
    }),
  );
  expect(response.status).toBe(200);
  expect(await response.text()).toBe(body);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(outbound.path).toBe(`/v1/staff/catalog/items/${id}/availability`);
  expect(outbound.method).toBe("PATCH");
  expect(outbound.body).toBe(raw);
  expect(Object.keys(outbound.headers).sort()).toEqual([
    "accept",
    "authorization",
    "connection",
    "content-length",
    "content-type",
    "host",
    "idempotency-key",
  ]);
  expect(outbound.headers.authorization?.startsWith("Bearer ")).toBe(true);
  expect(outbound.headers.authorization?.includes("browser-secret")).toBe(
    false,
  );
  expect(outbound.headers["idempotency-key"]).toBe("x".repeat(128));
  const replay = await invoke(
    request(raw, { "idempotency-key": "x".repeat(128) }),
  );
  expect(await replay.text()).toBe(body);
  const originalResponse = body;
  body =
    originalResponse +
    " ".repeat(512 * 1024 - Buffer.byteLength(originalResponse));
  const boundedResponse = await invoke(request(raw));
  expect(boundedResponse.status).toBe(200);
  expect(await boundedResponse.text()).toBe(body);
  body += " ";
  await failure(await invoke(request(raw)), 503, "temporarily_unavailable");
  body = originalResponse;
  const escaped = '{"expected_version":"0","availa\\u0062le":false}';
  expect((await invoke(request(escaped))).status).toBe(200);
  const padded = raw + " ".repeat(65536 - Buffer.byteLength(raw));
  expect((await invoke(request(padded))).status).toBe(200);
  // Count UTF-8 bytes, not JS characters, including invalid over-boundary bodies.
  await failure(
    await invoke(request("é".repeat(32769))),
    413,
    "payload_too_large",
  );
  await failure(
    await invoke(
      request(
        Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(raw)]),
      ),
    ),
    400,
    "invalid_input",
  );
  body = `${JSON.stringify({ data: { open: false, version: "1" }, request_id: "b".repeat(32) })}\n`;
  expect(
    (
      await restaurantRoute.PATCH(
        new Request(`${origin}/api/staff/restaurant/availability`, {
          method: "PATCH",
          headers: {
            cookie,
            origin,
            "content-type": "application/json; charset=utf-8",
            "idempotency-key": "r",
          },
          body: '{"expected_version":"0","open":false}',
        }),
      )
    ).status,
  ).toBe(200);
  expect(outbound.path).toBe("/v1/staff/restaurant/availability");
});
it("sanitizes known errors and fails closed on unknown, corrupt, mismatched, redirect, oversized and slow results", async () => {
  for (const [code, error] of [
    [400, "invalid_input"],
    [401, "unauthenticated"],
    [403, "forbidden"],
    [404, "not_found"],
    [409, "version_conflict"],
    [409, "idempotency_conflict"],
    [413, "payload_too_large"],
    [415, "unsupported_media_type"],
    [500, "internal_error"],
    [503, "temporarily_unavailable"],
  ] as const) {
    status = code;
    body = JSON.stringify({
      error: { code: error, message: "SQL provider-secret" },
      request_id: "a".repeat(32),
    });
    await failure(await invoke(), code, error);
  }
  status = 409;
  body = JSON.stringify({
    error: { code: "forbidden", message: "secret" },
    request_id: "a".repeat(32),
  });
  await failure(await invoke(), 503, "temporarily_unavailable");
  for (const code of [302, 201, 418]) {
    status = code;
    await failure(await invoke(), 503, "temporarily_unavailable");
  }
  status = 200;
  for (const value of [
    "bad",
    Buffer.from([0xff]),
    Buffer.concat([
      Buffer.from(`{"data":{"id":"${id}","name":"`),
      Buffer.from([0xff]),
      Buffer.from(
        `","price":${JSON.stringify(item.price)},"available":false,"version":"1"},"request_id":"${"a".repeat(32)}"}`,
      ),
    ]),
    Buffer.concat([
      Buffer.from([0xef, 0xbb, 0xbf]),
      Buffer.from(JSON.stringify({ data: item, request_id: "a".repeat(32) })),
    ]),
    JSON.stringify({
      data: item,
      request_id: "a".repeat(32),
      token: "provider-secret",
    }),
    "x".repeat(524289),
    JSON.stringify({
      data: { ...item, id: "223e4567-e89b-42d3-a456-426614174000" },
      request_id: "a".repeat(32),
    }),
    JSON.stringify({
      data: { ...item, available: true },
      request_id: "a".repeat(32),
    }),
    JSON.stringify({
      data: { ...item, version: "2" },
      request_id: "a".repeat(32),
    }),
  ]) {
    body = value;
    await failure(await invoke(), 503, "temporarily_unavailable");
  }
  const beforeTimeout = calls;
  hang = true;
  await failure(await invoke(), 503, "temporarily_unavailable");
  hang = false;
  expect(calls).toBe(beforeTimeout + 1);
});
it("public exports validate promised params and reject all other methods; logout blocks writes before Go", async () => {
  body = JSON.stringify({ data: item, request_id: "a".repeat(32) });
  expect(
    (await itemRoute.PATCH(request(), { params: Promise.resolve({ id }) }))
      .status,
  ).toBe(200);
  for (const route of [itemRoute, restaurantRoute])
    for (const method of [
      route.GET,
      route.POST,
      route.PUT,
      route.DELETE,
      route.HEAD,
      route.OPTIONS,
    ]) {
      const response = await method();
      expect(response.headers.get("allow")).toBe("PATCH");
      await failure(response, 405, "method_not_allowed");
    }
  await failure(
    await itemRoute.PATCH(request(), {
      params: Promise.resolve({ id: "invalid" }),
    }),
    400,
    "invalid_input",
  );
  await owner.auth.api.signOut({ headers: new Headers({ cookie, origin }) });
  const before = calls;
  await failure(await invoke(), 401, "unauthenticated");
  expect(calls).toBe(before);
  boundary.config = undefined;
  await failure(
    await restaurantRoute.PATCH(request()),
    503,
    "temporarily_unavailable",
  );
});
