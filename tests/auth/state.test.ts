import {
  chmod,
  link,
  lstat,
  mkdtemp,
  readFile,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it, vi } from "vitest";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { loadOrCreateState, readState } from "../../src/server/auth/state.ts";

vi.mock("server-only", () => ({}));

it("durably preserves private credentials and refuses changed targets, unsafe modes and symlinks", async () => {
  const root = await mkdtemp(join(tmpdir(), "staff-state-"));
  const path = join(root, "private", "setup.json");
  const target = databaseTarget(
    "postgres://fixture@127.0.0.1:5544/restaurant_foundation_test",
  );
  try {
    const first = await loadOrCreateState(path, target);
    expect(first.fixtures.map((f) => [f.name, f.email])).toEqual([
      ["Alex", "alex.staff@example.test"],
      ["Sam", "sam.staff@example.test"],
    ]);
    expect(first.fixtures.every((f) => f.password.length >= 32)).toBe(true);
    expect((await lstat(join(root, "private"))).mode & 0o777).toBe(0o700);
    expect((await lstat(path)).mode & 0o777).toBe(0o600);
    const before = await readFile(path, "utf8");
    expect(
      JSON.stringify(await loadOrCreateState(path, target)) ===
        JSON.stringify(first),
    ).toBe(true);
    await expect(
      readState(path, { ...target, database: "restaurant_foundation_other" }),
    ).rejects.toThrow();
    expect((await readFile(path, "utf8")) === before).toBe(true);
    await link(path, `${path}.pending`);
    expect(
      JSON.stringify(await loadOrCreateState(path, target)) ===
        JSON.stringify(first),
    ).toBe(true);
    expect((await lstat(path)).nlink).toBe(1);
    await chmod(path, 0o644);
    await expect(readState(path, target)).rejects.toThrow();
    await chmod(path, 0o600);
    const interrupted = join(root, "private", "interrupted.json");
    await writeFile(`${interrupted}.pending`, "{partial", { mode: 0o600 });
    const recovered = await loadOrCreateState(interrupted, target);
    expect(recovered.fixtures.every((f) => f.password.length >= 32)).toBe(true);
    await symlink(path, join(root, "private", "link.json"));
    await expect(
      readState(join(root, "private", "link.json"), target),
    ).rejects.toThrow();
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
