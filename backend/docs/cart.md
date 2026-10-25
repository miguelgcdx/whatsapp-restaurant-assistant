# Local fixture cart core

`internal/cart` persists a single draft per explicitly provisioned **local fixture
conversation**. It has native Go interfaces only: no HTTP/customer endpoint,
WhatsApp/model adapter, order submission, receipts, dashboard, or new CLI.
A subtotal is not a final order total.

The additive [local conversation-to-cart execution bridge](cart-execution.md)
binds already persisted synthetic events and actual job claims to explicitly
provisioned local contexts. It offers immutable server-owned call plans and
same-transaction authority guards, not automatic worker dispatch or customer
identity. Direct `Create`, `Apply`, `Read` and failed-key reuse below retain their
existing semantics. Migration 6 adds two empty plan tables; these are separate
from committed mutation results. See the bridge guide for API/errors, trusted-clock
linearization, retry behavior and the new focused proof boundary.

## Ownership and setup

Apply the existing explicit `postgres.Migrate` to an owned loopback foundation
database. Migration `00005_local_cart.sql` adds four empty tables:

- `app.cart_conversations`: restaurant-owned `local_fixture` business/sender context;
- `app.carts`: one draft per conversation, currency/exponent snapshot and version;
- `app.cart_units`: stable unit IDs, exact notes, stored catalog prices and ordering;
- `app.cart_operations`: complete committed request fingerprints and results,
  separate from the existing staff `app.operations` ledger.

Composite foreign keys prevent cross-restaurant/conversation/cart/menu links.
UUIDs and timestamps are supplied explicitly by the component; there are no
fixture defaults, automatic setup, TTL, cleanup, or cascading deletion policies.
Old migrations, synthetic foundation senders, and staff/auth tables are unchanged.

Open with `cart.OpenStore(ctx, localDSN, restaurantID)` after explicit catalog setup.
It reuses the existing guarded local pool configuration; opening never migrates or
seeds. Close the store when its owning application shuts down.
`ProvisionLocal(ctx, business, sender)` explicitly allocates a fictional/local
conversation and returns opaque `LocalContext`. `LoadLocal(ctx, conversationID,
business, sender)` restores that context after reopening. Both require existing
restaurant configuration/ownership; neither provisions customer authentication.

**These strings, UUID parsers and opaque contexts are not verified customer or
WhatsApp identity.** Only local application/fixture code should supply them.
Foundation `Event.Sender` is not cart authority. Business/sender fixture identifiers
must be nonblank, valid UTF-8, NUL-free and at most 256 Unicode code points; this
is not validation of any external phone/provider identity format. Every read,
creation, edit and cached replay rechecks stored restaurant/conversation/source/business/sender.
No provider/auth calls run inside a cart transaction.

## Public Go workflow

IDs are distinct opaque `CartID`, `ConversationID`, `MenuID`, `UnitID` and
`OperationID` values. Use the corresponding `Parse*ID` for canonical lowercase
UUID boundary parsing. Parsing proves syntax only, not ownership. `ParseVersion`
accepts nonnegative int64 versions; `Version.Int64()` exposes the observed value.

1. Allocate `NewOperationID()` in application/server code and retain it for retries.
   `ParseOperationID` restores that server-issued identity; it does not introduce
   a model-owned nonce or a public idempotency protocol.
2. `Create(ctx, local, operationID)` explicitly creates the draft at version zero.
   Concurrent creates converge on one cart. A distinct creation identity returns
   the existing draft; an exact replay returns that creation's original result.
   `Read(ctx, local, cartID)` never invents an empty draft.
3. Build a closed action with `Add(menuID, positiveCount)`, `Remove(unitIDs)` or
   `SetNote(unitIDs, exactNote)`. Target lists must be nonempty, unique and owned
   by this draft. New units have server-generated IDs, never caller prices/IDs.
4. `Apply(ctx, local, operationID, cartID, expectedVersion, action)` returns a
   detached `Result` containing the operation ID and complete `Snapshot`.
   Each accepted edit increments the version exactly once, including an accepted
   same-note intent. Increases allocate new units; decreasing/removing names exact
   unit IDs, so a noted unit is never arbitrarily dropped.

Unit notes are exact UTF-8, at most 500 Unicode code points, including whitespace
and empty notes. NUL is rejected at this persistence boundary because PostgreSQL
text cannot store it. There is no trimming/truncation or price adjustment. The
old pure pricing validator remains unchanged (including its separate text rules).
All target validation is whole-request: missing/foreign/duplicate IDs reject the
entire request, not just the bad target.

Snapshots include currency, exponent, int64 subtotal and per-unit price snapshots.
They contain **no selected fulfillment, fee, payment, or final total**. An empty
draft has subtotal zero. Existing units remain intact and editable when a menu
item later becomes unavailable. Adding uses the current available stored menu
row and current settings, locked/read in the same transaction. Reading/editing
other units never reprices from the current catalog. Future final-submission
repricing and contextual confirmation are not implemented.

## Concurrency, retry and errors

Lock order is restaurant → stored local conversation → cart → added menu/current
settings. Creation locks the existing conversation before a cart exists. The
added menu row lock is essential: legacy `catalog.SetAvailability` does not lock
the parent restaurant. This makes both legacy and staff availability writers
coherent with add, without changing either existing writer.

