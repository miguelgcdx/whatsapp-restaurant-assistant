import assert from "node:assert/strict";
import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  type Browser,
  type BrowserContext,
  chromium,
  expect,
} from "@playwright/test";
import { z } from "zod";
import { catalogSchema } from "../../src/contracts/staff-catalog.ts";
import { databaseTarget } from "../../src/server/auth/config.ts";
import { createAuth } from "../../src/server/auth/factory.ts";
import { readState } from "../../src/server/auth/state.ts";
import { startAuthPostgres } from "../helpers/auth-postgres.mts";

// Real owned Next + Go + PostgreSQL only. No fake providers, auth tables or inherited DSNs.
const root = fileURLToPath(new URL("../../", import.meta.url));
const temporary = await mkdtemp(join(tmpdir(), "staff-dashboard-e2e-"));
const pg = await startAuthPostgres();
const port = async () => {
  const listener = createServer();
  await new Promise<void>((done, fail) => {
    listener.once("error", fail);
    listener.listen(0, "127.0.0.1", done);
  });
  const address = listener.address();
  assert(address && typeof address !== "string");
  await new Promise<void>((done, fail) =>
    listener.close((error) => (error ? fail(error) : done())),
  );
  return address.port;
};
const origin = `http://127.0.0.1:${await port()}`;
const apiOrigin = `http://127.0.0.1:${await port()}`;
const statePath = join(temporary, "private", "setup.json");
const cursorKey = randomBytes(32).toString("hex");
const env: NodeJS.ProcessEnv = {
  NODE_ENV: "development",
  PATH: process.env.PATH,
  HOME: process.env.HOME,
  LANG: "C",
  TMPDIR: process.env.TMPDIR,
  NO_UPDATE_NOTIFIER: "1",
  NEXT_TELEMETRY_DISABLED: "1",
  GOTOOLCHAIN: "local",
  GOTELEMETRY: "off",
  GOPROXY: "off",
  GOFLAGS: "-mod=readonly",
  GOCACHE: resolve(root, "backend/.cache/build"),
  GOPATH: resolve(root, "backend/.cache/gopath"),
  GOMODCACHE: resolve(root, "backend/.cache/modules"),
  LOCAL_DATABASE_URL: pg.dsn,
  LOCAL_DASHBOARD_STATE: statePath,
  BETTER_AUTH_URL: origin,
  STAFF_API_URL: apiOrigin,
  STAFF_JWT_ISSUER: origin,
  STAFF_JWKS_URL: `${origin}/api/auth/jwks`,
  STAFF_CATALOG_CURSOR_KEY: cursorKey,
  PLAYWRIGHT_BROWSERS_PATH: resolve(root, "backend/.cache/playwright"),
  DASHBOARD_E2E_ORIGIN: origin,
};
const secrets = [pg.dsn, cursorKey];
const redact = (value: string) =>
  secrets.reduce(
    (text, secret) => text.replaceAll(secret, "[redacted]"),
    value,
  );
let stage = "setup";
let browser: Browser | undefined;
let owner: ReturnType<typeof createAuth> | undefined;
let pgClosed = false;
const children: Array<{
  child: ChildProcess;
  closed: Promise<void>;
  logs: () => string;
}> = [];
const run = (binary: string, args: string[], cwd = root) =>
  new Promise<{ code: number; stdout: string; stderr: string }>((done) => {
    execFile(
      binary,
      args,
      { cwd, env, timeout: 30000, maxBuffer: 1024 * 1024 },
      (error, stdout, stderr) =>
        done({
          code: error ? (typeof error.code === "number" ? error.code : -1) : 0,
          stdout,
          stderr,
        }),
    );
  });
