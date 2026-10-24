import { randomBytes } from "node:crypto";
import { getMigrations } from "better-auth/db/migration";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { authRequest } from "../../src/server/auth/http.ts";
import { authoritativeSession } from "../../src/server/auth/session.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

vi.mock("server-only", () => ({}));
const origin = "http://127.0.0.1:3000";
let postgres: Awaited<ReturnType<typeof startAuthPostgres>>;
let setup: ReturnType<typeof createAuth>;
let runtime: ReturnType<typeof createAuth>;
const password = randomBytes(24).toString("base64url");
let cookie = "";
let subject = "";

beforeAll(async () => {
  postgres = await startAuthPostgres();
  const config = {
    target: databaseTarget(postgres.dsn),
    origin,
    secret: randomBytes(48).toString("base64url"),
  };
  setup = createAuth({ ...config, setup: true });
  await setup.pool.query("CREATE SCHEMA IF NOT EXISTS auth");
  const migration = await getMigrations(setup.auth.options);
  await migration.runMigrations();
  runtime = createAuth(config);
  const response = await setup.auth.api.signUpEmail({
    headers: new Headers({ origin }),
    body: { email: "alex.staff@example.test", name: "Alex", password },
    asResponse: true,
  });
  expect(response.status).toBe(200);
  cookie = response.headers
    .getSetCookie()
    .map((value) => value.split(";")[0])
    .join("; ");
  const session = await authoritativeSession(
    setup.auth,
    new Headers({ cookie }),
  );
  expect(session).not.toBeNull();
  subject = session?.user.id ?? "";
});
afterAll(async () => {
  await setup?.pool.end();
  await runtime?.pool.end();
  await postgres?.close();
});

it("uses real authoritative sessions and subject-only Ed25519 official tokens, not browser bearer authority", async () => {
  const headers = new Headers({ cookie });
  const session = await authoritativeSession(runtime.auth, headers);
  expect(session?.user.id === subject).toBe(true);
  expect(
    await authoritativeSession(
      runtime.auth,
      new Headers({
        cookie: "better-auth.session_token=forged",
        authorization: "Bearer forged",
      }),
    ),
  ).toBeNull();
  const token = await runtime.auth.api.getToken({ headers });
  const [encodedHeader, encodedPayload] = token.token.split(".");
  const header = JSON.parse(
    Buffer.from(encodedHeader ?? "", "base64url").toString(),
  );
  const payload = JSON.parse(
    Buffer.from(encodedPayload ?? "", "base64url").toString(),
  );
  expect(header.alg).toBe("EdDSA");
  expect(payload).toEqual({
    sub: subject,
    iss: origin,
    aud: "restaurant-assistant-api",
    iat: expect.any(Number),
    exp: expect.any(Number),
  });
  expect(payload.exp - payload.iat).toBe(60);
  const sessionResponse = await runtime.auth.api.getSession({
    headers,
    asResponse: true,
  });
  expect(sessionResponse.headers.has("set-auth-jwt")).toBe(false);
});

it("public HTTP exposes only JWKS; token/signup/session/signin endpoints stay closed", async () => {
  for (const path of [
    "token",
    "sign-up/email",
    "get-session",
    "sign-in/email",
    "sign-out",
  ]) {
    const response = await authRequest(
      new Request(`${origin}/api/auth/${path}`, { headers: { cookie } }),
      runtime.auth,
      origin,
    );
    expect(response.status).toBe(404);
    expect(await response.text()).toBe(
      '{"error":{"code":"not_found","message":"Not found."}}',
    );
    expect(response.headers.has("set-auth-jwt")).toBe(false);
  }
  const jwks = await authRequest(
    new Request(`${origin}/api/auth/jwks`),
    runtime.auth,
    origin,
  );
  expect(jwks.status).toBe(200);
  const body = await jwks.json();
  expect(body.keys.length).toBeGreaterThan(0);
  expect(
    body.keys.every(
      (key: Record<string, unknown>) =>
        key.alg === "EdDSA" && key.crv === "Ed25519" && !("d" in key),
    ),
  ).toBe(true);
  const foreign = await authRequest(
    new Request(`${origin}/api/auth/jwks`, {
      headers: { origin: "http://127.0.0.1:9999" },
    }),
    runtime.auth,
    origin,
  );
  expect(foreign.status).toBe(403);
  await expect(
    runtime.auth.api.signUpEmail({
      body: { name: "Sam", email: "sam.staff@example.test", password },
    }),
  ).rejects.toMatchObject({
    body: { code: "EMAIL_PASSWORD_SIGN_UP_DISABLED" },
  });
});

it("immediately rejects a publicly signed-out session despite its old cookie", async () => {
  await runtime.auth.api.signOut({ headers: new Headers({ cookie, origin }) });
  expect(
    await authoritativeSession(runtime.auth, new Headers({ cookie })),
  ).toBeNull();
  await expect(
    runtime.auth.api.getToken({ headers: new Headers({ cookie }) }),
  ).rejects.toMatchObject({ statusCode: 401 });
});

it("rejects an expired session without private adapter or table manipulation", async () => {
  const expiring = createAuth({
    target: databaseTarget(postgres.dsn),
    origin,
    secret: String(runtime.auth.options.secret),
    sessionExpiresIn: 1,
  });
  try {
    const response = await expiring.auth.api.signInEmail({
      body: { email: "alex.staff@example.test", password },
      asResponse: true,
    });
    expect(response.status).toBe(200);
    const shortCookie = response.headers
      .getSetCookie()
      .map((value) => value.split(";")[0])
      .join("; ");
    expect(
      await authoritativeSession(
        expiring.auth,
        new Headers({ cookie: shortCookie }),
      ),
    ).not.toBeNull();
    await new Promise((resolve) => setTimeout(resolve, 1100));
    expect(
      await authoritativeSession(
        expiring.auth,
        new Headers({ cookie: shortCookie }),
      ),
    ).toBeNull();
  } finally {
    await expiring.pool.end();
  }
});
