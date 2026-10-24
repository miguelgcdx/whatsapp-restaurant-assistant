# Local staff authentication/access component contract

Parent-owned handoff for `odd/tasks/protected-dashboard.md`, T1–T4, S1–S3.
This is the auth/access foundation of the planned dashboard, **not order views**.
No menu/currency/fees, operational settings, order data, real accounts or live
services are provisioned. The existing synthetic API/worker remain supported.

## Frozen authority and wire boundary

- Better Auth 1.7.12 and its CLI `auth` 1.7.12 own schema `auth` and password/session/JWK handling. Use supported APIs, never auth-private adapters/tables or custom password/token cryptography.
- PostgreSQL/Goose owns `app` and existing `app.goose_db_version` bookkeeping. Migration 00002 adds restaurant **identity only** and membership. Business settings are deferred, not silently defaulted. `auth_user_id` is opaque text without a private-auth FK.
- Ed25519 / JWT `EdDSA` only. Fixed audience `restaurant-assistant-api`; issuer is exact configured local Better Auth origin. Require signed `sub`, `iss`, `aud`, `iat`, `exp`; nonempty bounded subject, nonfuture issue time, unexpired token and `0 < exp-iat <= 60 seconds`. No arbitrary algorithm, caller key/JWKS URL, unsigned header identity or membership caching.
- Public auth route permits JWKS retrieval; API JWT is never returned to browser. Disable HTTP `/token` and automatic `set-auth-jwt` header. Test the official server-side `auth.api.getToken({headers})` remains usable. Keep auth initialization/session/token/DB/config modules server-only. No full session objects, cookies, secrets or JWT rendered/serialized/logged.
- Sign-in/out use supported server actions and documented cookie propagation (`nextCookies` last when used), not browser API JWT. Runtime signup disabled. Every protected page/action/handler validates an authoritative uncached server session. Proxy/layout checks are optional navigation only.
- Browser state-changing requests require a matching explicit configured Origin; do not accept missing/foreign Origin or substitute Sec-Fetch-Site/forwarded Host. Next/Better Auth protections supplement, not replace, application checks. In-process offline setup API has no browser endpoint.
- Same-origin BFF only calls fixed configured Go origin and fixed access path. Never forward browser Authorization/role/restaurant/identity headers, arbitrary path/query, cookies or provider secrets to Go. Validate Go response before returning minimal DTO. No automatic non-idempotent retries. Use a direct non-proxy HTTP transport (e.g. node:http with agent:false for the local HTTP origin); do not inherit ambient proxy settings. GET may omit Origin, but a supplied foreign Origin is rejected; state-changing actions always require the explicit matching Origin.

### Staff access HTTP

Go: `GET /v1/staff/access`, BFF: `GET /api/staff/access`.
No request body, query parameters or browser restaurant selection. Both require
current membership in the server-configured singleton restaurant; existence of an
auth user alone grants no application access. BFF authenticates its cookie and
obtains a JWT server-side; Go independently validates JWT/current membership.

Success 200 is exactly the shape `{ "restaurant_id": "<canonical UUID>" }`.
The parent-owned Zod contract is `src/contracts/staff-access.ts`; derive TS types
from that schema rather than duplicating them. Dashboard shows only selected
staff display name and access state; it must not show a fabricated order count.
Go JSON encoding may terminate with newline; Next JSON need not. Tests assert
the exact streams of their own public interface, not incidental object order.

Errors use `{ "error": { "code": "...", "message": "..." } }`:

| Status | Code | Exact message |
| --- | --- | --- |
| 400 | invalid_input | Invalid request. |
| 401 | unauthenticated | Authentication is required. |
| 403 | forbidden | Staff access is not available. |
| 405 | method_not_allowed | The method is not allowed. |
| 503 | temporarily_unavailable | Staff access is temporarily unavailable. |
| 500 | internal_error | Staff access could not be completed. |

Wrong method includes `Allow: GET`. Failed dependency is not an invalid token or
successful empty view. Protected UI may redirect to sign-in/access-denied or show
a safe unavailable state; direct BFF/Go endpoints must return the stated statuses.
Errors never expose credentials/configuration, tokens or private library errors.

