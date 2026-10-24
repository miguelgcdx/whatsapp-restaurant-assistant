# Local Go staff access

This component provides authentication and membership access and an optional
[protected catalog reads](staff-catalog.md) and two versioned
[availability controls](staff-availability.md). It does not retrieve orders,
create business settings, read Better Auth's private tables, or
contact live providers. Better Auth owns identity; Go owns app membership with an
opaque subject and no cross-schema auth foreign key.

## Explicit setup and membership

Use a dedicated numeric-loopback PostgreSQL database named
`restaurant_foundation_<nonempty suffix>`. Set `LOCAL_DATABASE_URL` explicitly.
Run the existing `local-migrate` command as a separate setup operation first.
Neither staff executable nor `staff.OpenStore` migrates or seeds at runtime.

After the parent-owned local auth setup obtains an opaque user subject:

```sh
go run ./cmd/local-staff-membership -restaurant-id "$LOCAL_RESTAURANT_ID" \
  -auth-user-id "$AUTH_USER_ID" -action ensure
```

Run Go commands from `backend`. `-restaurant-id` requires a canonical lowercase
hyphenated UUID. `-auth-user-id` is an opaque nonblank UTF-8 subject, at most 256
bytes, not a password or email. `-action` must be `ensure` or `revoke`.

`ensure` atomically creates missing restaurant identity and membership, without
operational defaults. Repetition is safe and **never reactivates a revoked
membership**. `revoke` is idempotent, including an absent membership. A revoked
membership remains inactive on setup retry. No activation command is provided.
The successful stdout stream is exactly one JSON object plus newline:

```json
{"restaurant_id":"12345678-1234-4234-8234-123456789abc","auth_user_id":"fictional-subject","active":true}
```

Revoke and ensure-of-revoked return `active:false`. Success exits 0 with empty
stderr. Failures exit 1, leave stdout empty, and write exactly
`Staff membership could not be completed.` plus newline to stderr. `-help` exits
0 and writes usage to stdout without reading configuration or contacting a DB.
Library methods return redacted sentinel errors; they do not expose driver errors.

## Protected runtime

Set these explicit values before starting `go run ./cmd/local-staff-api`:

| Variable | Meaning |
| --- | --- |
| `LOCAL_DATABASE_URL` | Guarded local database shared with explicit app setup |
| `LOCAL_RESTAURANT_ID` | Singleton configured canonical UUID |
| `STAFF_JWT_ISSUER` | Exact local Better Auth origin, e.g. `http://127.0.0.1:3000` |
| `STAFF_JWKS_URL` | Fixed local JWKS URL ending exactly `/api/auth/jwks` |
| `STAFF_CATALOG_CURSOR_KEY` | Optional separate application-purpose key: 64 lowercase hex characters / 32 random bytes; missing disables authenticated catalog reads with 503, not availability writes |

Service origins require numeric loopback, explicit port, no userinfo, query or
fragment. `-listen` defaults to `127.0.0.1:8081`; only numeric-loopback host:port is
accepted (ports 0–65535). Port 0 permits an OS-assigned listener for local testing.
No ambient HTTP proxies or redirects are used for JWKS. Startup is lazy: it does
not connect to the DB or fetch JWKS before a request. Runtime errors exit 1 with
empty stdout and exactly `Staff API could not be completed.` plus newline on
stderr. `-help` prints usage to stdout and exits 0 without configuration reads.
SIGINT/SIGTERM initiates bounded HTTP shutdown and closes the owned pool.

`GET /v1/staff/access` accepts one `Authorization: Bearer <JWT>` header, no query
or body. Only trusted Ed25519 `EdDSA` signatures are accepted, using pinned
`jwt/v5`, with required exact issuer, single audience `restaurant-assistant-api`,
nonblank bounded subject, issued-at and expiry. Issued-at must not be future;
expiry must be unexpired, later than issued-at and at most 60 seconds after it.
Caller identity/role/restaurant headers never grant authority. Every valid
request queries current membership in the configured restaurant; no membership
cache exists. Missing/cross-restaurant/revoked membership is forbidden.

Success is `200` with exactly `{"restaurant_id":"<canonical UUID>"}` plus newline.
Errors have `{"error":{"code":"...","message":"..."}}` plus newline:

| Status | Code | Message |
| --- | --- | --- |
| 400 | invalid_input | Invalid request. |
| 401 | unauthenticated | Authentication is required. |
| 403 | forbidden | Staff access is not available. |
| 405 | method_not_allowed | The method is not allowed. |
| 503 | temporarily_unavailable | Staff access is temporarily unavailable. |

405 also includes `Allow: GET`. Dependency failure is not an invalid-token claim
or a successful empty view. Responses use `Cache-Control: no-store`. Tokens and
DSNs are never included in error streams. This endpoint performs no
business/job/outbound writes. Logout does not immediately invalidate an already
issued JWT: its exposure window is at most its 60-second lifetime; revoking app
membership denies subsequent requests independently of token expiry.

`PATCH /v1/staff/catalog/items/{id}/availability` and
`PATCH /v1/staff/restaurant/availability` use the same JWT/current membership.
They require JSON `{expected_version:STRING,available:BOOL}` or
`{expected_version:STRING,open:BOOL}` respectively, plus one `Idempotency-Key`.
See [the write contract](staff-availability.md) for strict bounds, exact errors,
transactional replay and why uncertain retries must retain the same key/body.
These routes do not change the legacy access envelope above or need a cursor key.

## Key cache and bounds

The verifier synchronizes all key-cache access. Fetches are request-driven, with
no periodic refresh. The set expires after one minute; an unknown key can trigger
at most one refresh per five seconds globally, including failed fetches. During
suppression unknown keys fail closed (401 with a valid cached set; 503 if no valid
set exists). New key rotation may therefore be denied during that short window.
A fetch error fails the triggering request closed; unexpired known cached keys
remain usable without a fetch. Expired keys are not used on a failed refresh.

JWKS has a two-second total deadline, 64 KiB body cap, and 1–32 distinct bounded
key IDs. Only public OKP/Ed25519 keys of exactly 32 bytes are accepted; conflicting
algorithm/use, duplicate IDs or private key material invalidate the set. Token
size is bounded to 8192 bytes. HTTP header/read/write/idle deadlines and header
size are bounded; request DB/JWKS work has a five-second context. The shared local
pool helper bounds connections/timeouts and is reused without permissive parsing.

## Verification boundary

Tests use owned throwaway PostgreSQL via `testpg`, generated Ed25519 keys and
loopback JWKS servers, never inherited database credentials. They exercise public
store, HTTP, verifier and command streams: claim failures, forged tokens, current
membership/revocation, ensure/revoke idempotency, no runtime migrations, no
cross-restaurant authority, dependency failure, bounded JWKS parsing,
redirect rejection, cold-cache races, unknown-kid suppression, cancellation and
foundation record/job/outbox preservation through the public foundation API.

From `backend`, with the parent-specified local toolchain/cache environment:

```sh
go test -race -count=1 -timeout=25s ./internal/staff ./cmd/local-staff-api ./cmd/local-staff-membership
go vet ./internal/staff ./cmd/local-staff-api ./cmd/local-staff-membership
```

These Go component tests do **not** prove the real browser cookie → Better Auth
server token → Go integration. Parent T3 owns that seam and independent security
verification. No live services, migrations at runtime, publication or delivery
approval are implied.
