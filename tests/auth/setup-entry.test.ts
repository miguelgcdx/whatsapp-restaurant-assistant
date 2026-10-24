import { execFile } from "node:child_process";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { expect, it } from "vitest";

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

function runSetup(args: string[], statePath?: string) {
  return new Promise<{ code: number; stdout: string; stderr: string }>(
    (resolveResult) => {
      execFile(
        process.execPath,
        [
          "--conditions=react-server",
          resolve("scripts/local-auth/setup.mts"),
          ...args,
        ],
        {
          env: {
            NODE_ENV: "test",
            PATH: process.env.PATH,
            PGHOST: "remote.invalid",
            PGDATABASE: "postgres",
            ...(statePath
              ? {
                  LOCAL_DATABASE_URL: "invalid-local-target",
                  BETTER_AUTH_URL: "invalid-local-origin",
                  LOCAL_DASHBOARD_STATE: statePath,
                }
              : {}),
          },
          timeout: 5000,
        },
        (error, stdout, stderr) =>
          resolveResult({
            code: error
              ? typeof error.code === "number"
                ? error.code
                : -1
              : 0,
            stdout,
            stderr,
          }),
      );
    },
  );
}

it("native setup help exits cleanly without config, state, database or provisioning", async () => {
  const root = await mkdtemp(join(tmpdir(), "staff-setup-help-"));
  try {
    for (const flag of ["--help", "-h"]) {
      expect(
        await runSetup([flag], join(root, "private", "setup.json")),
      ).toEqual({
        code: 0,
        stdout: usage,
        stderr: "",
      });
      expect(await readdir(root)).toEqual([]);
      expect(await runSetup([flag])).toEqual({
        code: 0,
        stdout: usage,
        stderr: "",
      });
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it("preserves redacted invalid-args and missing-config behavior", async () => {
  for (const args of [[], ["--unknown"], ["--help", "extra"], ["extra"]]) {
    expect(await runSetup(args)).toEqual({
      code: 1,
      stdout: "",
      stderr: "Local staff setup is incomplete.\n",
    });
  }
});
