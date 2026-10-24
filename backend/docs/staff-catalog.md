# Protected local staff catalog

## Run and capability

The existing loopback `local-staff-api` also serves `GET /v1/staff/catalog`.
Use the explicit local DB/restaurant/JWT configuration in [staff.md](staff.md).
Optionally supply `STAFF_CATALOG_CURSOR_KEY`: exactly 64 lowercase hexadecimal
characters encoding 32 random bytes, generated explicitly for this application
purpose. Keep it private and independent of JWT/auth secrets. Never commit it.
There is no default, startup-generated fallback, or secret output. Missing key
leaves `/v1/staff/access` usable but authenticated catalog reads return 503.
A nonempty malformed key fails startup with the existing redacted CLI error.
Key rotation invalidates existing continuation cursors; restart paging.

Runtime does not migrate or seed. Explicit catalog setup remains a separate local
operation; missing restaurant settings fail closed rather than inventing defaults.
The original Go-only slice had no browser/BFF seam. The additive
[read-only dashboard guide](../../docs/local-catalog.md) describes the Next BFF,
protected browser page and separately owned verification. There is still no
ordering feature. Two [availability PATCH controls](staff-availability.md) are
now available using the same JWT/membership and configured restaurant, without a
cursor-key dependency. Names/prices/menu/settings configuration remain fixed.

## Request and response

Send one `Authorization: Bearer <JWT>` header. Every page validates the JWT using
the existing verifier and queries CURRENT membership, including continuations.
A cursor grants no authority. Go does not substitute an Origin check for JWT.

Optional query parameters are `limit` (default 50, range 1–100) and `cursor`
(nonempty opaque ASCII, at most 2048 characters). Omit cursor on the first page.
Duplicate/unknown fields, malformed encoding, payloads and body framing are 400.
Other methods return 405 with `Allow: GET`. Exact-path dispatch retains legacy
access/unknown-route behavior without ServeMux normalization redirects.

Success is 200 with exactly:

```text
{data:{items:[{id,name,price:{amount_minor,currency,exponent},available,version}],next_cursor,restaurant:{open,version}},request_id}
```

Money and versions are nonnegative canonical decimal strings bounded by MaxInt64.
Currency/exponent come from stored settings, never assumed USD/two decimals.
Names preserve Unicode and authored text, must be nonblank valid UTF-8 and at
most 200 code points. IDs must be lowercase canonical UUIDv4. Invalid stored
scalars return safe 500, never clipped or coerced. End-of-list `next_cursor` is
explicitly null; an empty menu is an empty array. The actual serialized response,
including envelope, escaping, newline and random server request ID, is checked
against 512 KiB before writing success.

Errors contain `{error:{code,message},request_id}`. Every catalog success/error
has `Cache-Control: no-store`. Codes: 400 `invalid_input`, 401 `unauthenticated`,
403 `forbidden`, 405 `method_not_allowed`, 500 `internal_error`, and 503
`temporarily_unavailable`. Missing settings/key and DB/JWKS failure are 503, not
successful empty pages. No SQL, DSN, bearer token, key or provider payload is
returned. Legacy access envelopes/messages are unchanged.

## Paging semantics

The separate `catalog.Store.Page` API reads scoped PostgreSQL rows in ascending
`(created_at,id)` order with a keyset predicate and SQL `LIMIT limit+1`. It does
not call the unbounded internal `Menu` API. Lookahead determines whether more
rows exist; the cursor names the last EMITTED row. Existing catalog APIs remain
unchanged. Settings are current at the read; availability can change between
pages and there is no snapshot transaction spanning requests.

Cursors use purpose-separated HMAC-SHA256 and bind version, collection kind,
configured restaurant, UTC microsecond timestamp and UUID position. Noncanonical
encoding/shape, unknown fields, tampering, wrong purpose/scope or position reject
with 400 after current authorization. No cross-request transaction is retained.

## Stored-data boundary

PostgreSQL `timestamptz` permits infinity, negative infinity and years outside
RFC3339. `Store.Page` decodes these explicitly and returns the safe
`catalog.ErrStoredData` sentinel rather than classifying them as a dependency
outage. The HTTP handler maps that sentinel to 500 `internal_error`. All fetched
positions, including lookahead and terminal-page rows, are checked before any
success is emitted; an invalid lookahead fails the page rather than hiding a bad
next position. True DB/JWKS failures, cancellation, and absent settings remain
503. No stored value is clipped, normalized into a different position, or skipped.

Existing migration 00003 already prevents negative prices/fees/versions, bigint
overflow, invalid currency/exponent, currency mismatch, null required fields,
names over 200 code points, and names empty after trimming ASCII spaces.
PostgreSQL UUID/boolean types prevent malformed representations; UUID text output
is canonical, but the UUID type does NOT enforce version 4. UTF8 databases reject
invalid UTF-8 and PostgreSQL text rejects NUL. Those impossible persisted cases
do not justify disabling constraints in tests. Application validation still
checks UUIDv4 and Unicode nonblank names (SQL `btrim` does not reject every
Unicode whitespace-only name), and retains defensive scalar validation.

## Verification boundary

Component tests use generated Ed25519/JWKS and cursor keys, loopback HTTP and
owned `testpg` clusters with explicit migrations, never inherited DSNs or live
services. Tests cover 105 equal-timestamp rows, traversal, defaults/limits,
Unicode/numeric strings, empty/exact/tail pages, query/body errors, signed cursor
scope/purpose/shape rejection, revocation, unavailable dependencies and preserved
catalog/settings/foundation state. Named signed-cursor cases cover every missing
required field, duplicate/unknown fields, canonical JSON/base64/signature,
purpose/version/restaurant and UUID/time boundaries. Isolated fixture SQL changes
only timestamps, without removing constraints, and proves stored infinities/year
10000 fail through both exported Page and public HTTP. The CLI package tests use
the actual production dispatch helper with real handlers/PG/JWKS, both with and
without a key, preserving exact legacy access streams and unknown-path 404s.
Parent normal tests, vet and race tests passed. Independent static review found
no established candidate blocker, but its sole runtime launch failed before Go
because the verifier-added `/usr/bin/time` was missing: runtime count 0, verdict
PARTIAL. The human explicitly accepted static review plus parent runtime evidence
for this local delivery only; this is not independent runtime PASS or a deployment
grant. [The feature log](../../odd/tasks/staff-catalog-read.md) preserves the full
failed-launch evidence and disposition. Those prior-slice component tests do
not prove a browser/BFF seam. The new dashboard candidate must collect its own real integration and
independent verification evidence; this historical waiver is not inherited.
