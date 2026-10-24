import "server-only";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { databaseTarget, localOrigin } from "../../src/server/auth/config.ts";
import { runLocked } from "./lock.mts";

const usage = `Usage: pnpm setup:auth [--help|-h]
Set up generated local Alex/Sam staff credentials and access.

Required environment:
  LOCAL_DATABASE_URL     Dedicated numeric-loopback PostgreSQL URL with explicit port,
                         user and restaurant_foundation_* database.
  BETTER_AUTH_URL        Exact numeric-loopback HTTP origin with explicit port.
Optional environment:
  LOCAL_DASHBOARD_STATE  Private setup-state path; default:
                         backend/.cache/dashboard/setup.json (from project root).

Setup preserves generated credentials in a same-user nonsymlink 0700 directory
and 0600 file before migrations/provisioning. Reruns reuse that state and never
reset passwords or reactivate revoked membership. Keep the state private.
Help performs no configuration validation, state writes, database work or setup.
`;

try {
  const args = process.argv.slice(2);
  if (args.length === 1 && (args[0] === "--help" || args[0] === "-h")) {
    process.stdout.write(usage);
  } else {
    if (args.length !== 0) throw new Error("arguments not supported");
    databaseTarget(process.env.LOCAL_DATABASE_URL);
    localOrigin(process.env.BETTER_AUTH_URL);
    const path = resolve(
      process.env.LOCAL_DASHBOARD_STATE ??
        "backend/.cache/dashboard/setup.json",
    );
    process.exitCode = await runLocked(
      path,
      fileURLToPath(new URL("./worker.mts", import.meta.url)),
    );
  }
} catch {
  process.stderr.write("Local staff setup is incomplete.\n");
  process.exitCode = 1;
}
