import { randomBytes } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getMigrations } from "better-auth/db/migration";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { provisionFixtures } from "../../scripts/local-auth/provision.mts";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { loadOrCreateState } from "../../src/server/auth/state.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

vi.mock("server-only", () => ({}));
let pg: Awaited<ReturnType<typeof startAuthPostgres>>;
let owner: ReturnType<typeof createAuth>;
let root = "";
let state: Awaited<ReturnType<typeof loadOrCreateState>>;
const origin = "http://127.0.0.1:3000";
beforeAll(async () => {
  pg = await startAuthPostgres();
  root = await mkdtemp(join(tmpdir(), "setup-component-"));
  const target = databaseTarget(pg.dsn);
  state = await loadOrCreateState(join(root, "private", "setup.json"), target);
  owner = createAuth({ target, origin, secret: state.secret, setup: true });
  await owner.pool.query("CREATE SCHEMA auth");
  await (await getMigrations(owner.auth.options)).runMigrations();
});
afterAll(async () => {
  await owner?.pool.end();
  await pg?.close();
  if (root) await rm(root, { recursive: true, force: true });
});
it("recovers cross-owner partial failure with preserved passwords and cleans setup-created sessions", async () => {
  const members = new Set<string>();
  let fail = true;
  let ensureCalls = 0;
  // Test-only operator seam, not actual Go membership proof.
  const ensure = async (subject: string) => {
    ensureCalls++;
    if (fail && ensureCalls === 2) throw new Error("injected operator failure");
    members.add(subject);
    return true;
  };
  await expect(
    provisionFixtures(owner.auth, state, origin, ensure),
  ).rejects.toThrow("Local staff setup is incomplete.");
  expect(members.size).toBe(1);
  fail = false;
  const result = await provisionFixtures(owner.auth, state, origin, ensure);
  expect(result.complete).toBe(true);
  expect(members.size).toBe(2);
  // Public session listing proves only the newly signed-in session remains.
  for (const fixture of state.fixtures) {
    const response = await owner.auth.api.signInEmail({
      body: { email: fixture.email, password: fixture.password },
      asResponse: true,
    });
    expect(response.status).toBe(200);
    const cookie = response.headers
      .getSetCookie()
      .map((v) => v.split(";")[0])
      .join("; ");
    const sessions = await owner.auth.api.listSessions({
      headers: new Headers({ cookie }),
    });
    expect(sessions.length).toBe(1);
    await owner.auth.api.signOut({ headers: new Headers({ cookie, origin }) });
  }
});
it("never reactivates a revoked membership or resets an unknown existing password", async () => {
  let calls = 0;
  await expect(
    provisionFixtures(owner.auth, state, origin, async () => {
      calls++;
      return false;
    }),
  ).rejects.toThrow("Local staff setup is incomplete.");
  expect(calls).toBe(1);
  const changed = {
    ...state,
    fixtures: [
      { ...state.fixtures[0], password: randomBytes(32).toString("base64url") },
      state.fixtures[1],
    ] satisfies typeof state.fixtures,
  };
  calls = 0;
  await expect(
    provisionFixtures(owner.auth, changed, origin, async () => {
      calls++;
      return true;
    }),
  ).rejects.toThrow("Local staff setup is incomplete.");
  expect(calls).toBe(0);
  const response = await owner.auth.api.signInEmail({
    body: {
      email: state.fixtures[0].email,
      password: state.fixtures[0].password,
    },
    asResponse: true,
  });
  expect(response.status).toBe(200);
  const cookie = response.headers
    .getSetCookie()
    .map((v) => v.split(";")[0])
    .join("; ");
  await owner.auth.api.signOut({ headers: new Headers({ cookie, origin }) });
});
