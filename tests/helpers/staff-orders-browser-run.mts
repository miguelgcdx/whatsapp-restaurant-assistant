import { type ChildProcess, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { constants } from "node:fs";
import {
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  realpath,
  symlink,
} from "node:fs/promises";
import { dirname, isAbsolute, join, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { z } from "zod";

const NODE = "/home/miguelgcd/.local/share/mise/installs/node/26.11.1/bin/node";
const BROWSERS = "/tmp/restaurant-chromium-install-qmEN2d/browsers";
const usage = `Usage: ${NODE} --conditions=react-server tests/integration/staff-orders-browser.mts --help
       ${NODE} --conditions=react-server tests/integration/staff-orders-browser.mts --execute --manifest ABSOLUTE_PUBLIC_JSON --manifest-sha256 SHA256

Static preparation does not authorize execution. Parent must approve a fresh public
manifest/source window and the single run. See tests/integration/staff-orders-browser.md.
One build300s, combined host+seed compile120s, setup120s, seed120s,
readiness30s per listener, journey60s. No retries, installs or preview changes.
Only stage/status constants are printed. Protected evidence is retained in /tmp.
`;
const absolute = z.string().refine(isAbsolute);
const pin = z.string().regex(/^[a-f0-9]{64}$/);
const fileSchema = z.strictObject({
  root: z.enum(["frontend", "backend"]),
  path: z.string().min(1),
  sha256: pin,
  bytes: z
    .number()
    .int()
    .nonnegative()
    .max(8 * 1024 * 1024),
});
const manifestSchema = z.strictObject({
  frontend_root: absolute,
  backend_root: absolute,
  module_cache: absolute,
  files: z.array(fileSchema).min(1),
  backend_manifest: z.strictObject({
    path: absolute,
    sha256: z.literal(
      "3c24c98316caf3b91db72c94e05ca56e0a891cfd88721d3af0135d4f1c0c270c",
    ),
  }),
  ports: z.strictObject({
    postgres: z.number().int().min(1024).max(65535),
    next: z.number().int().min(1024).max(65535),
    staff: z.number().int().min(1024).max(65535),
  }),
});
const seedSchema = z.strictObject({
  order_id: z.uuid(),
  summary_id: z.uuid(),
  initial_version: z.string().regex(/^[1-9][0-9]*$/),
  unit_count: z.number().int().min(2),
});
export type JourneyInput = {
  origin: string;
  env: Record<string, string>;
  fixture: { name: string; email: string; password: string };
  seed: z.infer<typeof seedSchema>;
  signal: AbortSignal;
};
type Manifest = z.infer<typeof manifestSchema>;
type Stage =
  | "arguments"
  | "snapshot"
  | "build"
  | "compile"
  | "postgres"
  | "setup"
  | "listeners"
  | "seed"
  | "journey"
  | "shutdown";
type OwnedChild = { child: ChildProcess; closed: Promise<number> };
const fail = () => new Error("isolated run stopped");
const digest = (bytes: Buffer) =>
  createHash("sha256").update(bytes).digest("hex");
async function within<T>(
  pending: Promise<T>,
  milliseconds: number,
  signal?: AbortSignal,
): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  let abort = () => {};
  try {
    return await Promise.race([
      pending,
      new Promise<never>((_, reject) => {
        abort = () => reject(fail());
        if (milliseconds <= 0 || signal?.aborted) {
          reject(fail());
          return;
        }
        timer = setTimeout(abort, milliseconds);
        signal?.addEventListener("abort", abort, { once: true });
      }),
    ]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", abort);
  }
}

// Never follow source symlinks, including symlinked ancestors. node_modules is
// deliberately the sole exception, separately checked and linked below.
async function nonsymlink(path: string) {
  if (!isAbsolute(path) || resolve(path) !== path) throw fail();
  let current: string = sep;
  for (const part of path.slice(1).split(sep)) {
    current = join(current, part);
    if ((await lstat(current)).isSymbolicLink()) throw fail();
  }
}
async function publicBytes(path: string) {
  await nonsymlink(path);
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await file.stat();
    if (!stat.isFile() || stat.size > 8 * 1024 * 1024) throw fail();
    return await file.readFile();
  } finally {
    await file.close();
  }
}
async function privateWrite(path: string, data: string | Buffer) {
  const file = await open(
    path,
    constants.O_CREAT |
      constants.O_EXCL |
      constants.O_WRONLY |
      constants.O_NOFOLLOW,
    0o600,
  );
  try {
    await file.writeFile(data);
    await file.sync();
  } finally {
    await file.close();
  }
}
async function privateJSON(path: string): Promise<unknown> {
  await nonsymlink(path);
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await file.stat();
    if (
      !stat.isFile() ||
      stat.nlink !== 1 ||
      stat.uid !== process.getuid?.() ||
      (stat.mode & 0o777) !== 0o600 ||
      stat.size > 16384
    )
      throw fail();
    return JSON.parse(await file.readFile("utf8"));
  } finally {
    await file.close();
  }
}
function publicPath(file: z.infer<typeof fileSchema>) {
  const path = file.path;
  if (
    path
      .split("/")
      .some(
        (part) =>
          !part ||
          part === ".." ||
          part.startsWith(".") ||
          /^(?:node_modules|credentials|data|pgdata|cache|caches|browserstate)$/i.test(
            part,
          ),
      ) ||
    /(?:secret|credential|\.env)/i.test(path)
  )
    throw fail();
  const allowed =
    file.root === "frontend"
      ? /^(?:(?:src|scripts\/local-auth|tests\/helpers|tests\/integration)\/.*\.(?:ts|tsx|mts|css)|public\/.*\.(?:svg|png|webp|jpg|ico|woff2)|(?:package\.json|pnpm-lock\.yaml|tsconfig\.json|next-env\.d\.ts|next\.config\.ts|postcss\.config\.mjs))$/
      : /^(?:go\.(?:mod|sum)|[^/]+\.md|(?:cmd|internal|docs|migrations)\/.*\.(?:go|sql|md))$/;
  if (!allowed.test(path)) throw fail();
}
async function verifySources(manifest: Manifest) {
  for (const file of manifest.files) {
    publicPath(file);
    const root =
      file.root === "frontend" ? manifest.frontend_root : manifest.backend_root;
    const bytes = await publicBytes(join(root, file.path));
    if (bytes.length !== file.bytes || digest(bytes) !== file.sha256)
      throw fail();
  }
}
async function bindBackend(manifest: Manifest, root: string): Promise<Manifest> {
  // The final owner manifest includes the seed; no core-only/separate-seed seam.
  if (manifest.files.some((file) => file.root !== "frontend")) throw fail();
  const bytes = await publicBytes(manifest.backend_manifest.path);
  if (digest(bytes) !== manifest.backend_manifest.sha256) throw fail();
  const full = z
    .object({
      schema: z.literal("restaurant-public-source-manifest/v1"),
      source_root: absolute,
      count: z.literal(254),
      total_bytes: z.literal(2096701),
      status: z.literal("FULL_PUBLIC_READSET_PINNED"),
      mutable_exceptions: z.array(z.never()),
      entries: z.array(
        z.strictObject({
          path: z.string(),
          bytes: z.number().int().nonnegative(),
          sha256: pin,
        }),
      ),
    })
    .parse(JSON.parse(bytes.toString("utf8")));
  if (
    join(full.source_root, "backend") !== manifest.backend_root ||
    full.entries.length !== full.count ||
    full.entries.reduce((total, entry) => total + entry.bytes, 0) !==
      full.total_bytes
  )
    throw fail();
  const seed = full.entries.find(
    (entry) =>
      entry.path === "backend/cmd/local-staff-api/orders_browser_seed_test.go",
  );
  if (
    !seed ||
    seed.bytes !== 19355 ||
    seed.sha256 !==
      "f8d5f1af9ac55b8e6537adf74cfb8798096dd84f9d13586a831e112d85e44e3c"
  )
    throw fail();
  const backend = full.entries.map((entry) => {
    if (!entry.path.startsWith("backend/")) throw fail();
    return {
      root: "backend" as const,
      path: entry.path.slice("backend/".length),
      bytes: entry.bytes,
      sha256: entry.sha256,
    };
  });
  await privateWrite(join(root, "backend-full-manifest.json"), bytes);
  return { ...manifest, files: [...manifest.files, ...backend] };
}
async function snapshot(manifest: Manifest, root: string) {
  if (
    manifest.frontend_root !==
      resolve(fileURLToPath(new URL("../../", import.meta.url))) ||
    manifest.module_cache !== join(manifest.backend_root, ".cache/modules") ||
    manifest.files.filter((file) => file.root === "backend").length !== 254 ||
    new Set(Object.values(manifest.ports)).size !== 3
  )
    throw fail();
  const keys = manifest.files.map((file) => `${file.root}:${file.path}`);
  if (new Set(keys).size !== keys.length) throw fail();
  for (const required of [
    "frontend:package.json",
    "frontend:tsconfig.json",
    "frontend:next-env.d.ts",
    "frontend:next.config.ts",
    "frontend:src/app/layout.tsx",
    "frontend:scripts/local-auth/setup.mts",
    "frontend:scripts/local-auth/worker.mts",
    "frontend:scripts/local-auth/lock.mts",
    "frontend:scripts/local-auth/provision.mts",
    "frontend:src/server/auth/config.ts",
    "frontend:src/server/auth/factory.ts",
    "frontend:src/server/auth/state.ts",
    "frontend:src/server/auth/runtime.ts",
    "frontend:tests/helpers/staff-orders-browser-run.mts",
    "frontend:tests/integration/staff-orders-browser.mts",
    "backend:go.mod",
    "backend:go.sum",
    "backend:cmd/local-staff-api/orders_browser_seed_test.go",
    "backend:cmd/local-staff-api/orders_connected_test.go",
  ])
    if (!keys.includes(required)) throw fail();
  await verifySources(manifest);
  const destination = join(root, "source");
  await mkdir(destination, { mode: 0o700 });
  for (const file of manifest.files) {
    const origin =
      file.root === "frontend" ? manifest.frontend_root : manifest.backend_root;
    const target = join(
      destination,
      file.root === "frontend" ? "" : "backend",
      file.path,
    );
    await mkdir(dirname(target), { recursive: true, mode: 0o700 });
    const bytes = await publicBytes(join(origin, file.path));
    if (bytes.length !== file.bytes || digest(bytes) !== file.sha256)
      throw fail();
    await privateWrite(target, bytes);
  }
  const modules = join(manifest.frontend_root, "node_modules");
  // Explicit installed dependency reuse only, never inventory/copy the directory.
  if (!(await lstat(await realpath(modules))).isDirectory()) throw fail();
  await symlink(modules, join(destination, "node_modules"), "dir");
  for (const [name, version] of [
    ["next", "16.4.2"],
    ["@playwright/test", "1.64.1"],
    ["better-auth", "1.7.12"],
  ]) {
    const installed: unknown = JSON.parse(
      await readFile(join(modules, name, "package.json"), "utf8"),
    );
    if (
      z.object({ version: z.literal(version) }).safeParse(installed).success !==
      true
    )
      throw fail();
  }
  const packageJSON: unknown = JSON.parse(
    await readFile(join(destination, "package.json"), "utf8"),
  );
  z.object({
    dependencies: z.object({
      next: z.literal("16.4.2"),
      "better-auth": z.literal("1.7.12"),
    }),
    devDependencies: z.object({ "@playwright/test": z.literal("1.64.1") }),
  }).parse(packageJSON);
  await verifySources(manifest);
  for (const file of manifest.files) {
    const path = join(
      destination,
      file.root === "frontend" ? "" : "backend",
      file.path,
    );
    const bytes = await publicBytes(path);
    if (bytes.length !== file.bytes || digest(bytes) !== file.sha256)
      throw fail();
  }
  return destination;
}

