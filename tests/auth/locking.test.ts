import { execFile } from "node:child_process";
import { access, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { promisify } from "node:util";
import { expect, it } from "vitest";

const exec = promisify(execFile);
const worker = resolve("tests/auth/lock-probe.mts");
const run = (path: string, mode: string) =>
  exec(process.execPath, ["--conditions=react-server", worker, path, mode], {
    env: { NODE_ENV: "test", PATH: process.env.PATH },
    timeout: 10_000,
  });

it("releases the OS lock after a crashed owner", async () => {
  const root = await mkdtemp(join(tmpdir(), "staff-lock-"));
  const path = join(root, "private", "setup.json");
  try {
    const first = run(path, "crash").then(
      () => "fulfilled",
      () => "rejected",
    );
    await new Promise((resolve) => setTimeout(resolve, 100));
    const second = run(path, "normal").then(
      () => "fulfilled",
      () => "rejected",
    );
    const results = await Promise.all([first, second]);
    expect(results).toEqual(["rejected", "fulfilled"]);
    expect(await readFile(join(root, "private", "probe"), "utf8")).toBe(
      "finished",
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it("locks the actual private state inode, not a shared relative descriptor filename", async () => {
  const root = await mkdtemp(join(tmpdir(), "staff-lock-inode-"));
  const firstPath = join(root, "first", "setup.json");
  let first: Promise<boolean> | undefined;
  try {
    first = run(firstPath, "hold").then(
      () => true,
      () => false,
    );
    let ready = false;
    for (let i = 0; i < 100; i++) {
      try {
        await access(join(root, "first", "ready"));
        ready = true;
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
    }
    expect(ready).toBe(true);
    // A different private state lock must make progress while the first is held.
    await run(join(root, "second", "setup.json"), "normal");
    await writeFile(join(root, "first", "release"), "release");
    expect(await first).toBe(true);
  } finally {
    if (first) await first;
    await rm(root, { recursive: true, force: true });
  }
});
