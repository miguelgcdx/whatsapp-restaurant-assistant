import { execFile } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

const exec = promisify(execFile);
// Test-owned cluster only: never consumes LOCAL_DATABASE_URL or inherited PG defaults.
export async function startAuthPostgres() {
  const root = await mkdtemp(join(tmpdir(), "staff-auth-pg-"));
  const data = join(root, "data");
  const env = {
    NODE_ENV: "test" as const,
    PATH: process.env.PATH,
    HOME: root,
    LANG: "C",
  };
  const run = (name: string, args: string[]) =>
    exec(name, args, { env, timeout: 20_000 });
  let started = false;
  const close = async () => {
    try {
      if (started)
        await run("pg_ctl", ["-D", data, "-m", "immediate", "-w", "stop"]);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  };
  try {
    await run("initdb", [
      "-D",
      data,
      "-U",
      "localtest",
      "-A",
      "trust",
      "--no-locale",
      "-E",
      "UTF8",
      "--no-sync",
    ]);
    const listener = createServer();
    await new Promise<void>((resolve, reject) => {
      listener.once("error", reject);
      listener.listen(0, "127.0.0.1", resolve);
    });
    const address = listener.address();
    if (!address || typeof address === "string")
      throw new Error("isolated PostgreSQL port unavailable");
    const port = address.port;
    await new Promise<void>((resolve, reject) =>
      listener.close((error) => (error ? reject(error) : resolve())),
    );
    await run("pg_ctl", [
      "-D",
      data,
      "-l",
      join(root, "postgres.log"),
      "-o",
      `-F -p ${port} -h 127.0.0.1 -k '' -c max_connections=20`,
      "-w",
      "start",
    ]);
    started = true;
    await run("createdb", [
      "-h",
      "127.0.0.1",
      "-p",
      String(port),
      "-U",
      "localtest",
      "restaurant_foundation_auth_test",
    ]);
    return {
      dsn: `postgres://localtest@127.0.0.1:${port}/restaurant_foundation_auth_test?sslmode=disable`,
      close,
    };
  } catch {
    await close();
    throw new Error("Isolated PostgreSQL prerequisite failed.");
  }
}
