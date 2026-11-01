# Pickup staff HTTP adapter

**Status: `pickup-staff-v1-r3` source-only update — UNVERIFIED.**
The authorized change includes projection, fixture expectations and documentation
only. No compilation, tests, vet, runtime or independent verification were run
for this update. Historical checks remain historical; stopped proof runs were
not reopened. Shared service integration and broader acceptance remain separate.

`NewHandler(cart.PickupStaffPort, *staff.Verifier)` adapts only the four routes
in the [canonical LOCAL pickup contract](../../docs/local-pickup-orders.md#staff-wire-subset):

- `GET /v1/staff/orders`
- `GET /v1/staff/orders/{id}`
- `GET /v1/staff/orders/{id}/summaries/{summary_id}/units`
- `POST /v1/staff/orders/{id}/actions`

The last route accepts exactly `action` and decimal-string `expected_version`,
with an `Idempotency-Key`. Actions are `accept`, `reject`, `start_preparing`,
`mark_ready`, `mark_picked_up`, `cancel`, and `mark_paid`; there is no generic
status setter. Lists and units accept only `limit` (default 50, maximum 100)
and a nonempty opaque `cursor` (maximum 2,048 ASCII characters).

The adapter verifies the bearer token itself with the existing verifier, then
passes only the verified subject and typed request to the port. Restaurant,
current membership, resource relationships, authenticated cursors, transition
policy, atomic effects, and original replay data remain domain responsibilities.
There is no handler replay cache or browser-selected authority. Domain errors
are mapped to safe envelopes without copying their messages.

Responses use `no-store`, decimal money/version strings, UTC times, explicit
nullable metadata, and the initial pickup-only projection. The sole collection
link is an allowlisted relative `/api/staff/orders/.../summaries/.../units` BFF
link. Deferred workflow metadata/links are not fabricated. Responses are validated
and buffered before writing, with a 512 KiB serialized limit; exact unit names
and notes are never trimmed or clipped. Invalid port output fails closed with
`internal_error`. Shape and body limits reject input before any domain call.

## r3 projection

Order-list headers remain unchanged: neither confirmed identity nor capabilities
are added to the list. Detail and successful action responses preserve immutable
`confirmed.id` and include a nonnull `permitted_actions` array. The adapter copies
all server-provided codes in their original order, without sorting, filtering or
reconstructing eligibility. An empty array is valid; nil, duplicate, unknown or
empty codes are invalid port output and fail closed with `internal_error`.
Only the seven action codes listed above are supported.

Capabilities are response hints, not authorization. An empty or different hint
set does not short-circuit an action request: every POST still reaches the port
under the verified subject and current domain authorization/version policy.
There is no adapter replay cache. An invalid POST result is not proof that a
previous domain attempt did not commit; uncertain outcomes retain the exact
idempotency key and body.

Money stays unchanged: nonnegative int64 minor amounts are canonical decimal
JSON strings, currency is exactly three uppercase ASCII letters, and exponent
is an integer from 0 through 6. Confirmed contents, unit links, list fields,
request bodies, error envelopes and authentication policy are not widened.

Existing test sources use a fake port, actual synthetic Ed25519/JWKS verification, and public
HTTP requests. They do not prove persistence, natural-language interpretation,
live delivery, BFF registration, or end-to-end feature acceptance. Shared server
registration and connected domain integration are owned by the parent task.
The capability test source is preserved, but was not executed for this update.
Repeated fake-port POST forwarding is not durable idempotency/persistence proof.

The standard commands below require separate applicable execution authority;
they were **not run** for this source-only update and do not authorize restarting
any stopped proof. From `backend`, use the task's private temporary/build
environment and existing public module cache:

```sh
go test -mod=readonly -count=1 -timeout=120s ./internal/stafforders
go test -mod=readonly -race -count=1 -timeout=180s ./internal/stafforders
go vet -mod=readonly ./internal/stafforders
```
