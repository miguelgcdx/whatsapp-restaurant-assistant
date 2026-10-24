import "server-only";
import { spawn } from "node:child_process";
import { constants } from "node:fs";
import { open } from "node:fs/promises";
import { dirname } from "node:path";
import { privateDirectory } from "../../src/server/auth/state.ts";

// flock holds fd3 over the entire worker; an OS/process crash cannot leave a stale lock.
export async function runLocked(
  statePath: string,
  entry: string,
  args: string[] = [],
) {
  await privateDirectory(dirname(statePath), true);
  const file = await open(
    `${statePath}.lock`,
    constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW,
    0o600,
  );
  try {
    const stat = await file.stat();
    if (
      !stat.isFile() ||
      stat.uid !== process.getuid?.() ||
      (stat.mode & 0o777) !== 0o600 ||
      stat.nlink !== 1
    )
      throw new Error("Local staff setup is incomplete.");
    return await new Promise<number>((resolve, reject) => {
      const child = spawn(
        "flock",
        [
          "--exclusive",
          "--timeout",
          "15",
          "--no-fork",
          "/proc/self/fd/3",
          process.execPath,
          "--conditions=react-server",
          entry,
          ...args,
        ],
        { stdio: ["ignore", "inherit", "inherit", file.fd] },
      );
      child.once("error", () =>
        reject(new Error("Local staff setup is incomplete.")),
      );
      child.once("exit", (code) => resolve(code ?? 1));
    });
  } finally {
    await file.close();
  }
}
