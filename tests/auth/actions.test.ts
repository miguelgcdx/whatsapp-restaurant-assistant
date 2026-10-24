import { randomBytes } from "node:crypto";
import { getMigrations } from "better-auth/db/migration";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { signIn, signOut } from "../../src/app/sign-in/actions.ts";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { authoritativeSession } from "../../src/server/auth/session.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

// Only Next request/redirect plumbing is substituted; identity/session storage is real.
const boundary = vi.hoisted((): { headers: Headers; config: unknown } => ({
  headers: new Headers(),
  config: undefined,
}));
vi.mock("server-only", () => ({}));
vi.mock("next/headers", () => ({ headers: async () => boundary.headers }));
vi.mock("next/navigation", () => ({
  redirect: (path: string) => {
    throw new Error(`redirect:${path}`);
  },
}));
vi.mock("@/server/auth/runtime", () => ({
  runtimeAuth: async () => boundary.config,
}));
let pg: Awaited<ReturnType<typeof startAuthPostgres>>;
let owner: ReturnType<typeof createAuth>;
let cookie = "";
const origin = "http://127.0.0.1:3000";
const password = randomBytes(32).toString("base64url");
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
    body: { email: "alex.staff@example.test", name: "Alex", password },
    asResponse: true,
  });
  cookie = response.headers
    .getSetCookie()
    .map((v) => v.split(";")[0])
    .join("; ");
  boundary.config = { auth: owner.auth, origin };
});
afterAll(async () => {
  await owner?.pool.end();
  await pg?.close();
});
it("sign-in and sign-out actions reject missing/foreign Origin without changing real sessions", async () => {
  const form = new FormData();
  form.set("email", "alex.staff@example.test");
  form.set("password", password);
  for (const supplied of [null, "http://127.0.0.1:9999"]) {
    boundary.headers = new Headers({
      cookie,
      "sec-fetch-site": "same-origin",
      "x-forwarded-host": "127.0.0.1:3000",
    });
    if (supplied) boundary.headers.set("origin", supplied);
    await expect(signIn(form)).rejects.toThrow("redirect:/access-denied");
    await expect(signOut()).rejects.toThrow("redirect:/access-denied");
    expect(
      await authoritativeSession(owner.auth, new Headers({ cookie })),
    ).not.toBeNull();
    expect(
      (await owner.auth.api.listSessions({ headers: new Headers({ cookie }) }))
        .length,
    ).toBe(1);
  }
  boundary.headers = new Headers({ cookie, origin });
  await expect(signOut()).rejects.toThrow("redirect:/sign-in");
  expect(
    await authoritativeSession(owner.auth, new Headers({ cookie })),
  ).toBeNull();
});