## Process/configuration and ownership

All actual service/database origins must be explicit numeric-loopback addresses;
no proxy/redirect escape, userinfo, query or fragment on service origins. Database
name remains guarded `restaurant_foundation_` plus nonempty suffix. Do not change
existing synthetic command defaults/streams/behavior. Runtime never migrates or
provisions. Next config/DB initialization is lazy: build/typegen do not seed or
require a live DB. No external auth telemetry or background keepalive SQL. Node must construct the pool from validated explicit host/port/database fields rather than passing a raw URI that driver query parameters can reinterpret; reject target-override parameters and inherited host/database/service defaults. Local sslmode=disable is supported. Fixture setup is cross-process serialized with recoverable OS locking or equivalent, never a permanent crash-stale lock.

- `LOCAL_DATABASE_URL`: dedicated loopback PostgreSQL, shared logical namespaces.
- `BETTER_AUTH_URL`: exact local origin (e.g. `http://127.0.0.1:3000`), issuer.
- `LOCAL_DASHBOARD_STATE`: private generated setup-state path; default `backend/.cache/dashboard/setup.json` from project root. No committed/default passwords or secret.
- `STAFF_API_URL`: Node-only fixed Go origin (e.g. `http://127.0.0.1:8081`).
- `LOCAL_RESTAURANT_ID`: Go configured canonical UUID from setup state.
- `STAFF_JWT_ISSUER`, `STAFF_JWKS_URL`: Go exact expected origin and fixed public JWKS URL (`/api/auth/jwks`). Audience is the shared literal above.

Go staff API command `cmd/local-staff-api`: loopback `-listen` default
`127.0.0.1:8081`, same owned shutdown/redaction discipline as existing commands.
No implicit migration. Request-driven bounded JWKS fetch/cache, not periodic
refresh; fixed trusted URL, bounded body/keys/deadlines and unknown-key refresh
suppression. Failed JWKS dependency fails closed. Never cache membership.

Go operator fixture command `cmd/local-staff-membership`: `-restaurant-id`,
`-auth-user-id`, `-action ensure|revoke`; `LOCAL_DATABASE_URL` required.
`ensure` creates identity/membership if absent and **does not reactivate** an
existing revoked membership. `revoke` is idempotent. Exit0 stdout is minimal JSON
`{"restaurant_id":"...","auth_user_id":"...","active":true|false}` plus
newline, stderr empty. Help/error contracts belong to command help/docs/tests;
failures are redacted and exit nonzero. No passwords or emails in Go tables.

Go public component entry points in `internal/staff`:
`OpenStore(ctx, dsn, restaurantID)`, `Store.Access(ctx, subject) (bool,error)`,
`EnsureMember(ctx, subject) (bool,error)`, `RevokeMember(ctx, subject) error`,
`Close()`. Store constructors use parent `postgres.LocalPoolConfig(dsn)` and
existing typed generated queries; no new permissive DSN parsing or hand-built
SQL. Handlers/verifier constructor names may follow idiomatic worker design;
wire/config and public state semantics above are fixed.

## Setup lifecycle

`pnpm setup:auth` is explicit local setup, never a runtime route/import side effect.
Prefer a native Node26 TypeScript/ESM entry if tests prove it works; do not add a
loader/upgrade typings just from a runtime-version assertion.

