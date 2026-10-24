import "server-only";
import { resolve } from "node:path";
import { databaseTarget, localOrigin } from "./config.ts";
import { createAuth } from "./factory.ts";
import { readState } from "./state.ts";

let pending: Promise<Awaited<ReturnType<typeof initialize>>> | undefined;
async function initialize() {
  const target = databaseTarget(process.env.LOCAL_DATABASE_URL);
  const origin = localOrigin(process.env.BETTER_AUTH_URL);
  const apiOrigin = localOrigin(process.env.STAFF_API_URL);
  // Operator-owned private credentials are runtime input, never bundled assets.
  const state = await readState(
    resolve(
      /* turbopackIgnore: true */
      process.env.LOCAL_DASHBOARD_STATE ??
        "backend/.cache/dashboard/setup.json",
    ),
    target,
  );
  return {
    ...createAuth({ target, origin, secret: state.secret, next: true }),
    origin,
    apiOrigin,
  };
}
// No DB/config/provisioning at module evaluation or during build/typegen.
export function runtimeAuth() {
  pending ??= initialize().catch((error) => {
    pending = undefined;
    throw error;
  });
  return pending;
}
