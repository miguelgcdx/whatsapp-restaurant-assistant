import "server-only";
import { execFile } from "node:child_process";
import { resolve } from "node:path";
import { promisify } from "node:util";
import { getMigrations } from "better-auth/db/migration";
import { z } from "zod";
import { databaseTarget, localOrigin } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { loadOrCreateState } from "../../src/server/auth/state.ts";
import { provisionFixtures } from "./provision.mts";

const exec = promisify(execFile);
const membership = z.strictObject({
  restaurant_id: z.uuid(),
  auth_user_id: z.string(),
  active: z.boolean(),
});
let owner: ReturnType<typeof createAuth> | undefined;
try {
  const target = databaseTarget(process.env.LOCAL_DATABASE_URL);
  const origin = localOrigin(process.env.BETTER_AUTH_URL);
  const path = resolve(
    process.env.LOCAL_DASHBOARD_STATE ?? "backend/.cache/dashboard/setup.json",
  );
  // This worker is an internal entrypoint. setup.mts must own fd3's OS lock.
  const { fstatSync } = await import("node:fs");
  if (!fstatSync(3).isFile()) throw new Error("setup lock unavailable");
  const state = await loadOrCreateState(path, target);
  const go = async (args: string[]) =>
    exec("go", ["run", ...args], {
      cwd: resolve("backend"),
      env: {
        ...process.env,
        GOPROXY: "off",
        GOTOOLCHAIN: "local",
        GOTELEMETRY: "off",
      },
      timeout: 30000,
      maxBuffer: 16384,
    });
  await go(["./cmd/local-migrate"]);
  owner = createAuth({ target, origin, secret: state.secret, setup: true });
  await owner.pool.query("CREATE SCHEMA IF NOT EXISTS auth");
  // Public pinned 1.7.12 library migration API: same planner/executor used by auth migrate.
  await (await getMigrations(owner.auth.options)).runMigrations();
  const result = await provisionFixtures(
    owner.auth,
    state,
    origin,
    async (subject) => {
      const output = await go([
        "./cmd/local-staff-membership",
        "-restaurant-id",
        state.restaurantID,
        "-auth-user-id",
        subject,
        "-action",
        "ensure",
      ]);
      if (output.stderr) throw new Error("operator unavailable");
      const response = membership.parse(JSON.parse(output.stdout));
      if (
        response.restaurant_id !== state.restaurantID ||
        response.auth_user_id !== subject
      )
        throw new Error("operator mismatch");
      return response.active;
    },
  );
  process.stdout.write(
    `${JSON.stringify({ complete: result.complete, restaurant_id: result.restaurantID, state_path: path })}\n`,
  );
} catch {
  process.stderr.write("Local staff setup is incomplete.\n");
  process.exitCode = 1;
} finally {
  await owner?.pool.end();
}
