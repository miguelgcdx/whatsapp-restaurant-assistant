# Local conversation control

This native Go foundation persists **explicit staff control** of an existing
`local_fixture` conversation. It is not wired to HTTP, a model, workers, orders,
messages, or dispatch. Reading `ai` is **not permission to invoke a model**.
Takeover does not yet invalidate unsent AI intents or recall in-flight work;
status notifications and exact staff-message delivery remain separate future
integration work. No customer identity or history is added here.

## Explicit setup and API

Apply the existing explicit local migrator, including migration 9. Provision
restaurant configuration, staff membership and fictional conversation context
through their existing operator/setup APIs. The control store does not perform
any of those steps, seed a mode, or automatically return control.

```go
store, err := conversation.OpenStore(ctx, localDSN, restaurantID, verifier)
// Check err, then defer store.Close(). verifier is a concrete *staff.Verifier.
id := local.ConversationID() // Existing cart.LocalContext, not customer input.
initial, err := store.Initialize(ctx, rawJWT, id, conversation.ModeAI, "init-1")
// Check err before using initial.Version.
taken, err := store.SetMode(ctx, rawJWT, id, conversation.ModeStaff,
    initial.Version, "takeover-1")
// Check err. Returning control is another explicit SetMode(...ModeAI...).
_ = taken
sample, err := store.Read(ctx, rawJWT, id)
// Check err. sample is staff-visible sampled state, never an authority permit.
_ = sample
```

`OpenStore` is lazy and uses the unchanged local PostgreSQL DSN guard. It requires
an explicit canonical restaurant UUID, nonnil context and verifier. `Initialize`,
`SetMode`, and `Read` each take `context.Context` first. Tokens are **raw JWTs**,
not `Bearer ...` headers. The existing verifier authenticates EdDSA/Ed25519 with
its configured local issuer, audience and JWKS source; caller-supplied subjects
and membership booleans are not accepted.

The only state fields are typed `cart.ConversationID`, `Mode` (`ai` or `staff`),
and nonnegative `int64 Version`. Every error returns `State{}`. A missing control
row fails closed on `Read` and `SetMode`; only authorized `Initialize` creates
version zero. A new initialization operation cannot reset an existing row.

## CAS and operation identity

Retain one exact key per intended mutation: 1–128 ASCII characters from
`A-Z a-z 0-9 . _ : -`, with no trimming or normalization. The namespace is
`(restaurant, verified staff subject, key)` across **both actions and every
conversation**. Intent includes action, conversation, target mode and expected
version (absent only for initialization).

- A fresh `SetMode` requires the current expected version and increments once,
  including assignment of the already-current mode.
- An identical key and complete intent returns its exact historical committed
  state, even after later transitions. It neither reapplies nor returns current
  control. Use `Read` for current sampled state.
- Reusing a key with different intent conflicts. A different verified actor has
  an independent namespace. Rejected operations never reserve a key.
- Stale expected versions and exhausted `MaxInt64` versions return `ErrStale`
  without overflow, row changes or ledger entries.

## Transaction and authentication boundaries

Each operation has one cooperative five-second context cap, including JWT
verification, respecting earlier deadlines/cancellation. Error normalization
honors an observable explicit cancellation first, then the context's actual
elapsed deadline even if `Done`/`Err` publication lags. A dependency timeout with
a still-live caller deadline is not by itself a caller deadline failure. Entry,
pre-write and pre-commit checks also reject elapsed deadlines without waiting or
padding the budget. Authentication runs
**before** opening a transaction, so no JWKS/network fetch happens under row
locks. There is no stronger claim that token expiry is revalidated at commit.

The normal PostgreSQL transaction locks, in order:

1. Existing restaurant (`FOR UPDATE` for mutations, `FOR SHARE` for reads).
2. Current membership (`FOR SHARE`), checked active **after** any restaurant wait.
3. Existing matching restaurant/conversation with `source='local_fixture'`.
4. Control and operation rows.

Membership remains locked until commit, including reads and historical replays.
A revocation committed while the operation waits for the restaurant is observed
and rejects the operation. A revocation blocked behind the held membership lock
may legitimately commit after the authorized operation. Restaurant serialization
is intentionally coarse and reuses the existing local mutation order; it is not
a distributed execution lease. Reads use ordinary transactions, not PostgreSQL
`READ ONLY` transactions containing incompatible row locks.

Control and structured immutable operation result insert commit together.
Timestamps are explicit database values normalized to UTC, not claim authority
clocks. Rollback cleanup has its own independent one-second cap; there are no
watchdog goroutines. As with any database connection loss at commit, a caller
must not infer absence of a commit from a transport failure: retry the **same
intent and key** to resolve an uncertain outcome, never synthesize another key.

Migration 9 adds only `app.conversation_controls` and
`app.conversation_control_operations`, with matching-context/membership foreign
keys and checked action/mode/key/version/result shapes. It seeds nothing, has no
cascade/TTL/default mode and changes no old schema or records. Its Down removes
only these two new tables, in dependency order.

## Errors and local verification

Stable domain errors are `ErrInput`, `ErrNotFound`, `ErrForbidden`, `ErrConflict`,
`ErrStale`, `ErrStoredData`, and `ErrUnavailable`. Safe concrete
`staff.ErrUnauthenticated`, `staff.ErrUnavailable`, `context.Canceled` and
`context.DeadlineExceeded` identities are preserved. No raw JWT, operation key,
SQL, driver body or DSN is returned or logged by this package. There is no
customer-facing fallback on failure.

Focused tests use actual `staff.NewVerifier`, fabricated Ed25519 JWTs and an
owned loopback JWKS server, plus owned `testpg.Start` clusters (missing
prerequisites fail, not skip). Public reads and replays establish behavior;
ledger counts and positive `pg_stat_activity` lock observations supplement them.
The migration test builds a populated v8 database from embedded original
migrations, checks upgrade preservation and rejects structurally invalid SQL.

Run package tests, race tests and vet for `./internal/conversation`, plus selected
`^TestConversationControlMigration` normal/race tests and `./internal/postgres`
vet, using the project's prepared offline environment. No existing CLI changed,
so CLI differential verification is not applicable. Broader backend and
independent security/concurrency verification belong to the parent delivery
pass, not a claim that this prerequisite implements the future product gate.