1. Validate guarded target/origin and private same-user nonsymlink state directory/file. Create random auth secret, restaurant UUID and Alex/Sam random passwords **before** provisioning; private0700 directory/0600 file. Bind state to nonsecret database identity, reject mismatched target/invalid existing state; no secret overwrites. Never echo passwords/secret/DSN in tool/process logs.
2. Explicit Go app migrations; pinned Better Auth 1.7.12 exported `better-auth/db/migration` planner/`runMigrations` for schema auth, the same planner used by `auth migrate`. The pinned CLI is installed but setup does not invoke it. Use the same factory/plugin schema with telemetry disabled. No runtime migration, destructive rollback/reset or auth-private adapter/table access. Real two-owner setup and reapplication must be verified, not inferred from imports.
3. Separate non-HTTP setup-only Better Auth instance enables signUpEmail; production/runtime instance disables signup. Use supported library hashing/provisioning. Recognize only actual account-exists error, then authenticate with preserved fixture password using supported API and clean up any setup-created session. Arbitrary400 or wrong existing password fails safely; never resets another account password.
4. Pass returned opaque subject to Go `ensure`. A failure between owners is incomplete setup, not rolled-back auth or success. Rerun reuses preserved credentials, does not duplicate memberships or undo revocation. Mark complete only after both fixtures have active membership; avoid persisting auth sessions in state.
5. Output nonsecret setup summary/state path; operator can read their own private generated credential file. No emails/network services. Existing foundation records/outbox must survive migration reapplication.

## Shared app SQL and pool helper (parent T1)

`app.restaurants(id uuid PRIMARY KEY)` is identity only at this stage.
`app.staff_memberships(restaurant_id uuid REFERENCES app.restaurants(id), auth_user_id text, active boolean)` has composite primary key and bounded nonblank subject.
Financial/open/queue configuration and orders are later migrations, not default
values. Parent adds sqlc queries for ensuring identity/member, reading active
membership and revoking it; regen with existing pinned sqlc1.31.1. Existing
foundation queries/behavior remain unchanged. Parent exposes guarded
`postgres.LocalPoolConfig` by delegating to existing localConfig, not weakening it.

## Exact delegated edit surfaces

### T2-Go worker

- `backend/internal/staff/**`
- `backend/cmd/local-staff-api/**`
- `backend/cmd/local-staff-membership/**`
- `backend/docs/staff.md`

Do not edit postgres/shared generated SQL, migrations, dependencies or tracker.
Request parent changes if a frozen interface cannot be implemented as stated.

### T2-Next worker

- `src/server/auth/**`
- `src/server/staff-access.ts`
- `src/app/api/auth/**`
- `src/app/api/staff/access/**`
- `src/app/sign-in/**`, `src/app/dashboard/**`, `src/app/access-denied/**`, `src/app/unavailable/**`
- `src/app/page.tsx`, `src/app/layout.tsx`, `src/app/globals.css`
- `scripts/local-auth/**`
- `tests/auth/**`, `tests/helpers/auth-postgres.mts`, `tests/e2e/auth.spec.ts`
- `docs/local-auth.md`

Parent owns package/lock/config/shared DTO, browser orchestration/config, feature
tracker, architecture/product docs, Go siblings and Git. Do not change them.
Real Go integration follows both T2 units and is completed by parent T3; the Next unit may use a
bounded loopback fake staff API for component tests, never claim it proves Go auth.

## Evidence and completion

Parent shared-tooling commands: `pnpm test:auth` runs contract/auth component tests,
`pnpm typecheck` checks TypeScript, and `pnpm dev:local` binds Next dev to
`127.0.0.1` without changing the existing `dev` command. `pnpm exec playwright test`
is reserved for real browser scenarios once parent orchestration is present.
`pnpm setup:auth` above is the T2-Next setup contract, not a claim T1 already implements
or has run provisioning. Missing prerequisites must fail, never silently seed.

Load assigned skills before writing. Actual deterministic RED before each new
behavior, then GREEN. Tests use public API/component/HTTP/CLI, no auth-private
storage or direct business-table state queries. Include unchanged foundation
behavior where shared config/migrations are touched. Isolated databases only,
never existing environment DSNs/real accounts. Browser checks use real Next,
actual generated fixtures and independent sessions; distinguish component fake
Go evidence from final real-cookie/JWT/Go-membership seam. Cover anonymous/forged/
expired/wrong-issuer/audience/algorithm, withheld/revoked membership, two sessions,
logout/token window, HTTP-token/header non-exposure, origin denial and partial
setup preservation. Denials create no business/jobs/outbound effects.

No native approval, runtime compatibility or full dashboard completion is implied
by this document. Report exact failed/skipped/unavailable checks and S# coverage;
parent independently verifies risk2/4/5 before delivery. No commits by workers.
