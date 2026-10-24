# Local staff access foundation

This unit provides authentication and a protected access shell, **not orders**.
The additive [read-only catalog dashboard](local-catalog.md) reuses these
boundaries; its new candidate verification is separate from the historical
foundation evidence below.
Better Auth owns `auth`; Go owns `app` restaurant identity/current membership.
No public registration, real accounts, email providers or external services are used.

## Explicit local setup

Prerequisites: installed pinned dependencies, Node 26, PostgreSQL, Go and Linux
`flock`. Use an explicitly dedicated database named `restaurant_foundation_*`.
Never point this setup at an existing real-user database. Runtime does not migrate
or provision anything.

Set these explicit values in your own process (do not commit credentials):

- `LOCAL_DATABASE_URL`: PostgreSQL URI with explicit numeric-loopback host, port,
  database and user; only the optional `sslmode=disable` query is accepted.
- `BETTER_AUTH_URL`: exact HTTP numeric-loopback origin with explicit port, no
  trailing slash, matching the Next listener.
- `LOCAL_DASHBOARD_STATE`: optional absolute private state path; default
  `backend/.cache/dashboard/setup.json` relative to the project root.
- `STAFF_API_URL`: runtime-only exact Go HTTP numeric-loopback origin.

From the project root, `pnpm setup:auth` invokes
`node --conditions=react-server scripts/local-auth/setup.mts`.
Setup takes a crash-released OS lock, durably writes
private generated credentials first, runs explicit Go app migrations, then the
pinned Better Auth 1.7.12 public library migration planner/executor. This is the
same planner used by `auth migrate`; no runtime or browser migration endpoint
exists. Auth migration is separate from Goose bookkeeping.

Use `pnpm setup:auth --help` or `pnpm setup:auth -h` for usage, required
configuration and private-state handling. Help exits0 with usage on stdout and
empty stderr, without configuration validation, state writes, database access or
provisioning. Missing configuration, unsupported arguments and help combined
with extra arguments retain the redacted nonzero setup failure.

The non-HTTP setup instance uses supported `signUpEmail`. Only the actual 422
`USER_ALREADY_EXISTS_USE_ANOTHER_EMAIL` response permits recovery through
`signInEmail` with the already-stored password. No arbitrary 400 recovery,
password resets, private table reads or revocation reversal are performed.
Setup-created sessions are signed out before Go `ensure`. Both fixtures must be
active before a complete summary is printed. Cross-owner failure is incomplete:
rerun the same command with unchanged state/target to recover. A revoked fixture
remains revoked and setup fails rather than reactivating it.

The state directory/file must be same-user nonsymlink 0700/0600; existing unsafe
state or a changed database identity is rejected. The generated Alex/Sam emails
are `alex.staff@example.test` and `sam.staff@example.test`. The private file is
where the operator reads their generated passwords, auth secret and restaurant
UUID. Never paste its contents into chat, logs, screenshots or test artifacts.

Configure Go from the nonsecret restaurant UUID and exact auth origin:
`LOCAL_RESTAURANT_ID`, `STAFF_JWT_ISSUER`, and `STAFF_JWKS_URL` ending in
`/api/auth/jwks`. Follow `backend/docs/staff.md` for the Go listener/operator.
Start each listener explicitly on numeric loopback. Setup is not automatic on
server start or build.

## Browser and server boundaries

Sign in at `/sign-in`; `/dashboard` displays staff name/access state, sign-out
and a link to the read-only `/dashboard/catalog` page. Every dashboard load,
sign-out action and BFF request checks the actual uncached server session; no
cookie-presence/layout authorization is used.
Sign-in/out use server actions with `nextCookies` last. Cookie/session caching
is disabled. Existing Next Cache Components/Partial Prefetching stay enabled;
a public Suspense fallback surrounds request-time content. Dashboard headers are
read before auth initialization, so prerender/build cannot initialize a session.
Cache Components use the default Node runtime: incompatible `runtime`/`dynamic`
route exports are deliberately absent. No auth component uses `use cache`.
The private operator state path is runtime input, explicitly excluded from
Turbopack asset tracing; credentials must not become bundled build assets.
Writes require an explicit matching Origin; missing/foreign origins
cannot fall back to Fetch Metadata or forwarded Host. GET may omit Origin, but a
supplied foreign Origin is rejected.

