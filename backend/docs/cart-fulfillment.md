# Local draft fulfillment and quotes

`internal/cart` adds explicit pickup/delivery selection and server-calculated quotes
for fictional local drafts. This is a **native Go seam only**: no CLI, HTTP tool,
text parsing, automatic worker dispatch, customer authentication, confirmation,
order, payment collection, staff UI or delivery assignment is added.

## Quick path

Apply public `postgres.Migrate` explicitly to an owned local database; configure
its catalog and provision/load a local context as in [cart.md](cart.md). Persist a
synthetic event and obtain its actual public `ClaimJob` result as in
[cart-execution.md](cart-execution.md). Opening a store performs none of that setup.

Application code can then use these operations (check every error in real code):

```go
// snapshot is a current legacy subtotal Snapshot. Event/context/slot/claim are
// trusted runtime inputs, not fields supplied by a model or customer.
choice, err := cart.DeliveryFulfillment(" exact fictional address\n", "fictional phone")
// Alternatively: choice := cart.PickupFulfillment()
call, err := cart.SetFulfillmentCall(snapshot.ID, snapshot.Version, choice)
plan, err := store.RegisterExecution(ctx, local, eventID, nextSlot, call)
selected, err := store.ExecuteDraft(ctx, local, plan, job)

call, err = cart.QuoteCall(selected.Cart.ID)
plan, err = store.RegisterExecution(ctx, local, eventID, anotherSlot, call)
fresh, err := store.ExecuteDraft(ctx, local, plan, job)
```

This is an API sketch, not a runnable command or error-handling example. Slot
identity is immutable: use a new slot for a different intent, not for a retry.
Registering the same intent restores the original operation UUID after reopen or
job reclaim. Registration is setup, not a mutation result reservation.

## Selection, money and versions

| Surface | Contract |
| --- | --- |
| `Fulfillment` | Closed immutable value; zero invalid; `Kind()`, `Address()` and `RecipientPhone()` expose exact values |
| Pickup | No address/phone; derived `pay_at_pickup`; applied delivery fee zero |
| Delivery | Nonblank exact UTF-8 address ≤1,000 and phone ≤128 Unicode code points; no NUL; derived `cash_on_delivery` |
| `DraftResult` | Operation ID, unchanged subtotal `Cart` snapshot, selected `Fulfillment`, complete `Quote` |
| `DraftQuote` | Currency/exponent, subtotal, separate fee and total, derived payment method, settings version and configured delivery-fee basis |
| Set fulfillment | CAS on the same cart version as unit edits; accepted same-selection/new-operation also increments once |
| Quote | Fresh read; no version increment or mutation-ledger entry |
| No selection | `ErrFulfillmentRequired`, zero business result, no writes; never implicit pickup |

Constructors preserve text; they do not normalize phone numbers, establish phone
ownership, or imply geographic coverage. Native fields are not a public HTTP/model
JSON contract. Private deterministic `draft-v1` DTOs encode exactly one fulfillment
variant: pickup has only `kind` and derived `payment_method`; delivery additionally
has `address` and `recipient_phone`. Caller-supplied prices/payment are not accepted.

Quotes use **persisted unit price snapshots**, not current menu prices or
availability. Unit IDs, order and exact unpriced notes survive selection switching,
reopen and direct unit edits. Empty selected drafts retain pure-pricing behavior:
pickup total zero, delivery total its configured fee. This does not authorize an
empty order. Checked arithmetic and the complete 512 KiB result budget reject
unsafe totals/results without partial writes or silently dropped units.

Cart version identifies units plus selection. Settings version and configured
flat-fee basis separately identify the sampled quote configuration. Settings are
read and locked in the cart transaction, never via a prior pool lookup. No fee
editor or final-confirmation freshness policy is implemented.

## Compatibility and retries

Migration `00007_local_cart_fulfillment.sql` adds one initially empty scoped
`cart_fulfillments` side table. Row absence means unselected; pickup uses NULL
delivery columns, delivery uses validated exact nonblank fields. Composite foreign
keys retain restaurant/conversation/cart scope. The migration only widens the
existing operation/call action checks; migrations 1–6 remain unchanged. There are
no defaults, pickup backfills, seeds, fee/payment state, cascading deletion, TTL
or cleanup policies.

Legacy request/result bytes and SHA-256 intent fingerprints remain unchanged.
`Create`, `Apply`, `Read`, `Snapshot` and `Result` stay subtotal-only and preserve
selection. A fresh distinct `Create` observes the shared current cart version;
an old-version direct edit becomes stale after fulfillment selection.

`Execute` rejects rich calls with `ErrInput`; `ExecuteDraft` rejects legacy calls
with `ErrInput`. Both return zero business output before domain effects. Their
shared plan/job loader reuses whole-event registration and actual claim authority.
The additive [immutable LOCAL preview seam](cart-summary-preview.md) captures
stored unit names/notes, selection and quote through a third result seam using the
same transaction-local quote helper. Old codecs and fresh-quote behavior are
preserved. Both old seams reject preview descriptors with `ErrInput`/zero output;
preview execution rejects old descriptors. Migration 8 is additive; the final
historical evidence below is not proof of the new preview candidate. No real
workload, presentation, confirmation or submission is implied.

