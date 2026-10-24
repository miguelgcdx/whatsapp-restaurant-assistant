import { access, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { runLocked } from "../../scripts/local-auth/lock.mts";

const path = process.argv[2];
if (!path) throw new Error("missing test path");
if (process.argv[4] === "inside") {
  if (process.argv[3] === "crash") {
    await new Promise((resolve) => setTimeout(resolve, 300));
    process.exit(9);
  }
  if (process.argv[3] === "hold") {
    await writeFile(join(dirname(path), "ready"), "ready");
    let released = false;
    for (let i = 0; i < 100; i++) {
      try {
        await access(join(dirname(path), "release"));
        released = true;
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
    }
    if (!released) process.exit(8);
  }
  await writeFile(join(dirname(path), "probe"), "finished");
} else {
  const code = await runLocked(path, fileURLToPath(import.meta.url), [
    path,
    process.argv[3] ?? "normal",
    "inside",
  ]);
  process.exitCode = code;
}