After fresh ownership validation, the component looks up operation identity and
canonical action/body **before** comparing the expected version. The fingerprint
includes action, expected version, cart/menu IDs, count, sorted target set and
exact note. Target order is not meaningful; note bytes are. The same identity/body
returns the original result/allocated IDs after later edits. Any changed body or
action conflicts. Distinct operations at the same version have one winner.

Domain changes, CAS and a complete validated result ledger insert share one
transaction. There are no pending claims, automatic retries or outbound effects.
On an ambiguous connection/commit failure, retain the identity and exact intent;
retrying it resolves a committed result rather than allocating another edit.
Cancellation/failed validation rolls back both domain changes and result claim.

Exported sentinel errors (use `errors.Is`):

| Error | Meaning |
| --- | --- |
| `ErrInput` | malformed/zero ID or context, negative version, bad count/note/targets, missing action |
| `ErrNotFound` | unknown/foreign stored context, cart, menu or target (no foreign-scope disclosure) |
| `ErrConflict` | retained operation identity has a different canonical intent |
| `ErrStale` | version differs or cannot increment past int64 maximum |
| `ErrUnavailableItem` | current added menu item is unavailable |
| `ErrLimit` | checked arithmetic or complete-result resource budget exceeded |
| `ErrStoredData` | invalid stored snapshot/result; never return fabricated partial data |
| `ErrUnavailable` | cancellation or local database/randomness failure, with no driver details |

Reads and complete saved results have a **512 KiB resource safety budget**. This
is not a product quantity cap. Cardinality, a minimum encoded-size bound and
checked arithmetic reject huge counts (including free items) before count-sized
allocation/UUID generation. The complete encoding is then checked before writes
or commit. Oversized reads reject; they are never silently clipped. Stored results
are parsed, scalar-validated, priced from their snapshots and canonically
round-tripped before replay. No database credential-bearing errors are returned.

## Evidence boundary

Tests use exported component interfaces and fresh owned PostgreSQL clusters,
not inherited DSNs. Fixture SQL supplements public reads/replay/key reuse and
foundation `Inspect` with row/counter and owned lock-wait observations; those SQL
observations are not claimed as public ledger endpoints.

`internal/cart` covers reopen/persistence, notes and stable unit identity, empty
subtotal, scoped and malformed input, huge free counts, arithmetic and encoded
result limits, whole rejection/key reuse, concurrent create/edit/replay, fresh
ownership and corrupt committed results. `internal/integration/cart_test.go`
observes actual PostgreSQL waits for both availability writer paths in both
orders, and cancels a transaction blocked at result insertion after domain writes
to check rollback and key reuse. No sleeps or elapsed-time ordering assertions
are used.

`internal/postgres/cart_migration_test.go` uses Goose's public provider and an
embedded v1–4 fixture FS to install pre-cart schema, then populates existing
foundation/catalog/member/staff-result stores through public APIs **before**
calling public current `Migrate`. It compares exported snapshots/settings/menu,
membership access, original staff replay and subsequent key use; SQL hashes also
check exact old records/counters. All four new cart tables must be empty after
upgrade. Tests do not read Git history or add product setup flags.

Focused checks are `go test -count=1 -timeout=20s ./internal/cart`, corresponding
pricing/postgres checks, and `go test -count=1 -timeout=20s ./internal/integration
-run TestLocalCart`. The initial missing API RED checks preceded implementation;
additional preservation/upgrade/ordering/resource probes supplement that baseline.
The initial subtotal/cart API checks observed RED before implementation. Supplemental
upgrade, availability-ordering, resource and live-cancellation probes were added after
implementation and observed GREEN only. Their missing pre-implementation RED history is
retained explicitly, not reconstructed by artificially breaking working code.

## Observed results and local dispositions

The writer observed all four focused packages and focused vet passing. The parent then
ran the complete backend with prepared offline caches: normal tests, vet and race checks
all exited 0 in 32 seconds, including existing staff/catalog/foundation/worker packages.
Race timings included cart 10.896s, integration 13.316s and PostgreSQL 12.461s. These are
**parent runtime results**, not independent executions. The 12 new top-level test
declarations do not establish a separate independent execution count.

The final independent static assessment found no new candidate-caused security or
concurrency blocker. Its setup timed out at the explicit 30-second foreground limit
while copying prepared caches, **before any Go version, test, race, build, PostgreSQL or
migration command ran**. Independent runtime executions were **zero**; the authorized
focused test command was unexecuted, not an application-test failure. No retry, repair,
probe, installation or cleanup followed. Actual independent toolchain version was not
verified.

Independent before/after comparisons matched 182 original source files, porcelain Git
status, the staged-entry listing and 153,837 inventoried Go dependency entries. The latter
covered `go.mod`, `go.sum`, the module cache and GOPATH, not original build-cache bytes or
all outside filesystem paths. Staged-entry equality is not a raw Git-index byte hash.
A post-timeout partial-copy link inventory was not a completed pre-execution isolation
certificate. These integrity facts do not supply the missing runtime proof.

The human explicitly accepted two **LOCAL-only** dispositions: the historical RED gap,
then independent static review plus parent runtime results despite missing independent
runtime proof. **S4 / overall independent verification remains PARTIAL.** Neither
acceptance is clean per-rule TDD compliance, independent runtime approval, customer
authentication, live-provider/production readiness, publication permission or a future
waiver. No new verification run, cache-copy retry, installation or scratch cleanup was
used for closure. See `odd/tasks/local-cart-core.md` for the exact choices and evidence.
The existing scratch copy remains untouched; removing it requires separate authorization.