const succeeded = async (binary: string, args: string[], cwd = root) => {
  const result = await run(binary, args, cwd);
  if (result.code !== 0)
    throw new Error(
      redact(`Child exit ${result.code}: ${result.stdout}\n${result.stderr}`),
    );
  return result;
};
const start = (
  binary: string,
  args: string[],
  extra: Partial<NodeJS.ProcessEnv> = {},
) => {
  const child = spawn(binary, args, {
    cwd: root,
    env: { ...env, ...extra },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let tail = "";
  child.stdout?.on("data", (data: Buffer) => {
    tail = (tail + data.toString()).slice(-16384);
  });
  child.stderr?.on("data", (data: Buffer) => {
    tail = (tail + data.toString()).slice(-16384);
  });
  const closed = new Promise<void>((done) => {
    child.once("exit", () => done());
    child.once("error", () => done());
  });
  const entry = { child, closed, logs: () => redact(tail) };
  children.push(entry);
  return entry;
};
const stop = async (entry: (typeof children)[number]) => {
  if (entry.child.exitCode !== null || entry.child.signalCode !== null) return;
  if (entry.child.pid) {
    try {
      process.kill(-entry.child.pid, "SIGTERM");
    } catch {
      /* Already exited. */
    }
  }
  let timer: NodeJS.Timeout | undefined;
  try {
    await Promise.race([
      entry.closed,
      new Promise<never>((_, fail) => {
        timer = setTimeout(
          () => fail(new Error("Owned process shutdown timed out.")),
          10000,
        );
      }),
    ]);
  } catch (error) {
    if (entry.child.pid) {
      try {
        process.kill(-entry.child.pid, "SIGKILL");
      } catch {
        /* Already exited. */
      }
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
};
const ready = async (url: string, status: number) => {
  await expect
    .poll(
      async () => {
        try {
          return (
            await fetch(url, {
              signal: AbortSignal.timeout(5000),
              redirect: "manual",
            })
          ).status;
        } catch {
          return 0;
        }
      },
      { timeout: 60000, message: "Owned listener readiness" },
    )
    .toBe(status);
};
const node = process.execPath;
const setup = () =>
  run(node, ["--conditions=react-server", "scripts/local-auth/setup.mts"]);
const next = () =>
  start(node, [
    "node_modules/next/dist/bin/next",
    "dev",
    "--hostname",
    "127.0.0.1",
    "--port",
    new URL(origin).port,
  ]);
const apiBinary = join(temporary, "local-staff-api");
const membershipBinary = join(temporary, "local-staff-membership");
const go = (restaurant: string) =>
  start(apiBinary, ["-listen", new URL(apiOrigin).host], {
    LOCAL_RESTAURANT_ID: restaurant,
  });
const cookieHeaders = async (context: BrowserContext) => {
  const cookies = await context.cookies();
  for (const cookie of cookies) secrets.push(cookie.value);
  return new Headers({
    cookie: cookies
      .map((cookie) => `${cookie.name}=${cookie.value}`)
      .join("; "),
    origin,
  });
};
const access = (
  context: BrowserContext,
  headers: Record<string, string> = {},
) => context.request.get(`${origin}/api/staff/access`, { headers });

try {
  const first = await setup();
  assert.equal(first.code, 0, redact(first.stderr));
  assert.equal(first.stderr, "");
  const state = await readState(statePath, databaseTarget(pg.dsn));
  secrets.push(
    state.secret,
    ...state.fixtures.map((fixture) => fixture.password),
  );
  const stateHash = async () =>
    createHash("sha256")
      .update(await readFile(statePath))
      .digest("hex");
  const before = await stateHash();
  for (const result of await Promise.all([setup(), setup()])) {
    assert.equal(result.code, 0, redact(result.stderr));
    assert.equal(result.stderr, "");
    const summary = z
      .strictObject({
        complete: z.literal(true),
        restaurant_id: z.uuid(),
        state_path: z.string(),
      })
      .parse(JSON.parse(result.stdout));
    assert.equal(summary.restaurant_id, state.restaurantID);
    assert.equal(summary.state_path, statePath);
  }
  assert.equal((await stateHash()) === before, true);
  for (const secret of secrets)
    assert.equal(first.stdout.includes(secret), false);
  console.log(
    "PASS real Go/Better Auth setup and concurrent credential-preserving reruns",
  );

  stage = "listeners and anonymous browser routes";
  await succeeded(
    "go",
    ["build", "-o", apiBinary, "./cmd/local-staff-api"],
    resolve(root, "backend"),
  );
  await succeeded(
    "go",
    ["build", "-o", membershipBinary, "./cmd/local-staff-membership"],
    resolve(root, "backend"),
  );
  stage = "explicit fictional catalog fixture";
  const fixtureBinary = join(temporary, "browser-catalog-fixture");
  await succeeded(
    "go",
    [
      "build",
      "-o",
      fixtureBinary,
      "./internal/catalog/testdata/browser-fixture",
    ],
    resolve(root, "backend"),
  );
  const fixture = await new Promise<{ code: number; stderr: string }>(
    (done) => {
      execFile(
        fixtureBinary,
        [],
        {
          env: { ...env, LOCAL_RESTAURANT_ID: state.restaurantID },
          timeout: 15000,
        },
        (error, _stdout, stderr) => done({ code: error ? 1 : 0, stderr }),
      );
    },
  );
  assert.equal(fixture.code, 0, redact(fixture.stderr));
  assert.equal(fixture.stderr, "");
  let api = go(state.restaurantID);
  let web = next();
  await ready(`${apiOrigin}/v1/staff/access`, 401);
  await ready(`${origin}/sign-in`, 200);
  const anonymous = await succeeded(node, [
    "node_modules/@playwright/test/cli.js",
    "test",
  ]);
  process.stdout.write(anonymous.stdout);
  process.stderr.write(anonymous.stderr);
  browser = await chromium.launch({ headless: true, env });
  owner = createAuth({
    target: databaseTarget(pg.dsn),
    origin,
    secret: state.secret,
  });
  const contexts = await Promise.all([
    browser.newContext(),
    browser.newContext(),
    browser.newContext(),
  ]);
  const [alex, sam, alexOther] = contexts;
  assert(alex && sam && alexOther);
  const pages = [];
  for (const [context, index] of [
    [alex, 0],
    [sam, 1],
    [alexOther, 0],
  ] as const) {
    const fixture = state.fixtures[index];
    const page = await context.newPage();
    pages.push(page);
    await page.goto(`${origin}/sign-in`);
    await page.getByLabel("Email", { exact: true }).fill(fixture.email);
    await page.getByLabel("Password", { exact: true }).fill(fixture.password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(`${origin}/dashboard`);
    await expect(
      page.getByRole("heading", { name: "Staff dashboard" }),
    ).toBeVisible();
    await expect(
      page.getByText(`Signed in as ${fixture.name}.`, { exact: true }),
    ).toBeVisible();
    const response = await access(context, {
      Authorization: "Bearer forged",
      "X-Role": "admin",
      "X-Restaurant-ID": randomUUID(),
    });
    assert.equal(response.status(), 200);
    assert.deepEqual(await response.json(), {
      restaurant_id: state.restaurantID,
    });
    assert.equal(response.headers()["set-auth-jwt"], undefined);
    assert.deepEqual(await page.evaluate(() => Object.keys(localStorage)), []);
    assert(
      (await context.cookies()).some(
        (cookie) => cookie.name.endsWith("session_token") && cookie.httpOnly,
      ),
    );
  }
  console.log(
    "PASS real server-action cookie propagation, two staff and separate same-user sessions, JWT/JWKS→Go membership",
  );

  stage = "real catalog navigation and exact fictional prices";
  const catalogPage = pages[2];
  assert(catalogPage);
  await catalogPage
    .getByRole("link", { name: "View catalog", exact: true })
    .click();
  await expect(
    catalogPage.getByRole("heading", { name: "Staff catalog", exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("Restaurant is open.", { exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("USD 6.50", { exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("USD 4.50", { exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("USD 2.00", { exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("Unavailable", { exact: true }),
  ).toBeVisible();
  await catalogPage.goto(`${origin}/dashboard/catalog?limit=1`);
  await expect(
    catalogPage.getByRole("heading", { name: "Fictional bowl", exact: true }),
  ).toBeVisible();
  const continuation = await catalogPage
    .getByRole("link", { name: "Next page", exact: true })
    .getAttribute("href");
  assert(continuation);
  const catalogBefore = await alexOther.request.get(
    `${origin}/api/staff/catalog`,
  );
  assert.equal(catalogBefore.status(), 200);
  let baseline = catalogSchema.parse(await catalogBefore.json()).data;
  await catalogPage
    .getByRole("link", { name: "Next page", exact: true })
    .click();
  await expect(
    catalogPage.getByRole("heading", { name: "Fictional soup", exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByRole("heading", { name: "Fictional bowl", exact: true }),
  ).toHaveCount(0);
  await catalogPage
    .getByRole("link", { name: "Next page", exact: true })
    .click();
  await expect(
    catalogPage.getByRole("heading", { name: "Fictional tea", exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByText("End of menu.", { exact: true }),
  ).toBeVisible();
  await expect(
    catalogPage.getByRole("link", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  await catalogPage
    .getByRole("link", { name: "Start again", exact: true })
    .click();
  await expect(
    catalogPage.getByText(
      "Showing 3 items on this page. Availability may change between pages.",
      { exact: true },
    ),
  ).toBeVisible();
  assert.deepEqual(
    await catalogPage.evaluate(() => Object.keys(localStorage)),
    [],
  );
  const catalogAPI = `${origin}/api/staff/catalog`;
  for (const suffix of [
    "?restaurant_id=forged",
    "?limit=1&limit=2",
    "?cursor=invalid",
  ])
    assert.equal(
      (await alexOther.request.get(`${catalogAPI}${suffix}`)).status(),
      400,
    );
  assert.equal(
    (
      await alexOther.request.get(catalogAPI, {
        headers: { origin: "http://foreign.invalid" },
      })
    ).status(),
    403,
  );
  assert.equal((await alexOther.request.post(catalogAPI)).status(), 405);
  assert.deepEqual(
    catalogSchema.parse(await (await alexOther.request.get(catalogAPI)).json())
      .data,
    baseline,
  );
  console.log(
    "PASS real catalog, exact prices, current-page traversal/start-again and unchanged rejected reads",
  );

  stage = "availability controls, conflict and uncertain commit recovery";
  // Register owned session-cookie values for failure redaction before transport injection.
  for (const context of contexts) await cookieHeaders(context);
  const readCatalog = async () => {
    const response = await alexOther.request.get(catalogAPI);
    assert.equal(response.status(), 200);
    return catalogSchema.parse(await response.json()).data;
  };
  const itemID = baseline.items[0]?.id;
  assert(itemID);
  const itemWriteURL = `${catalogAPI}/items/${itemID}/availability`;
  const restaurantWriteURL = `${origin}/api/staff/restaurant/availability`;
  const write = (
    context: BrowserContext,
    url: string,
    key: string,
    body: string,
  ) =>
    context.request.patch(url, {
      headers: {
        origin,
        "content-type": "application/json",
        "idempotency-key": key,
      },
      data: body,
    });
  const bowl = catalogPage.getByRole("listitem").filter({
    has: catalogPage.getByRole("heading", {
      name: "Fictional bowl",
      exact: true,
    }),
  });
  const restaurantControl = catalogPage.getByRole("region", {
    name: "Restaurant availability control",
    exact: true,
  });
  await bowl
    .getByRole("button", { name: "Mark unavailable", exact: true })
    .click();
  await expect(
    bowl.getByRole("button", { name: "Mark available", exact: true }),
  ).toBeEnabled();
  await expect(bowl.getByText("Unavailable", { exact: true })).toBeVisible();
  await restaurantControl
    .getByRole("button", { name: "Close restaurant", exact: true })
    .click();
  await expect(
    catalogPage.getByText("Restaurant is closed.", { exact: true }),
  ).toBeVisible();
  await expect(
    restaurantControl.getByRole("button", {
      name: "Open restaurant",
      exact: true,
    }),
  ).toBeEnabled();
  let current = await readCatalog();
  assert.equal(
    current.items[0]?.version,
    (BigInt(baseline.items[0]?.version ?? "0") + BigInt(1)).toString(),
  );
  assert.equal(
    current.restaurant.version,
    (BigInt(baseline.restaurant.version) + BigInt(1)).toString(),
  );
  assert.deepEqual(
    current.items.map((item) => ({
      id: item.id,
      name: item.name,
      price: item.price,
    })),
    baseline.items.map((item) => ({
      id: item.id,
      name: item.name,
      price: item.price,
    })),
  );

  // Real BFF -> Go commit, then lose only its browser response once.
  const beforeUnknown = current.items[0];
  assert(beforeUnknown);
  let lost: { key: string; body: string } | undefined;
  let lostAttempts = 0;
  await catalogPage.route(itemWriteURL, async (route) => {
    lostAttempts++;
    const request = route.request();
    const key = request.headers()["idempotency-key"];
    const body = request.postData();
    assert(key && body);
    lost = { key, body };
    const committed = await route.fetch({ maxRetries: 0, maxRedirects: 0 });
    assert.equal(committed.status(), 200);
    await route.abort("failed");
  });
  await bowl
    .getByRole("button", { name: "Mark available", exact: true })
    .click();
  await expect(bowl.getByRole("status")).toHaveText(
    "The result is uncertain. Retry the same request; do not start a new change.",
  );
  assert(lost);
  assert.equal(
    (await readCatalog()).items[0]?.version,
    (BigInt(beforeUnknown.version) + BigInt(1)).toString(),
  );
  // A sibling authoritative refresh must not discard or rewrite this intent.
  await restaurantControl
    .getByRole("button", { name: "Open restaurant", exact: true })
    .click();
  await expect(
    catalogPage.getByText("Restaurant is open.", { exact: true }),
  ).toBeVisible();
  await expect(
    bowl.getByRole("button", { name: "Retry same request", exact: true }),
  ).toBeVisible();
  assert.equal(lostAttempts, 1);
  await catalogPage.unroute(itemWriteURL);
  const retriedRequest = catalogPage.waitForRequest(
    (request) => request.url() === itemWriteURL && request.method() === "PATCH",
  );
  await bowl
    .getByRole("button", { name: "Retry same request", exact: true })
    .click();
  const retry = await retriedRequest;
  assert.equal(retry.headers()["idempotency-key"], lost.key);
  assert.equal(retry.postData(), lost.body);
  await expect(
    bowl.getByRole("button", { name: "Mark unavailable", exact: true }),
  ).toBeEnabled();
  current = await readCatalog();
  assert.equal(
    current.items[0]?.version,
    (BigInt(beforeUnknown.version) + BigInt(1)).toString(),
  );
  assert.equal(current.items[0]?.available, true);

  // A stale rendered control must conflict, not overwrite the public API winner.
  const stale = current.items[0];
  assert(stale);
  const staleWinner = await write(
    sam,
    itemWriteURL,
    randomUUID(),
    JSON.stringify({ expected_version: stale.version, available: false }),
  );
  assert.equal(staleWinner.status(), 200);
  const afterWinner = await readCatalog();
  const conflictRequest = catalogPage.waitForRequest(
    (request) => request.url() === itemWriteURL && request.method() === "PATCH",
  );
  await bowl
    .getByRole("button", { name: "Mark unavailable", exact: true })
    .click();
  await expect(bowl.getByRole("status")).toHaveText(
    "Availability changed or this key was already used. Review the refreshed availability before a new change.",
  );
  assert.deepEqual(await readCatalog(), afterWinner);
  const staleRequest = await conflictRequest;
  const review = bowl.getByRole("button", {
    name: "Review refreshed availability",
    exact: true,
  });
  await expect(review).toBeEnabled();
  await review.click();
  await expect(
    bowl.getByRole("button", { name: "Mark available", exact: true }),
  ).toBeEnabled();
  const newRequest = catalogPage.waitForRequest(
    (request) => request.url() === itemWriteURL && request.method() === "PATCH",
  );
  await bowl
    .getByRole("button", { name: "Mark available", exact: true })
    .click();
  const deliberate = await newRequest;
  assert.notEqual(
    deliberate.headers()["idempotency-key"],
    staleRequest.headers()["idempotency-key"],
  );
  assert.deepEqual(JSON.parse(deliberate.postData() ?? "null"), {
    expected_version: afterWinner.items[0]?.version,
    available: true,
  });
  await expect(
    bowl.getByRole("button", { name: "Mark unavailable", exact: true }),
  ).toBeEnabled();

  stage = "public BFF duplicate and competing intents";
  current = await readCatalog();
  const duplicateVersion = current.restaurant.version;
  const duplicateBody = JSON.stringify({
    expected_version: duplicateVersion,
    open: true,
  });
  const duplicateKey = randomUUID();
  const duplicates = await Promise.all([
    write(alexOther, restaurantWriteURL, duplicateKey, duplicateBody),
    write(alexOther, restaurantWriteURL, duplicateKey, duplicateBody),
  ]);
  assert.deepEqual(
    duplicates.map((response) => response.status()),
    [200, 200],
  );
  const duplicatePayloads = await Promise.all(
    duplicates.map((response) => response.text()),
  );
  assert.equal(duplicatePayloads[0], duplicatePayloads[1]);
  const afterDuplicate = await readCatalog();
  assert.equal(
    afterDuplicate.restaurant.version,
    (BigInt(duplicateVersion) + BigInt(1)).toString(),
  );
  assert.equal(
    (
      await write(
        alexOther,
        restaurantWriteURL,
        duplicateKey,
        JSON.stringify({ expected_version: duplicateVersion, open: false }),
      )
    ).status(),
    409,
  );
  assert.deepEqual(await readCatalog(), afterDuplicate);
  const raceBody = JSON.stringify({
    expected_version: afterDuplicate.restaurant.version,
    open: false,
  });
  const race = await Promise.all([
    write(alexOther, restaurantWriteURL, randomUUID(), raceBody),
    write(alexOther, restaurantWriteURL, randomUUID(), raceBody),
  ]);
  assert.deepEqual(
    race.map((response) => response.status()).sort(),
    [200, 409],
  );
  current = await readCatalog();
  assert.equal(
    current.restaurant.version,
    (BigInt(afterDuplicate.restaurant.version) + BigInt(1)).toString(),
  );
  assert.equal(current.restaurant.open, false);
  const historicalReplay = await write(
    alexOther,
    restaurantWriteURL,
    duplicateKey,
    duplicateBody,
  );
  assert.equal(historicalReplay.status(), 200);
  assert.equal(await historicalReplay.text(), duplicatePayloads[0]);
  assert.deepEqual(await readCatalog(), current);
  // Authorize before replay is checked below after revoking this same subject.
  const samReplayKey = randomUUID();
  const samReplayBody = JSON.stringify({
    expected_version: current.restaurant.version,
    open: false,
  });
  assert.equal(
    (
      await write(sam, restaurantWriteURL, samReplayKey, samReplayBody)
    ).status(),
    200,
  );
  baseline = await readCatalog(); // Only legitimate accepted writes advance the preserved snapshot.
  const invalidOrigins: Record<string, string>[] = [
    {},
    { origin: "http://foreign.invalid" },
  ];
  for (const headers of invalidOrigins) {
    assert.equal(
      (
        await alexOther.request.patch(restaurantWriteURL, {
          headers: {
            ...headers,
            "content-type": "application/json",
            "idempotency-key": randomUUID(),
          },
          data: JSON.stringify({
            expected_version: baseline.restaurant.version,
            open: true,
          }),
        })
      ).status(),
      403,
    );
  }
  assert.deepEqual(await readCatalog(), baseline);
  assert.deepEqual(
    await catalogPage.evaluate(() => Object.keys(localStorage)),
    [],
  );
  console.log(
    "PASS real item/open controls, authoritative refresh, stale conflict, lost-after-commit explicit same-intent retry, duplicate convergence and competing versions",
  );

  stage = "origin and framing";
  assert.equal(
    (await access(alex, { origin: "https://foreign.invalid" })).status(),
    403,
  );
  const framed = await alex.request.get(`${origin}/api/staff/access`, {
    headers: { "content-length": "1" },
    data: "x",
  });
  assert.equal(framed.status(), 400);
  assert.deepEqual(await framed.json(), {
    error: { code: "invalid_input", message: "Invalid request." },
  });
  assert.equal(
    (
      await alex.request.get(
        `${origin}/api/staff/access?restaurant_id=${randomUUID()}`,
      )
    ).status(),
    400,
  );

  stage = "membership revocation and incomplete setup preservation";
  const samHeaders = await cookieHeaders(sam);
  const samSession = await owner.auth.api.getSession({ headers: samHeaders });
  assert(samSession);
  await succeeded(membershipBinary, [
    "-restaurant-id",
    state.restaurantID,
    "-auth-user-id",
    samSession.user.id,
    "-action",
    "revoke",
  ]);
  assert.equal((await access(sam)).status(), 403);
  assert.equal((await access(alexOther)).status(), 200);
  for (const [key, body] of [
    [samReplayKey, samReplayBody],
    [
      randomUUID(),
      JSON.stringify({
        expected_version: baseline.restaurant.version,
        open: true,
      }),
    ],
  ]) {
    const rejected = await write(sam, restaurantWriteURL, key, body);
    assert.equal(rejected.status(), 403);
    assert.equal(rejected.headers()["cache-control"], "no-store");
    const denied = z
      .strictObject({
        error: z.strictObject({
          code: z.literal("forbidden"),
          message: z.literal("Staff access is not available."),
        }),
        request_id: z.string().regex(/^[a-f0-9]{32}$/),
      })
      .parse(await rejected.json());
    assert.equal(denied.error.code, "forbidden");
  }
  const revokedItem = await write(
    sam,
    itemWriteURL,
    randomUUID(),
    JSON.stringify({
      expected_version: baseline.items[0]?.version,
      available: false,
    }),
  );
  assert.equal(revokedItem.status(), 403);
  assert.deepEqual(await readCatalog(), baseline);
  const revokedCatalog = await sam.request.get(
    `${origin}/api/staff/catalog${continuation.slice(continuation.indexOf("?"))}`,
  );
  assert.equal(revokedCatalog.status(), 403);
  const samPage = pages[1];
  assert(samPage);
  await samPage.goto(`${origin}${continuation}`);
  await expect(samPage).toHaveURL(`${origin}/access-denied`);
  await expect(
    samPage.getByText("Fictional soup", { exact: true }),
  ).toHaveCount(0);
  assert.deepEqual(
    catalogSchema.parse(await (await alexOther.request.get(catalogAPI)).json())
      .data,
    baseline,
  );
  const incomplete = await setup();
  assert.equal(incomplete.code, 1);
  assert.equal(incomplete.stdout, "");
  assert.equal(incomplete.stderr, "Local staff setup is incomplete.\n");
  assert.equal((await stateHash()) === before, true);
  assert.equal((await access(sam)).status(), 403);
  assert.equal((await access(alexOther)).status(), 200);
  console.log(
    "PASS current-membership revocation and real incomplete-setup no-reactivation/no-password-reset",
  );

  stage = "logout";
  const alexHeaders = await cookieHeaders(alex);
  const issued = (await owner.auth.api.getToken({ headers: alexHeaders }))
    .token;
  secrets.push(issued);
  const part = issued.split(".")[1];
  assert(part);
  const payload = z
    .strictObject({
      iat: z.number().int(),
      exp: z.number().int(),
      sub: z.string().min(1),
      aud: z.literal("restaurant-assistant-api"),
      iss: z.literal(origin),
    })
    .parse(JSON.parse(Buffer.from(part, "base64url").toString()));
  assert.equal(payload.exp - payload.iat, 60);
  const page = pages[0];
  assert(page);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(`${origin}/sign-in`);
  assert.equal((await access(alex)).status(), 401);
  assert.equal(
    (await alex.request.get(`${catalogAPI}?limit=1&cursor=opaque`)).status(),
    401,
  );
  assert.equal(await owner.auth.api.getSession({ headers: alexHeaders }), null);
  assert.equal((await access(alexOther)).status(), 200);
  assert(await owner.auth.api.getSession({ headers: samHeaders }));
  assert.equal(
    (
      await fetch(`${apiOrigin}/v1/staff/access`, {
        headers: { Authorization: `Bearer ${issued}` },
      })
    ).status,
    200,
  );
  console.log(
    "PASS actual logout/session isolation; issued JWT declares 60-second lifetime (no elapsed-expiry or immediate-revocation claim)",
  );

  stage = "Go outage and absent membership";
  await stop(api);
  assert.equal((await access(alexOther)).status(), 503);
  assert.equal((await alexOther.request.get(catalogAPI)).status(), 503);
  assert.equal(
    (
      await write(
        alexOther,
        restaurantWriteURL,
        randomUUID(),
        JSON.stringify({
          expected_version: baseline.restaurant.version,
          open: true,
        }),
      )
    ).status(),
    503,
  );
  await catalogPage.goto(`${origin}/dashboard/catalog`);
  await expect(catalogPage.getByRole("main").getByRole("alert")).toHaveText(
    "Catalog is temporarily unavailable. Try again later.",
  );
  await expect(
    catalogPage.getByText("Fictional bowl", { exact: true }),
  ).toHaveCount(0);
  api = go(randomUUID());
  await ready(`${apiOrigin}/v1/staff/access`, 401);
  assert.equal((await access(alexOther)).status(), 403);
  await stop(api);
  api = go(state.restaurantID);
  await ready(`${apiOrigin}/v1/staff/access`, 401);
  assert.equal((await access(alexOther)).status(), 200);
  assert.deepEqual(await readCatalog(), baseline);

  stage = "JWKS outage and recovery";
  const headers = await cookieHeaders(alexOther);
  const fresh = (await owner.auth.api.getToken({ headers })).token;
  secrets.push(fresh);
  await stop(web);
  await stop(api);
  api = go(state.restaurantID);
  await ready(`${apiOrigin}/v1/staff/access`, 401);
  const direct = () =>
    fetch(`${apiOrigin}/v1/staff/access`, {
      headers: { Authorization: `Bearer ${fresh}` },
    });
  assert.equal((await direct()).status, 503);
  web = next();
  await ready(`${origin}/sign-in`, 200);
  await expect
    .poll(async () => (await direct()).status, { timeout: 15000 })
    .toBe(200);
  assert.equal((await access(alexOther)).status(), 200);
  console.log(
    "PASS Go outage, absent membership, and real cold-JWKS outage/recovery",
  );

  stage = "database outage";
  pgClosed = true;
  await pg.close();
  assert.equal((await access(alexOther)).status(), 503);
  assert.equal((await alexOther.request.get(catalogAPI)).status(), 503);
  assert.equal(
    (
      await write(
        alexOther,
        restaurantWriteURL,
        randomUUID(),
        JSON.stringify({
          expected_version: baseline.restaurant.version,
          open: true,
        }),
      )
    ).status(),
    503,
  );
  console.log(
    "PASS authoritative-session database outage fails closed for access, catalog and availability",
  );
  console.log(
    "PASS owned dashboard integration; no screenshots/traces/storageState or credentials emitted",
  );
} catch (error) {
  console.error(`FAIL dashboard integration stage: ${stage}`);
  console.error(
    redact(
      error instanceof Error ? error.message : "Unknown integration error.",
    ),
  );
  for (const child of children) if (child.logs()) console.error(child.logs());
  process.exitCode = 1;
} finally {
  await browser?.close();
  for (const child of [...children].reverse()) await stop(child);
  await owner?.pool.end();
  if (!pgClosed) await pg.close();
  await rm(temporary, { recursive: true, force: true });
}
