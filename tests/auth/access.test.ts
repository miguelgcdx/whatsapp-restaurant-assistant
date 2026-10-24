import { randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { getMigrations } from "better-auth/db/migration";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { staffAccessRequest } from "../../src/server/staff-access.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

vi.mock("server-only", () => ({}));
// Component-only fake Go seam: does NOT verify Go signature/membership behavior.
const origin = "http://127.0.0.1:3000";
const restaurantID = "123e4567-e89b-42d3-a456-426614174000";
let postgres: Awaited<ReturnType<typeof startAuthPostgres>>;
let owner: ReturnType<typeof createAuth>;
let cookie = "";
let calls = 0;
let status = 200;
let body = JSON.stringify({ restaurant_id: restaurantID });
let outbound: Record<string, unknown> = {};
const fake = createServer((request, response) => {
  calls++;
  outbound = {
    path: request.url,
    method: request.method,
    headers: request.headers,
  };
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(body);
});
let apiOrigin = "";
beforeAll(async () => {
  postgres = await startAuthPostgres();
  owner = createAuth({
    target: databaseTarget(postgres.dsn),
    origin,
    secret: randomBytes(48).toString("base64url"),
    setup: true,
  });
  await owner.pool.query("CREATE SCHEMA auth");
  await (await getMigrations(owner.auth.options)).runMigrations();
  const response = await owner.auth.api.signUpEmail({
    body: {
      name: "Alex",
      email: "alex.staff@example.test",
      password: randomBytes(32).toString("base64url"),
    },
    asResponse: true,
  });
  cookie = response.headers
    .getSetCookie()
    .map((value) => value.split(";")[0])
    .join("; ");
  await new Promise<void>((resolve) => fake.listen(0, "127.0.0.1", resolve));
  const address = fake.address();
  if (!address || typeof address === "string")
    throw new Error("fake listener unavailable");
  apiOrigin = `http://127.0.0.1:${address.port}`;
});
afterAll(async () => {
  fake.closeAllConnections();
  await new Promise<void>((resolve) => fake.close(() => resolve()));
  await owner?.pool.end();
  await postgres?.close();
});
const request = (headers: HeadersInit = {}, suffix = "", method = "GET") =>
  new Request(`${origin}/api/staff/access${suffix}`, { method, headers });
const invoke = (r: Request) =>
  staffAccessRequest(r, { auth: owner.auth, origin, apiOrigin });

it("rejects forged authority, foreign origin, queries, and methods without dependency effects", async () => {
  const before = calls;
  for (const headers of [
    new Headers(),
    new Headers({
      authorization: "Bearer forged",
      "x-user-id": "alex",
      "x-restaurant-id": restaurantID,
    }),
  ]) {
    const response = await invoke(request(headers));
    expect(response.status).toBe(401);
    expect(await response.text()).toBe(
      '{"error":{"code":"unauthenticated","message":"Authentication is required."}}',
    );
  }
  const foreign = await invoke(
    request({ cookie, origin: "http://127.0.0.1:9999" }),
  );
  expect(foreign.status).toBe(403);
  expect(await foreign.text()).toBe(
    '{"error":{"code":"forbidden","message":"Staff access is not available."}}',
  );
  const query = await invoke(request({ cookie }, "?restaurant_id=forged"));
  expect(query.status).toBe(400);
  expect(await query.text()).toBe(
    '{"error":{"code":"invalid_input","message":"Invalid request."}}',
  );
  const post = await invoke(request({ cookie, origin }, "", "POST"));
  expect(post.status).toBe(405);
  expect(post.headers.get("Allow")).toBe("GET");
  expect(await post.text()).toBe(
    '{"error":{"code":"method_not_allowed","message":"The method is not allowed."}}',
  );
  expect(calls).toBe(before);
});
it("rejects GET body metadata before authentication or Go access", async () => {
  const before = calls;
  const sessionsBefore = await owner.auth.api.listSessions({
    headers: new Headers({ cookie }),
  });
  let authReads = 0;
  const config = {
    get auth() {
      authReads++;
      return owner.auth;
    },
    origin,
    apiOrigin,
  };
  for (const metadata of [
    new Headers({ "content-length": "1" }),
    new Headers({ "content-length": "invalid" }),
    new Headers({ "transfer-encoding": "chunked" }),
    new Headers({ "transfer-encoding": "" }),
  ]) {
    metadata.set("cookie", cookie);
    const response = await staffAccessRequest(request(metadata), config);
    expect(response.status).toBe(400);
    expect(await response.text()).toBe(
      '{"error":{"code":"invalid_input","message":"Invalid request."}}',
    );
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    expect(response.headers.has("set-cookie")).toBe(false);
    expect(authReads).toBe(0);
    expect(calls).toBe(before);
  }
  const sessionsAfter = await owner.auth.api.listSessions({
    headers: new Headers({ cookie }),
  });
  expect(sessionsAfter.length).toBe(sessionsBefore.length);
  expect(JSON.stringify(sessionsAfter) === JSON.stringify(sessionsBefore)).toBe(
    true,
  );
  // A bodyless GET with explicit zero length keeps its previous successful behavior.
  const normal = await invoke(request({ cookie, "content-length": "0" }));
  expect(normal.status).toBe(200);
  expect(await normal.text()).toBe(
    JSON.stringify({ restaurant_id: restaurantID }),
  );
  expect(normal.headers.get("Cache-Control")).toBe("no-store");
  expect(calls).toBe(before + 1);
});
it("sends only an official server token to a fixed direct loopback path and returns the minimal parsed DTO", async () => {
  const response = await invoke(
    request({
      cookie,
      authorization: "Bearer browser-value",
      "x-user-id": "forged",
      "x-role": "admin",
    }),
  );
  expect(response.status).toBe(200);
  expect(await response.text()).toBe(body);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(outbound.path).toBe("/v1/staff/access");
  expect(outbound.method).toBe("GET");
  const headers = outbound.headers as Record<string, string>;
  expect(headers.authorization.startsWith("Bearer ")).toBe(true);
  expect(headers.authorization.includes("browser-value")).toBe(false);
  expect(Object.keys(headers).sort()).toEqual([
    "accept",
    "authorization",
    "connection",
    "host",
  ]);
});
it("never treats Go403, redirects, malformed/oversized data or dependency failures as successful access", async () => {
  for (const code of [403, 401, 500, 302]) {
    status = code;
    const response = await invoke(request({ cookie }));
    expect(response.status).toBe(code === 403 ? 403 : 503);
    expect(await response.text()).toBe(
      code === 403
        ? '{"error":{"code":"forbidden","message":"Staff access is not available."}}'
        : '{"error":{"code":"temporarily_unavailable","message":"Staff access is temporarily unavailable."}}',
    );
  }
  status = 200;
  for (const malformed of [
    "not-json",
    JSON.stringify({ restaurant_id: restaurantID, token: "must-not-escape" }),
    "x".repeat(17000),
  ]) {
    body = malformed;
    const response = await invoke(request({ cookie }));
    expect(response.status).toBe(503);
    expect(await response.text()).toBe(
      '{"error":{"code":"temporarily_unavailable","message":"Staff access is temporarily unavailable."}}',
    );
  }
  const response = await staffAccessRequest(request({ cookie }), {
    auth: owner.auth,
    origin,
    apiOrigin: "http://127.0.0.1:1",
  });
  expect(response.status).toBe(503);
});
