import { randomBytes } from "node:crypto";
import { createServer, type IncomingHttpHeaders } from "node:http";
import { getMigrations } from "better-auth/db/migration";
import { renderToStaticMarkup } from "react-dom/server";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import * as route from "../../src/app/api/staff/catalog/route.ts";
import CatalogPage from "../../src/app/dashboard/catalog/page.tsx";
import Dashboard from "../../src/app/dashboard/page.tsx";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { staffCatalogRequest } from "../../src/server/staff-catalog.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

const boundary = vi.hoisted((): { config: unknown; headers: Headers } => ({
  config: undefined,
  headers: new Headers(),
}));
vi.mock("server-only", () => ({}));
vi.mock("@/server/auth/runtime", () => ({
  runtimeAuth: async () => boundary.config,
}));
vi.mock("next/headers", () => ({ headers: async () => boundary.headers }));
vi.mock("next/navigation", () => ({
  redirect: (path: string) => {
    throw new Error(`redirect:${path}`);
  },
}));
vi.mock("../../src/app/sign-in/actions", () => ({ signOut: async () => {} }));
vi.mock(
  "@/server/staff-catalog",
  () => import("../../src/server/staff-catalog.ts"),
);
vi.mock(
  "@/server/staff-access",
  () => import("../../src/server/staff-access.ts"),
);
vi.mock(
  "@/contracts/staff-catalog",
  () => import("../../src/contracts/staff-catalog.ts"),
);
const origin = "http://127.0.0.1:3000";
const data = {
  items: [],
  next_cursor: null,
  restaurant: { open: true, version: "0" },
};
const envelope = { data, request_id: "a".repeat(32) };
let pg: Awaited<ReturnType<typeof startAuthPostgres>>;
let owner: ReturnType<typeof createAuth>;
let cookie = "";
let apiOrigin = "";
let calls = 0;
let status = 200;
let body: string | Buffer = JSON.stringify(envelope);
let hang = false;
let outbound: { path?: string; headers: IncomingHttpHeaders } = { headers: {} };
const upstream = createServer((request, response) => {
  calls++;
  if (request.url === "/v1/staff/access") {
    response.end(
      JSON.stringify({ restaurant_id: "123e4567-e89b-42d3-a456-426614174000" }),
    );
    return;
  }
  outbound = { path: request.url, headers: request.headers };
  if (hang) return;
  response.writeHead(status, {
    "content-type": "application/json",
    location: "http://foreign.invalid/secret",
  });
  response.end(body);
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
      name: "Catalog Staff",
      email: "catalog@example.test",
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
  boundary.headers = new Headers({ cookie });
});
afterAll(async () => {
  upstream.closeAllConnections();
  await new Promise<void>((done) => upstream.close(() => done()));
  await owner?.pool.end();
  await pg?.close();
});
const invoke = (
  suffix = "",
  headers: HeadersInit = { cookie },
  method = "GET",
) =>
  staffCatalogRequest(
    new Request(`${origin}/api/staff/catalog${suffix}`, { headers, method }),
    { auth: owner.auth, origin, apiOrigin },
  );
