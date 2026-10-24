# Local durable store

This is the synthetic ingestion foundation, not the restaurant order schema or a
verified Meta integration. Its records belong in a disposable, dedicated local
PostgreSQL database. Never use production data or credentials here.

## Database boundary

`postgres.Open(ctx, dsn)` does not connect eagerly or migrate. Both `Open` and
`Migrate` require a numeric loopback host and a database name beginning with
`restaurant_foundation_`, with a nonempty suffix. Parsed fallback hosts must also
be numeric loopback addresses. Empty configuration is rejected instead of
silently using the developer's default PostgreSQL database.

`postgres.Migrate(ctx, dsn)` is an explicit setup operation. It embeds the Goose
SQL migrations, uses a separate short-lived `database/sql` connection, and keeps
migration history at `app.goose_db_version`. API and worker startup must not
apply migrations implicitly. There are no destructive down migrations.

The foundation tables are `app.conversation_sequences`, `app.inbound_events`,
`app.jobs`, `app.outbox`, and `app.send_attempts`. They store fake-provider events and effects, not orders,
auth identities, catalog prices, or customer payments. The product's planned
schema remains in `../../docs/product/postgres-schema.md`.

## Atomic operations

- `Accept` validates the event and commits the inbound record and one job
  together. The provider event ID is unique. An identical retry is a no-op;
  reusing the identity with different sender/text returns `ErrConflict`.
  Text is preserved exactly rather than trimmed or normalized. A transactional
  per-sender counter records arrival order independently of clocks or UUIDs;
  replay/conflict rollback also rolls back any counter allocation.
- `ClaimJob` claims eligible queued work or reclaims expired processing work
  with `FOR UPDATE SKIP LOCKED`, incrementing its generation and attempt count.
  Earlier queued/leased work for the same sender blocks its successor; other
  senders can progress independently.
- `CompleteJob` checks the current, unexpired lease and atomically marks the job
  complete and inserts one logical outbox effect. Retrying or failing work
  likewise requires its current lease; delayed retries retain their due time.
- `ClaimDelivery` records send intent and an attempt before the caller performs
  HTTP, outside the transaction. An expired send lease becomes `unknown` rather
  than being returned for another send. Earlier pending/sending/unknown sends
  block later sends to that sender. Accepted or definitely failed predecessors
  allow subsequent sends; no unknown send is skipped to send its successor.
- `FinishDelivery` distinguishes `accepted`, `unknown`, and `failed`. Acceptance
  requires a provider message ID. No state here proves exactly-once external
  delivery. Unknown outcomes need reconciliation, which this slice does not
  provide; there is no automatic resend or manual reconciliation command.
- `NextDue` exposes eligible persisted retry/lease deadlines, excluding blocked
  successors so a past-due but ineligible record cannot cause hot idle polling. `Inspect` returns a small
  diagnostic snapshot for a synthetic event, not raw database rows or secrets.

## Idle behavior and limits

The pool has four maximum connections and zero minimum/idle-minimum connections.
It reaps idle connections after approximately one second. Its positive one-second
health ticker performs local resource housekeeping, not periodic SQL pings.
Setting `HealthCheckPeriod` to zero is **not** supported by the pinned pgx version:
its ticker would panic. Acquisition can check an idle connection during real
work; this is not a promise that active operations make no health query.

Store operations have five-second context deadlines and PostgreSQL statements
have a five-second timeout. A migration connection closes when setup returns.
Local idle behavior does not establish a remote Neon free-tier or suspension
guarantee. Actual startup/hint/timer/recovery scheduling still requires the worker
integration; store-level ordering tests alone are not end-to-end proof.

## Tooling and checks

Pinned tooling: Go 1.27.3, pgx/v5 v5.11.0, Goose v3.29.0, sqlc v1.31.1.
Keep Go caches inside the ignored backend cache directory:

```sh
cd backend
export GOTOOLCHAIN=local GOCACHE="$PWD/.cache/build"
export GOPATH="$PWD/.cache/gopath" GOMODCACHE="$PWD/.cache/modules"
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
go test -count=1 -timeout=120s ./internal/postgres ./internal/foundation
```

Tests require `initdb`, `pg_ctl`, and `createdb`. They create and stop their own
throwaway PostgreSQL clusters; a missing prerequisite fails rather than skips.
The command above is a focused store check, not evidence that an API, worker,
HTTP seam, independent verification, or real provider integration passed.
