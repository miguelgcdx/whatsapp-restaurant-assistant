import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { chmod, link, mkdtemp, readFile, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getMigrations } from "better-auth/db/migration";
import { it, vi } from "vitest";
import { provisionFixtures } from "../../scripts/local-auth/provision.mts";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { authRequest } from "../../src/server/auth/http.ts";
import { authoritativeSession } from "../../src/server/auth/session.ts";
import { loadOrCreateState, readState } from "../../src/server/auth/state.ts";
import { staffAccessRequest } from "../../src/server/staff-access.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

vi.mock("server-only", () => ({}));

// Retained independent public probes; this file never reads auth-private tables.
it("preserves credentials across rejected state operations and publication recovery", async () => {
  const root = await mkdtemp(join(tmpdir(), "independent-state-"));
  const path = join(root, "private", "setup.json");
  const target = databaseTarget(
    "postgres://fixture@127.0.0.1:5544/restaurant_foundation_test",
  );
  try {
    await loadOrCreateState(path, target);
    const digest = async () =>
      createHash("sha256")
        .update(await readFile(path))
        .digest("hex");
    const before = await digest();
    const rejected = async (operation: () => Promise<unknown>) => {
      await assert.rejects(operation);
      assert.equal((await digest()) === before, true);
    };
    await rejected(() => loadOrCreateState(path, { ...target, user: "other" }));
    await chmod(path, 0o640);
    await rejected(() => readState(path, target));
    await chmod(path, 0o600);
    await symlink(join(root, "private"), join(root, "alias"));
    await rejected(() => readState(join(root, "alias", "setup.json"), target));
    await link(path, `${path}.pending`);
    await loadOrCreateState(path, target);
    assert.equal((await digest()) === before, true);
    for (const query of [
      "host=remote.invalid",
      "sslmode=disable&sslmode=disable",
      "service=external",
      "options=-csearch_path=public",
    ]) {
      assert.throws(
        () =>
          databaseTarget(
            `postgres://fixture@127.0.0.1:5544/restaurant_foundation_test?${query}`,
          ),
        { message: "Local authentication configuration is unavailable." },
      );
      assert.equal((await digest()) === before, true);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it("fails conflicting partial setup safely and isolates real sessions", async () => {
  const root = await mkdtemp(join(tmpdir(), "independent-auth-"));
  const pg = await startAuthPostgres();
  const originalEnv = { ...process.env };
  let owner: ReturnType<typeof createAuth> | undefined;
  let runtime: ReturnType<typeof createAuth> | undefined;
  const origin = "http://127.0.0.1:3000";
  const cookies = (response: Response) =>
    response.headers
      .getSetCookie()
      .map((value) => value.split(";")[0])
      .join("; ");
  try {
    const target = databaseTarget(pg.dsn);
    const path = join(root, "private", "setup.json");
    const state = await loadOrCreateState(path, target);
    const digest = async () =>
      createHash("sha256")
        .update(await readFile(path))
        .digest("hex");
    const before = await digest();
    Object.assign(process.env, {
      PGHOST: "remote.invalid",
      PGPORT: "1",
      PGDATABASE: "postgres",
      PGUSER: "other",
      PGPASSWORD: "wrong",
      PGOPTIONS: "-c search_path=public",
      PGSSLMODE: "require",
      PGSERVICE: "external",
      PGSERVICEFILE: "/nonexistent",
    });
    const setup = createAuth({
      target,
      origin,
      secret: state.secret,
      setup: true,
    });
    owner = setup;
    await setup.pool.query("CREATE SCHEMA auth");
    await (await getMigrations(setup.auth.options)).runMigrations();
    await (await getMigrations(setup.auth.options)).runMigrations();
    const active = createAuth({ target, origin, secret: state.secret });
    runtime = active;
    const otherPassword = randomBytes(32).toString("base64url");
    const conflict = await setup.auth.api.signUpEmail({
      body: { ...state.fixtures[1], password: otherPassword },
      asResponse: true,
    });
    assert.equal(conflict.ok, true);
    await setup.auth.api.signOut({
      headers: new Headers({ cookie: cookies(conflict), origin }),
    });
    let ensured = 0;
    await assert.rejects(
      () =>
        provisionFixtures(setup.auth, state, origin, async () => {
          ensured++;
          return true;
        }),
      { message: "Local staff setup is incomplete." },
    );
    assert.equal(ensured, 1);
    assert.equal((await digest()) === before, true);
    const signIn = (index: 0 | 1, password = state.fixtures[index].password) =>
      setup.auth.api.signInEmail({
        body: { email: state.fixtures[index].email, password },
        asResponse: true,
      });
    assert.equal((await signIn(1)).ok, false);
    const sam = await signIn(1, otherPassword);
    assert.equal(sam.ok, true);
    await setup.auth.api.signOut({
      headers: new Headers({ cookie: cookies(sam), origin }),
    });
    const alex1 = await signIn(0);
    const alex2 = await signIn(0);
    assert.equal(alex1.ok && alex2.ok, true);
    const first = new Headers({ cookie: cookies(alex1), origin });
    const second = new Headers({ cookie: cookies(alex2), origin });
    assert.equal(
      (await setup.auth.api.listSessions({ headers: first })).length,
      2,
    );
    await active.auth.api.signOut({ headers: first });
    assert.equal(
      (await authoritativeSession(active.auth, first)) === null,
      true,
    );
    assert.equal(
      (await authoritativeSession(active.auth, second)) !== null,
      true,
    );
    assert.equal(
      typeof (await active.auth.api.getToken({ headers: second })).token,
      "string",
    );
    for (const method of ["GET", "POST"]) {
      for (const endpoint of [
        "get-session",
        "token",
        "sign-up/email",
        "sign-in/email",
        "sign-out",
      ]) {
        const response = await authRequest(
          new Request(`${origin}/api/auth/${endpoint}`, {
            method,
            headers: second,
          }),
          active.auth,
          origin,
        );
        assert.equal(response.status, 404);
        assert.equal(
          await response.text(),
          '{"error":{"code":"not_found","message":"Not found."}}',
        );
        assert.equal(response.headers.has("set-cookie"), false);
        assert.equal(response.headers.has("set-auth-jwt"), false);
      }
    }
    let authReads = 0;
    const config = {
      get auth() {
        authReads++;
        return setup.auth;
      },
      origin,
      apiOrigin: "http://127.0.0.1:1",
    };
    const malformedFraming: HeadersInit[] = [
      { "content-length": "00" },
      { "content-length": "-1" },
      { "transfer-encoding": "identity" },
    ];
    for (const headers of malformedFraming) {
      const response = await staffAccessRequest(
        new Request(`${origin}/api/staff/access`, { headers }),
        config,
      );
      assert.equal(response.status, 400);
      assert.equal(
        await response.text(),
        '{"error":{"code":"invalid_input","message":"Invalid request."}}',
      );
      assert.equal(authReads, 0);
    }
    assert.equal((await digest()) === before, true);
  } finally {
    for (const key of Object.keys(process.env)) {
      if (!(key in originalEnv)) delete process.env[key];
    }
    Object.assign(process.env, originalEnv);
    await owner?.pool.end();
    await runtime?.pool.end();
    await pg.close();
    await rm(root, { recursive: true, force: true });
  }
});