export async function runOrdersBrowser(
  journey: (input: JourneyInput) => Promise<void>,
) {
  let stage: Stage = "arguments";
  let root: string | undefined;
  let env: Record<string, string> = {};
  let source = "";
  let pgAttempted = false;
  let failed = false;
  let seedTimer: NodeJS.Timeout | undefined;
  let authOwner:
    | ReturnType<typeof import("../../src/server/auth/factory.ts").createAuth>
    | undefined;
  const children: OwnedChild[] = [];
  const cancellation = new AbortController();
  const stages: Array<{ stage: Stage; status: string }> = [];
  const receipt = (status: "PASS" | "FAIL" | "STOPPED") => {
    stages.push({ stage, status });
    process.stdout.write(`staff-orders-browser ${stage} ${status}\n`);
  };
  const launch = async (
    name: string,
    binary: string,
    args: string[],
    cwd: string,
    childEnv = env,
  ): Promise<OwnedChild> => {
    if (!root || cancellation.signal.aborted) throw fail();
    const out = await open(join(root, `${name}.stdout`), "wx", 0o600);
    const err = await open(join(root, `${name}.stderr`), "wx", 0o600);
    try {
      const child = spawn(binary, args, {
        cwd,
        env: childEnv,
        detached: true,
        stdio: ["ignore", out.fd, err.fd],
      });
      const closed = new Promise<number>((done) => {
        child.once("error", () => done(1));
        child.once("exit", (code) => done(code ?? 1));
      });
      const owned = { child, closed };
      children.push(owned);
      return owned;
    } finally {
      await out.close();
      await err.close();
    }
  };
  const stop = async (owned: OwnedChild) => {
    if (
      owned.child.exitCode !== null ||
      owned.child.signalCode !== null ||
      !owned.child.pid
    )
      return;
    // Only process groups created by this runner. No scanning or port-owner kills.
    try {
      process.kill(-owned.child.pid, "SIGTERM");
    } catch {
      /* Already gone. */
    }
    let timer: NodeJS.Timeout | undefined;
    try {
      await Promise.race([
        owned.closed,
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(fail()), 5000);
        }),
      ]);
    } catch {
      try {
        process.kill(-owned.child.pid, "SIGKILL");
      } catch {
        /* Already gone. */
      }
      throw fail();
    } finally {
      clearTimeout(timer);
    }
  };
  const run = async (
    name: string,
    binary: string,
    args: string[],
    cwd: string,
    timeout: number,
    childEnv = env,
  ) => {
    if (timeout <= 0) throw fail();
    const owned = await launch(name, binary, args, cwd, childEnv);
    let timer: NodeJS.Timeout | undefined;
    const abort = () => {
      void stop(owned).catch(() => {});
    };
    cancellation.signal.addEventListener("abort", abort, { once: true });
    try {
      const code = await Promise.race([
        owned.closed,
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(fail()), timeout);
        }),
      ]);
      if (code !== 0) throw fail();
    } finally {
      cancellation.signal.removeEventListener("abort", abort);
      clearTimeout(timer);
      await stop(owned);
    }
  };
  const ready = async (url: string, status: number, owned: OwnedChild) => {
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      if (cancellation.signal.aborted) throw fail();
      if (owned.child.exitCode !== null || owned.child.signalCode !== null)
        throw fail();
      try {
        const response = await fetch(url, {
          redirect: "manual",
          signal: AbortSignal.timeout(Math.min(1000, deadline - Date.now())),
        });
        await response.body?.cancel();
        if (response.status === status) return;
        throw fail(); // An unexpected HTTP result is not a retryable startup condition.
      } catch (error) {
        if (error instanceof Error && error.message === "isolated run stopped")
          throw error;
      }
      await new Promise((done) => setTimeout(done, 100));
    }
    throw fail();
  };
  const onSignal = () => {
    cancellation.abort();
  };
  try {
    const args = process.argv.slice(2);
    if (args.length === 1 && ["--help", "-h"].includes(args[0] ?? "")) {
      process.stdout.write(usage);
      return;
    }
    if (
      args.length !== 5 ||
      args[0] !== "--execute" ||
      args[1] !== "--manifest" ||
      args[3] !== "--manifest-sha256" ||
      process.execPath !== NODE ||
      !process.execArgv.includes("--conditions=react-server") ||
      process.version !== "v26.11.1"
    )
      throw fail();
    const manifestPath = absolute.parse(args[2]);
    if (!manifestPath.endsWith(".json")) throw fail();
    const bytes = await publicBytes(manifestPath);
    if (digest(bytes) !== pin.parse(args[4])) throw fail();
    const manifest = manifestSchema.parse(JSON.parse(bytes.toString("utf8")));
    process.umask(0o077);
    root = await mkdtemp("/tmp/restaurant-orders-browser-");
    await nonsymlink(root);
    const stat = await lstat(root);
    if (stat.uid !== process.getuid?.() || (stat.mode & 0o777) !== 0o700)
      throw fail();
    process.env.TMPDIR = root;
    process.env.TMP = root;
    process.env.TEMP = root;
    process.env.PLAYWRIGHT_BROWSERS_PATH = BROWSERS;
    process.env.PLAYWRIGHT_SKIP_BROWSER_GC = "1";
    process.stdout.write(`staff-orders-browser evidence ${root}\n`);
    await privateWrite(join(root, "public-manifest.json"), bytes);
    // Positive environment: unchanged HOME, no inherited PG/default/auth/proxy options.
    const home = process.env.HOME;
    if (!home || !isAbsolute(home)) throw fail();
    env = {
      HOME: home,
      PATH: `${dirname(NODE)}:/usr/bin:/bin`,
      LANG: "C",
      NODE_OPTIONS: "",
      TMPDIR: root,
      TMP: root,
      TEMP: root,
      NODE_ENV: "production",
      PGPASSFILE: "/dev/null",
      PGSERVICEFILE: "/dev/null",
      NEXT_TELEMETRY_DISABLED: "1",
      NO_UPDATE_NOTIFIER: "1",
      GOENV: "off",
      GOTOOLCHAIN: "local",
      GOPROXY: "off",
      GOSUMDB: "off",
      GOTELEMETRY: "off",
      GOWORK: "off",
      GOFLAGS: "-mod=readonly",
      GOMODCACHE: manifest.module_cache,
      GOCACHE: join(root, "go-build"),
      GOPATH: join(root, "gopath"),
      PLAYWRIGHT_BROWSERS_PATH: BROWSERS,
      PLAYWRIGHT_SKIP_BROWSER_GC: "1",
    };
    const toolsEnv = env;
    process.once("SIGINT", onSignal);
    process.once("SIGTERM", onSignal);
    stage = "snapshot";
    source = await snapshot(await bindBackend(manifest, root), root);
    receipt("PASS");
    // Public one-line acknowledgement lets the parent release the owner window
    // on this event BEFORE compilation/runtime, without polling private logs.
    let acknowledgement = "";
    let accept = (_chunk: Buffer) => {};
    const released = new Promise<void>((done, reject) => {
      accept = (chunk: Buffer) => {
        acknowledgement += chunk.toString("utf8");
        if (acknowledgement.length > 64) reject(fail());
        else if (acknowledgement.includes("\n")) {
          if (acknowledgement === "SOURCE_WINDOW_RELEASED\n") done();
          else reject(fail());
        }
      };
      process.stdin.on("data", accept);
    });
    process.stdout.write("SNAPSHOT_VALIDATED\n");
    try {
      await within(released, 30_000, cancellation.signal);
    } finally {
      process.stdin.removeListener("data", accept);
      process.stdin.pause();
    }
    // No original-source reads after this point; only copies/installed reuse.
    stage = "build";
    const next = join(source, "node_modules/next/dist/bin/next");
    await run("next-build", NODE, [next, "build"], source, 300_000);
    receipt("PASS");
    stage = "compile";
    const backend = join(source, "backend");
    const hostBinary = join(root, "local-staff-api");
    const seedBinary = join(root, "orders-browser-seed.test");
    const compileDeadline = Date.now() + 120_000;
    const compileTimer = setTimeout(() => cancellation.abort(), 120_000);
    try {
      await run(
        "host-compile",
        "/usr/bin/go",
        ["build", "-mod=readonly", "-o", hostBinary, "./cmd/local-staff-api"],
        backend,
        compileDeadline - Date.now(),
      );
      await run(
        "seed-compile",
        "/usr/bin/go",
        [
          "test",
          "-c",
          "-mod=readonly",
          "-o",
          seedBinary,
          "./cmd/local-staff-api",
        ],
        backend,
        compileDeadline - Date.now(),
      );
      if (cancellation.signal.aborted) throw fail();
    } finally {
      clearTimeout(compileTimer);
    }
    receipt("PASS");
    stage = "postgres";
    const data = join(root, "pgdata");
    const pgPort = String(manifest.ports.postgres);
    await run(
      "initdb",
      "/usr/bin/initdb",
      [
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
      ],
      source,
      30_000,
    );
    await privateWrite(join(root, "postgres.log"), "");
    pgAttempted = true;
    await run(
      "pg-start",
      "/usr/bin/pg_ctl",
      [
        "-D",
        data,
        "-l",
        join(root, "postgres.log"),
        "-o",
        `-F -p ${pgPort} -h 127.0.0.1 -k '' -c max_connections=20`,
        "-t",
        "30",
        "-w",
        "start",
      ],
      source,
      30_000,
    );
    const database = `restaurant_foundation_${root.split("-").at(-1)}`;
    await run(
      "createdb",
      "/usr/bin/createdb",
      ["-h", "127.0.0.1", "-p", pgPort, "-U", "localtest", database],
      source,
      30_000,
    );
    receipt("PASS");
    const origin = `http://127.0.0.1:${manifest.ports.next}`;
    const apiOrigin = `http://127.0.0.1:${manifest.ports.staff}`;
    const statePath = join(root, "private/setup.json");
    const dsn = `postgres://localtest@127.0.0.1:${pgPort}/${database}?sslmode=disable`;
    env = {
      ...env,
      LOCAL_DATABASE_URL: dsn,
      BETTER_AUTH_URL: origin,
      STAFF_API_URL: apiOrigin,
      LOCAL_DASHBOARD_STATE: statePath,
      STAFF_JWT_ISSUER: origin,
      STAFF_JWKS_URL: `${origin}/api/auth/jwks`,
      STAFF_CATALOG_CURSOR_KEY: randomBytes(32).toString("hex"),
    };
    stage = "setup";
    await run(
      "setup",
      NODE,
      ["--conditions=react-server", "scripts/local-auth/setup.mts"],
      source,
      120_000,
    );
    const { databaseTarget }: typeof import("../../src/server/auth/config.ts") =
      await import(
        pathToFileURL(join(source, "src/server/auth/config.ts")).href
      );
    const { readState }: typeof import("../../src/server/auth/state.ts") =
      await import(
        pathToFileURL(join(source, "src/server/auth/state.ts")).href
      );
    const { createAuth }: typeof import("../../src/server/auth/factory.ts") =
      await import(
        pathToFileURL(join(source, "src/server/auth/factory.ts")).href
      );
    const target = databaseTarget(dsn);
    const state = await readState(statePath, target);
    receipt("PASS");
    stage = "listeners";
    const web = await launch(
      "next",
      NODE,
      [
        next,
        "start",
        "--hostname",
        "127.0.0.1",
        "--port",
        String(manifest.ports.next),
      ],
      source,
    );
    await ready(`${origin}/sign-in`, 200, web);
    const host = await launch(
      "host",
      hostBinary,
      ["-listen", `127.0.0.1:${manifest.ports.staff}`],
      source,
      { ...env, LOCAL_RESTAURANT_ID: state.restaurantID },
    );
    await ready(`${apiOrigin}/v1/staff/access`, 401, host);
    receipt("PASS");
    stage = "seed";
    const seedDeadline = Date.now() + 120_000;
    seedTimer = setTimeout(() => cancellation.abort(), 120_000);
    const seedStep = <T,>(pending: Promise<T>) =>
      within(pending, seedDeadline - Date.now(), cancellation.signal);
    authOwner = createAuth({ target, origin, secret: state.secret });
    const fixture = state.fixtures[0];
    const response = await seedStep(
      authOwner.auth.api.signInEmail({
        headers: new Headers({ origin }),
        body: { email: fixture.email, password: fixture.password },
        asResponse: true,
      }),
    );
    if (!response.ok) throw fail();
    const cookie = response.headers
      .getSetCookie()
      .map((value) => value.split(";")[0])
      .join("; ");
    if (!cookie) throw fail();
    const headers = new Headers({ cookie, origin });
    const session = await seedStep(authOwner.auth.api.getSession({ headers }));
    if (!session || session.user.email !== fixture.email) throw fail();
    let seed: z.infer<typeof seedSchema>;
    try {
      // Mint only after BOTH binaries, schema/setup and listener readiness finish.
      const { token } = await seedStep(
        authOwner.auth.api.getToken({ headers }),
      );
      const input = join(root, "seed-input.json");
      const output = join(root, "seed-output.json");
      await privateWrite(
        input,
        JSON.stringify({
          database_url: dsn,
          restaurant_id: state.restaurantID,
          subject: session.user.id,
          issuer: origin,
          jwks_url: `${origin}/api/auth/jwks`,
          token,
          evidence_dir: root,
        }),
      );
      await run(
        "seed",
        seedBinary,
        [
          "-test.run",
          "^TestOrdersBrowserSeed$",
          "-test.count=1",
          "-test.timeout=120s",
        ],
        backend,
        seedDeadline - Date.now(),
        {
          ...toolsEnv,
          PGPASSFILE: "/dev/null",
          PGSERVICEFILE: "/dev/null",
          STAFF_ORDERS_BROWSER_SEED_INPUT: input,
          STAFF_ORDERS_BROWSER_SEED_OUTPUT: output,
        },
      );
      seed = seedSchema.parse(await privateJSON(output));
    } finally {
      await seedStep(authOwner.auth.api.signOut({ headers }));
    }
    clearTimeout(seedTimer);
    if (cancellation.signal.aborted) throw fail();
    receipt("PASS");
    stage = "journey";
    // Abort closes the owned browser, then await its finally BEFORE stopping
    // listeners. No abandoned Promise.race journey continues during teardown.
    const timer = setTimeout(() => cancellation.abort(), 60_000);
    try {
      await journey({
        origin,
        fixture,
        seed,
        env: toolsEnv,
        signal: cancellation.signal,
      });
      if (cancellation.signal.aborted) throw fail();
      receipt("PASS");
    } finally {
      clearTimeout(timer);
    }
  } catch {
    failed = true;
    receipt("FAIL");
  } finally {
    clearTimeout(seedTimer);
    if (root) {
      stage = "shutdown";
      // The ownership-only PG shutdown is independent of work cancellation.
      // Browser finishes/closes first, then Next, host, auth pool and owned PG.
      for (const owned of children.filter(
        (entry) =>
          entry.child.exitCode === null && entry.child.signalCode === null,
      )) {
        try {
          await stop(owned);
        } catch {
          failed = true;
        }
      }
      try {
        if (authOwner) await within(authOwner.pool.end(), 5000);
      } catch {
        failed = true;
      }
      if (pgAttempted) {
        try {
          await runShutdownPG();
        } catch {
          failed = true;
        }
      }
      receipt(failed ? "FAIL" : "PASS");
      await privateWrite(
        join(root, "outcome.json"),
        JSON.stringify({ status: failed ? "FAIL" : "PASS", stages }),
      ).catch(() => {
        failed = true;
      });
      // No deletion: failed and successful run data/logs remain protected.
    }
    process.removeListener("SIGINT", onSignal);
    process.removeListener("SIGTERM", onSignal);
    if (failed) process.exitCode = 1;
  }

  async function runShutdownPG() {
    // Separate ownership-only shutdown launch; work cancellation must not skip it.
    if (!root) throw fail();
    const out = await open(join(root, "pg-stop.stdout"), "wx", 0o600);
    const err = await open(join(root, "pg-stop.stderr"), "wx", 0o600);
    try {
      await new Promise<void>((done, reject) => {
        const child = spawn(
          "/usr/bin/pg_ctl",
          [
            "-D",
            join(root, "pgdata"),
            "-m",
            "immediate",
            "-t",
            "10",
            "-w",
            "stop",
          ],
          { cwd: source, env, stdio: ["ignore", out.fd, err.fd] },
        );
        const timer = setTimeout(() => {
          child.kill("SIGKILL");
          reject(fail());
        }, 15_000);
        child.once("error", () => {
          clearTimeout(timer);
          reject(fail());
        });
        child.once("exit", (code) => {
          clearTimeout(timer);
          code === 0 ? done() : reject(fail());
        });
      });
    } finally {
      await out.close();
      await err.close();
    }
  }
}