The public auth catchall allows **only GET `/api/auth/jwks`**. Signup, session,
sign-in/out and token HTTP endpoints are closed. Runtime signup is also disabled
at the library level. The official server `getToken` remains available; tokens
are subject-only Ed25519, exact issuer, fixed audience `restaurant-assistant-api`,
and 60-second lifetime. Automatic `set-auth-jwt` is disabled. No API token or full
session is serialized to the browser or stored in localStorage. Previously
issued Go JWTs may survive logout for at most 60 seconds; immediate Go token
revocation is not claimed.

`GET /api/staff/access` ignores caller authority headers and rejects all queries
and request bodies before authentication or Go access. A body stream, any
Transfer-Encoding header, or Content-Length other than exactly `0` returns the
contract's redacted400 invalid-input response; bodyless GET (including explicit
zero length) remains supported. Framing headers are checked even if Next has
stripped the GET stream; the BFF does not consume arbitrary incoming bodies.
It sends only the official server JWT to fixed `/v1/staff/access` via direct
non-proxy Node HTTP with a 2-second absolute deadline, no redirects and a 16KiB
body bound. Responses must pass the shared strict DTO schema. Go403 stays denied;
other dependency/protocol failures become redacted 503, never successful empty
access. No order counts, settings or business defaults are fabricated.

## Verification and evidence

- `pnpm test:auth`: contract/auth components on fresh temporary PostgreSQL, real
  supported Better Auth APIs, sessions and JWTs. Only `server-only` is stubbed.
  The fake Go in `access.test.ts` and operator callback in `setup.test.ts` remain
  component seams, not independent Go authorization proof. Retained independent
  public regressions cover private-state invariants and conflicting partial setup.
- `pnpm test:dashboard`: the owned `tests/integration/dashboard.mts` harness,
  not an inherited/live target. It ignores inherited database configuration,
  creates a temporary cluster/private generated fixtures, runs real Go migrations
  and setup concurrently, launches numeric-loopback Next/Go, and cleans up only
  its own listeners/state/database. It runs `tests/e2e/auth.spec.ts` plus actual
  Alex/Sam and a second Alex browser session: action-cookie propagation, official
  JWT/JWKS→Go membership, revocation, failed setup without reactivation/reset,
  logout isolation, absent membership, Go/JWKS outages/recovery and auth DB outage.
- `pnpm typecheck` and `pnpm build`: build with runtime auth environment unset.
  Auth APIs remain dynamic and personalized pages partially prerendered with
  request-time content; no runtime provisioning/configuration is required to build.

Observed parent checks: 22 component/contract tests, the real owned browser seam,
full Go normal/race/vet and production build passed. Original browser compile500
failure is retained in `odd/tasks/protected-dashboard.md`; the bounded correction
keeps Cache Components and suspends before auth initialization. The first build's
whole-project asset-tracing warning was corrected by excluding the private input
path, not by bundling credentials. Targeted independent static review of the final
request/asset boundary found no blocker; its runtime was unavailable because of
verifier tool/scratch-policy limits. The human explicitly accepted static review
plus parent real runtime for LOCAL delivery, retaining the final PARTIAL and
unexecuted check. RDD is disabled for this clone by explicit user instruction;
the earlier candidate decline is not approval. Local source delivery is tracked
separately.
The earlier independent formatter PARTIAL (scratch-only probe formatting) stays
recorded with explicit human acceptance; it is not relabeled PASS.

No screenshots, traces, storageState or credentials are emitted. The JWT test
asserts its declared 60-second lifetime and immediate post-logout acceptance;
there is no new elapsed-60-second expiry/rotation/cache proof. Component expiry
and Go expired-token checks are separate evidence, not a timed browser claim.
No orders/business workflows, live accounts/services or production deployment
are proved or enabled by these local checks.