Fulfillment mutations save a complete historical rich result in the **same**
`cart_operations` identity namespace as old direct/bound mutations. Exact replay
returns its original units, selection, quote and fee basis even after later unit,
selection or settings changes. Reusing that operation ID for another action/cart/
version/selection conflicts in either legacy/rich direction. Fresh quotes sample
the current basis, not the historical mutation quote.

The lock order remains restaurant → local conversation → source/call → actual
job/event → cart → settings/selection resources. The job/cart/settings locks are
held through the guarded outcome. Authority is checked before cached output,
after blocking resource acquisition before writes/read release, and after the
complete-result insert before commit. A separate trusted database clock statement
is sampled after blocking work. Expiry/cancellation rolls back domain and ledger
changes. This is a guarded linearization point, not a wall-clock promise at
physical commit/network delivery. Retain the exact plan after an ambiguous commit
failure and retry with a current actual claim; do not invent a replacement intent.

Existing [cart errors](cart.md#concurrency-retry-and-errors) and
[execution authority errors](cart-execution.md#results-retries-and-errors) apply.
`ErrStoredData` covers malformed selection, settings, canonical plans and complete
results; `ErrLimit` covers live total/resource overflow. All failures return zero
business results. Legacy subtotal reads remain available while unselected.

## Focused verification

Tests use existing owned system-temp PostgreSQL fixtures, never inherited DSNs.
Public reads, replay and foundation inspection are supplemented with exact fixture
hashes/counters, positive `pg_stat_activity` lock observations and trusted-clock
expiry barriers. There are no production test-clock hooks or sleep-based barriers.

From `backend`, with prepared caches and installed Go/PostgreSQL prerequisites:

```sh
export GOTOOLCHAIN=local GOCACHE=$PWD/.cache/build GOPATH=$PWD/.cache/gopath
export GOMODCACHE=$PWD/.cache/modules GOFLAGS=-mod=readonly GOPROXY=off
export GOSUMDB=off GOTELEMETRY=off GOWORK=off

go test -count=1 -timeout=20s ./internal/cart -run '^TestDraft(FulfillmentValidation|SelectionQuoteReplayAndLegacyPreservation)$'
go test -count=1 -timeout=20s ./internal/cart -run '^TestDraftEmptyMoneyAndStoredValidation$'
go test -count=1 -timeout=20s ./internal/cart -run '^TestDraft(ClaimBeforeCacheAndCAS|BlockingAuthority)$'
go test -count=1 -timeout=20s ./internal/cart -run '^TestDraftSchemaRejectsUnicodeBlankSelection$'
go test -count=1 -timeout=20s ./internal/postgres -run '^TestCartFulfillmentMigrationPreservesPopulatedV6$'
go test -count=1 -timeout=20s ./internal/cart -run '^TestIndependentDraft(FreshSettings|RichVariantTampering)$'
```

The initial draft behavior tests observed **compile RED** on absent rich APIs
before production implementation; this is not per-case runtime RED. The populated
v6 test observed **runtime RED** on the missing side table. It hashes all historical
records (including plan/result bytes and fingerprints), then checks public legacy
readback, re-registration and replay after current public `Migrate`.

A subsequent schema-specific runtime RED found C-locale POSIX whitespace did not
reject a Unicode-only blank address. An explicit Unicode White_Space check fixed
it; exact address/phone codepoint endpoint persistence is also checked. These are
focused writer observations; final parent and independent results are recorded below.

## Final local evidence

The full-suite commands below are historical execution evidence. For current
verification commands and budgets, see the [backend README](../README.md#verify-the-complete-local-loop).

Parent full checks on `go1.27.3-X:nodwarf5 linux/amd64`, using the offline
configuration above, each exited **0** in one 87-second run:

```sh
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=120s ./...
```

Cart normal/race timings were 37.484s/44.871s; integration 12.105s/15.865s and
PostgreSQL 11.644s/15.421s. These are parent results, separately attributed from
independent executions.

The final independent assessment **passed the native LOCAL scope**, with no
reproduced blocker or advisory. Its source-only scratch setup inventoried only
used dependencies (`go list -deps -test -json ./internal/cart ./internal/postgres`),
not the full module graph. It independently ran validation/selection/Unicode,
empty/corrupt/resource, populated-v6 upgrade, claim/CAS/blocking and targeted race
checks, all exit 0. Selected legacy public tests passed against both baseline and
candidate; timing text differed, not behavioral output. Captured test stdout
contained runner results; stderr was empty.

Two additional independently authored probes verify that the same quote plan
samples a new fee/settings version while historical mutation replay stays exact,
and that ten variant/money tamper cases reject with zero results and unchanged
state. Their source is retained as `fulfillment_independent_test.go`; a parent
focused execution after adoption also passed, exit 0, 1.520s. No production code
changed after the full run or independent assessment. These are supplemental
GREEN probes, not reconstructed pre-implementation RED.

Before/after SHA-256 matched for 104 selected original source files and 329 selected
dependency inputs (316 Go files, 11 module files, `go.mod`, `go.sum`); original Git
status matched. These bounded claims exclude the compiler cache, standard
toolchain, unused dependencies and other filesystem paths. Scratch was retained;
no cleanup, installation or network access was performed.

This PASS does not establish production authentication, customer/provider identity,
confirmation, order submission, payment collection, shared delivery, staff UI or
live integration. Earlier features' PARTIAL proof records remain unchanged. Exact
history and work-unit commits are in `odd/tasks/local-draft-fulfillment.md`.
