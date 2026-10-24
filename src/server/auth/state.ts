import "server-only";
import { randomBytes, randomUUID } from "node:crypto";
import { constants, type Stats } from "node:fs";
import { link, lstat, mkdir, open, unlink } from "node:fs/promises";
import { dirname, parse, resolve, sep } from "node:path";
import { z } from "zod";
import {
  configurationError,
  type DatabaseTarget,
  targetIdentity,
} from "./config.ts";

const fixture = (name: "Alex" | "Sam", email: string) =>
  z.strictObject({
    name: z.literal(name),
    email: z.literal(email),
    password: z.string().min(32).max(128),
  });
const stateSchema = z.strictObject({
  version: z.literal(1),
  target: z.strictObject({
    host: z.string(),
    port: z.number().int(),
    database: z.string(),
    user: z.string(),
  }),
  secret: z.string().min(48),
  restaurantID: z.uuid(),
  fixtures: z.tuple([
    fixture("Alex", "alex.staff@example.test"),
    fixture("Sam", "sam.staff@example.test"),
  ]),
});
export type SetupState = z.infer<typeof stateSchema>;

// Check every existing ancestor, not just the final component (no symlink traversal).
export async function privateDirectory(path: string, create: boolean) {
  const absolute = resolve(path);
  const root = parse(absolute).root;
  let current = root;
  for (const segment of absolute.slice(root.length).split(sep)) {
    current = resolve(current, segment);
    if (create) {
      try {
        await mkdir(current, { mode: 0o700 });
      } catch (error) {
        if (
          !(
            error instanceof Error &&
            "code" in error &&
            error.code === "EEXIST"
          )
        )
          throw configurationError();
      }
    }
    const stat = await lstat(current);
    if (!stat.isDirectory() || stat.isSymbolicLink())
      throw configurationError();
  }
  const stat = await lstat(absolute);
  if (stat.uid !== process.getuid?.() || (stat.mode & 0o777) !== 0o700)
    throw configurationError();
}
function privateFile(stat: Stats) {
  if (
    !stat.isFile() ||
    stat.isSymbolicLink() ||
    stat.uid !== process.getuid?.() ||
    (stat.mode & 0o777) !== 0o600 ||
    stat.size > 16384
  )
    throw configurationError();
}
const missing = (error: unknown) =>
  error instanceof Error && "code" in error && error.code === "ENOENT";
async function syncDirectory(path: string) {
  const directory = await open(dirname(path), constants.O_RDONLY);
  try {
    await directory.sync();
  } finally {
    await directory.close();
  }
}
export async function readState(
  path: string,
  target: DatabaseTarget,
): Promise<SetupState> {
  try {
    await privateDirectory(dirname(path), false);
    const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    try {
      const stat = await handle.stat();
      privateFile(stat);
      if (stat.nlink !== 1) throw configurationError();
      const state = stateSchema.parse(
        JSON.parse(await handle.readFile("utf8")),
      );
      if (
        JSON.stringify(state.target) !== JSON.stringify(targetIdentity(target))
      )
        throw configurationError();
      return state;
    } finally {
      await handle.close();
    }
  } catch {
    throw configurationError();
  }
}
// Caller holds the setup OS lock. No provisioning begins until final file + directory fsync.
export async function loadOrCreateState(
  path: string,
  target: DatabaseTarget,
): Promise<SetupState> {
  await privateDirectory(dirname(path), true);
  const pending = `${path}.pending`;
  let existing: Stats | undefined;
  try {
    existing = await lstat(path);
  } catch (error) {
    if (!missing(error)) throw configurationError();
  }
  if (existing) {
    privateFile(existing);
    // Recover a crash between publication and unlinking the staging hard link.
    if (existing.nlink === 2) {
      const staged = await lstat(pending);
      privateFile(staged);
      if (existing.ino !== staged.ino || existing.dev !== staged.dev)
        throw configurationError();
      await unlink(pending);
      await syncDirectory(path);
    }
    return readState(path, target);
  }
  // Unpublished staging data was never used for provisioning; no secret in use changes.
  try {
    const abandoned = await lstat(pending);
    privateFile(abandoned);
    if (abandoned.nlink !== 1) throw configurationError();
    await unlink(pending);
  } catch (error) {
    if (!missing(error)) throw configurationError();
  }
  const state: SetupState = {
    version: 1,
    target: targetIdentity(target),
    secret: randomBytes(48).toString("base64url"),
    restaurantID: randomUUID(),
    fixtures: [
      {
        name: "Alex",
        email: "alex.staff@example.test",
        password: randomBytes(32).toString("base64url"),
      },
      {
        name: "Sam",
        email: "sam.staff@example.test",
        password: randomBytes(32).toString("base64url"),
      },
    ],
  };
  const handle = await open(
    pending,
    constants.O_WRONLY |
      constants.O_CREAT |
      constants.O_EXCL |
      constants.O_NOFOLLOW,
    0o600,
  );
  try {
    await handle.writeFile(`${JSON.stringify(state)}\n`);
    await handle.sync();
  } finally {
    await handle.close();
  }
  // link, unlike rename, cannot overwrite an existing final credential file.
  await link(pending, path);
  await syncDirectory(path);
  await unlink(pending);
  await syncDirectory(path);
  return readState(path, target);
}