async function failure(
  response: Response,
  expected: number,
  code: string,
  message: string,
) {
  expect(response.status).toBe(expected);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(response.headers.has("set-cookie")).toBe(false);
  const json = await response.json();
  expect(json).toEqual({
    error: { code, message },
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
  });
}
it("rejects anonymous caller authority, foreign origin, invalid query/framing and methods without changing sessions", async () => {
  const before = calls;
  const sessions = await owner.auth.api.listSessions({
    headers: new Headers({ cookie }),
  });
  await failure(
    await invoke("", { authorization: "Bearer forged", "x-role": "admin" }),
    401,
    "unauthenticated",
    "Authentication is required.",
  );
  await failure(
    await invoke("", { cookie, origin: "http://foreign.invalid" }),
    403,
    "forbidden",
    "Staff access is not available.",
  );
  for (const query of [
    "?limit=0",
    "?limit=2&limit=3",
    "?restaurant_id=forged",
    "?cursor=%FF",
  ])
    await failure(
      await invoke(query),
      400,
      "invalid_input",
      "Invalid request.",
    );
  for (const headers of [
    new Headers({ "content-length": "1" }),
    new Headers({ "transfer-encoding": "" }),
    new Headers({ trailer: "x" }),
  ]) {
    headers.set("cookie", cookie);
    await failure(
      await invoke("", headers),
      400,
      "invalid_input",
      "Invalid request.",
    );
  }
  for (const method of ["POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"]) {
    const response = await invoke("", { cookie, origin }, method);
    expect(response.headers.get("allow")).toBe("GET");
    await failure(
      response,
      405,
      "method_not_allowed",
      "The method is not allowed.",
    );
  }
  expect(calls).toBe(before);
  expect(
    await owner.auth.api.listSessions({ headers: new Headers({ cookie }) }),
  ).toEqual(sessions);
});
it("forwards only a public server JWT and validated paging to the fixed direct endpoint", async () => {
  const response = await invoke("?limit=1&cursor=opaque", {
    cookie,
    authorization: "Bearer browser-secret",
    "x-role": "admin",
  });
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual(envelope);
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(outbound.path).toBe("/v1/staff/catalog?limit=1&cursor=opaque");
  expect(Object.keys(outbound.headers).sort()).toEqual([
    "accept",
    "authorization",
    "connection",
    "host",
  ]);
  expect(outbound.headers.authorization?.startsWith("Bearer ")).toBe(true);
  expect(outbound.headers.authorization?.includes("browser-secret")).toBe(
    false,
  );
  expect((await invoke("", { cookie, "content-length": "0" })).status).toBe(
    200,
  );
});
it("rechecks upstream authority on continuation and sanitizes redirects, malformed, oversized and timed-out dependencies", async () => {
  for (const code of [400, 403, 401, 500, 302]) {
    status = code;
    body = "SQL DSN Bearer secret";
    const expected = code === 400 || code === 403 ? code : 503;
    await failure(
      await invoke("?cursor=opaque"),
      expected,
      expected === 400
        ? "invalid_input"
        : expected === 403
          ? "forbidden"
          : "temporarily_unavailable",
      expected === 400
        ? "Invalid request."
        : expected === 403
          ? "Staff access is not available."
          : "Catalog is temporarily unavailable.",
    );
  }
  status = 200;
  for (const value of [
    "not-json",
    JSON.stringify({ ...envelope, token: "secret" }),
    Buffer.from([0xff]),
    Buffer.concat([
      Buffer.from([0xef, 0xbb, 0xbf]),
      Buffer.from(JSON.stringify(envelope)),
    ]),
    JSON.stringify({
      ...envelope,
      data: { ...data, restaurant: { open: true, version: "-1" } },
    }),
    "x".repeat(512 * 1024 + 1),
  ]) {
    body = value;
    await failure(
      await invoke(),
      503,
      "temporarily_unavailable",
      "Catalog is temporarily unavailable.",
    );
  }
  hang = true;
  await failure(
    await invoke(),
    503,
    "temporarily_unavailable",
    "Catalog is temporarily unavailable.",
  );
  hang = false;
  body = JSON.stringify(envelope);
});
it("renders only the current configured-currency page with escaped authored names and safe error states", async () => {
  const item = {
    id: "123e4567-e89b-42d3-a456-426614174000",
    name: "<script>fictional</script> 🍲",
    price: {
      amount_minor: "9223372036854775807",
      currency: "KWD",
      exponent: 3,
    },
    available: false,
    version: "0",
  };
  body = JSON.stringify({
    data: {
      items: [item],
      next_cursor: "opaque",
      restaurant: { open: false, version: "0" },
    },
    request_id: "b".repeat(32),
  });
  const html = renderToStaticMarkup(
    await CatalogPage({ searchParams: Promise.resolve({ limit: "1" }) }),
  );
  expect(html).toContain("KWD 9223372036854775.807");
  expect(html).toContain("Restaurant is closed.");
  expect(html).toContain("Unavailable");
  expect(html).toContain("&lt;script&gt;fictional&lt;/script&gt;");
  expect(html).toContain('href="/dashboard/catalog?limit=1&amp;cursor=opaque"');
  expect(html).not.toContain("The menu is empty.");
  expect(html).not.toContain("End of menu.");
  body = "private upstream failure";
  const failed = renderToStaticMarkup(
    await CatalogPage({ searchParams: Promise.resolve({}) }),
  );
  expect(failed).toContain(
    "Catalog is temporarily unavailable. Try again later.",
  );
  expect(failed).not.toContain("fictional");
  expect(failed).not.toContain("private upstream");
  const invalid = renderToStaticMarkup(
    await CatalogPage({ searchParams: Promise.resolve({ limit: ["1", "2"] }) }),
  );
  expect(invalid).toContain("This catalog page is not valid. Start again.");
  body = JSON.stringify(envelope);
});
it("exports fail-closed public route methods and preserves the signed-in dashboard confirmation/sign-out", async () => {
  const html = renderToStaticMarkup(await Dashboard());
  expect(html).toContain("Staff dashboard");
  expect(html).toContain("Signed in as Catalog Staff.");
  expect(html).toContain("Staff access confirmed.");
  expect(html).toContain("Sign out");
  expect(html).toContain('href="/dashboard/catalog"');
  const empty = renderToStaticMarkup(
    await CatalogPage({ searchParams: Promise.resolve({}) }),
  );
  expect(empty).toContain("The menu is empty.");
  expect(empty).toContain("Restaurant is open.");
  expect(empty).toContain("End of menu.");
  const response = await route.GET(
    new Request(`${origin}/api/staff/catalog`, { headers: { cookie } }),
  );
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual(envelope);
  for (const method of [
    route.POST,
    route.PUT,
    route.PATCH,
    route.DELETE,
    route.HEAD,
    route.OPTIONS,
  ]) {
    const response = await method(
      new Request(`${origin}/api/staff/catalog`, { method: "POST" }),
    );
    expect(response.headers.get("allow")).toBe("GET");
    await failure(
      response,
      405,
      "method_not_allowed",
      "The method is not allowed.",
    );
  }
  boundary.config = undefined;
  await failure(
    await route.GET(new Request(`${origin}/api/staff/catalog`)),
    503,
    "temporarily_unavailable",
    "Catalog is temporarily unavailable.",
  );
  boundary.config = { auth: owner.auth, origin, apiOrigin };
});
it("authoritative logout blocks continuation before the upstream read", async () => {
  const headers = new Headers({ cookie, origin });
  await owner.auth.api.signOut({ headers });
  const before = calls;
  await failure(
    await invoke("?cursor=opaque"),
    401,
    "unauthenticated",
    "Authentication is required.",
  );
  expect(calls).toBe(before);
  await expect(Dashboard()).rejects.toThrow("redirect:/sign-in");
  await expect(
    CatalogPage({ searchParams: Promise.resolve({ cursor: "opaque" }) }),
  ).rejects.toThrow("redirect:/sign-in");
});
