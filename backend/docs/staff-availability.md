# Staff availability controls

## Requests

The loopback `local-staff-api` exposes two operational controls, using the same
server-configured restaurant, verified short-lived EdDSA JWT, and current app
membership as `/v1/staff/access`. These writes do **not** require the catalog
cursor key. They do not edit names, prices, currency, fees, orders or membership.
Runtime never migrates or supplies operational defaults. See [staff.md](staff.md)
for explicit local setup; apply migrations separately before serving requests.

| Method and exact path | Exact JSON fields | `data` on 200 |
| --- | --- | --- |
| `PATCH /v1/staff/catalog/items/{id}/availability` | `{expected_version:STRING, available:BOOL}` | `{id,name,price:{amount_minor,currency,exponent},available,version}` |
| `PATCH /v1/staff/restaurant/availability` | `{expected_version:STRING, open:BOOL}` | `{open,version}` |

IDs are lowercase canonical UUIDv4. Versions are canonical nonnegative decimal
strings (`"0"`, `"1"`, …), bounded by signed int64. JSON numbers, leading zeros,
negative/overflow versions, nulls, duplicate/extra/missing fields, trailing JSON,
invalid UTF-8 and query parameters are rejected. Only `application/json` is
supported (optional UTF-8 charset); raw body limit is 64 KiB, inclusive. Other
methods return 405 with `Allow: PATCH`. Dispatch never cleans paths or redirects.

Supply exactly one `Authorization: Bearer <JWT>` and one `Idempotency-Key` header.
Keys contain 1–128 printable ASCII bytes, excluding whitespace/control characters.
Caller actor/restaurant/role headers and body fields cannot grant authority.
Go does not replace JWT verification with an Origin check. Browser access needs
the separately owned same-origin BFF/session/CSRF boundary, not direct authority
headers or a cursor. The Go component alone does not prove that browser seam. See the separately owned
[Next BFF and local UI guide](../../docs/local-availability.md) for the protected
write routes, controls, explicit same-intent retries and browser evidence boundary.

## Results, conflicts and retries

All responses on these routes contain `{data,request_id}` or
`{error:{code,message},request_id}`, JSON content type and `Cache-Control: no-store`.
Request IDs are server-generated. Item price/exponent come from configured data,
not assumed currency/decimals. Stored fields are validated without clipping;
serialized successful responses, including escaping/envelope/newline, must fit
512 KiB. Corrupt stored output fails safely and rolls back the mutation.

| Status | Code | Exact safe message |
| --- | --- | --- |
| 400 | `invalid_input` | Invalid request. |
| 401 | `unauthenticated` | Authentication is required. |
| 403 | `forbidden` | Staff access is not available. |
| 404 | `not_found` | Catalog item was not found. |
| 405 | `method_not_allowed` | The method is not allowed. |
| 409 | `version_conflict` | Refresh availability before trying again. |
| 409 | `idempotency_conflict` | Idempotency key was already used for a different request. |
| 413 | `payload_too_large` | Request body is too large. |
| 415 | `unsupported_media_type` | Content-Type must be application/json. |
| 500 | `internal_error` | Availability could not be completed. |
| 503 | `temporarily_unavailable` | Availability is temporarily unavailable. |

Missing operational settings, dependency failures and cancellation are 503, not
invented settings or successful empty data. A missing or cross-scope item is 404.
Stale/exhausted versions are 409. Every fresh accepted intent increments exactly
once, **even when its boolean already equals the requested value**.

A key is scoped to authenticated subject, configured restaurant, PATCH and the
canonical resource. Fingerprints use SHA-256 of parsed canonical JSON semantics;
whitespace/key order do not change intent. An authorized same-key/body retry
returns the **exact committed 200 payload bytes, including original request ID**,
even after subsequent changes or version exhaustion. Changed body with that key
returns 409 without mutation. Replay is not fresh current-state readback; refresh
the catalog after success/conflict when current state is needed.

There is no automatic mutation retry. Preserve the same key and body when
explicitly retrying an uncertain response. After a definite version conflict,
refresh before deciding a new intent/key. Do not reuse an old successful key to
attempt a new change, or silently retry with the latest version.

## Transaction and ownership

`catalog.Store.ApplyAvailability(ctx, AvailabilityIntent, authorize)` locks the
existing restaurant identity first (same order as create-only setup), then calls
a fresh local DB membership check **before** reading any complete result. The
HTTP handler verifies JWT/JWKS before opening this transaction and passes
`staff.Store.Access`; no external provider calls occur while holding the lock.
Store/membership must be constructed for the same server-owned restaurant.

A new intent uses the shared item CAS (or settings CAS), validates/serializes its
DTO and inserts a complete `app.operations` result in the **same transaction**.
The parent-owned unique scope constraint plus short per-restaurant row lock makes
duplicate requests converge and different-key/same-version requests yield one
winner. No separate pending claim, TTL, cleanup, advisory-lock framework or
external effect exists. Rejected/rolled-back intents leave no committed result
or domain change. Commit-outcome uncertainty is resolved with the same key/body,
not automatic non-idempotent resend. No retention/deletion policy is inferred.

Create-only `Initialize` ignores mutable open/version and item availability/version
on retry, preserving closure/inactive state. Currency, exponent, fee, IDs, names,
prices and menu composition remain immutable. The exported legacy
`SetAvailability` preserves its `ErrStale` behavior for missing/cross-scope/stale/
exhausted items and same-value increment behavior.

## Evidence boundary

Component tests use owned `testpg` clusters, explicit migrations, generated
Ed25519 JWT/JWKS and loopback HTTP. They cover both controls, canonical bodies,
exact replay, changed-body keys, stale/exhausted versions, duplicate/distinct-key
concurrency, malformed boundaries, revocation, outages/cancellation, post-CAS
rollback/key reuse, missing configuration, and preserved foundation/outbound state.
An identity-lock deadline test proves replay waits without checking membership
prematurely; a subsequent revoked replay is forbidden. A separate public-HTTP test,
`TestLiveReplayRechecksRevocationAfterIdentityLock`, observes the same still-live
replay backend blocked by the known identity-lock holder using bounded, owned-DB
`pg_stat_activity`/`pg_blocking_pids` checks. Membership is revoked while that exact
backend remains blocked and its callback count is zero. Releasing the lock lets
the same uncanceled client request complete with safe/no-store 403 after one fresh
membership check, preserving exported catalog/settings, committed result count
and payload, and public foundation/outbound state.
Production dispatcher tests exercise both writes with and without a cursor key,
while preserving legacy access/read/unknown-path behavior and CLI help/options.
The parent subsequently reports full Go normal/vet/race passing in 24 seconds,
including the same-live-request revocation proof above. That parent-owned Go result
is distinct from these component checks. The parent also observed the full JavaScript suite/typecheck/build and real browser
journey passing, including both controls, stale conflicts and same-intent retry after
real commit/response loss. These are parent-attributed, not independent browser results.

The independent three-package Go race run passed, including the retained public-HTTP
`TestVerifierConflictingSameKeyAndFailureReuse` regression for contradictory same-key
requests and failed-key reuse. The independent Node run passed eight tests but unexpectedly
provisioned 209 dependencies, violating the no-network/no-install constraint. Original
source/index/status integrity passed; original/shared dependency write isolation remains
unverified without a pre-run dependency manifest. S4 / overall independent proof remains
PARTIAL. The human explicitly accepted this exception for this LOCAL increment only;
no verification rerun, cleanup, publication or future waiver followed. See the
[local guide](../../docs/local-availability.md) and feature tracker for full attribution.
