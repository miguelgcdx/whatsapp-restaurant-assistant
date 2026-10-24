# Local staff access foundation

This unit provides authentication and a protected access shell, **not orders**.
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

Sign in at `/sign-in`; a minimal `/dashboard` displays only staff name/access
state. Every dashboard load, sign-out action and BFF request checks the actual
uncached server session; no cookie-presence/layout authorization is used.
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


## Component verification

Run `pnpm test:auth` and `pnpm typecheck`; isolated auth components and the
separate Go authorization checks have explicit evidence/limits in the feature
tracker. Combined browser orchestration is delivered in the following slice.
