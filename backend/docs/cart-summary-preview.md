# Immutable LOCAL draft previews

Capture a complete exact draft preview from persisted cart, fulfillment, settings
and catalog state, then retrieve that same snapshot later. This native Go seam is
**not complete `prepare_summary`**, an order candidate, a confirmation, or evidence
that a summary was sent or presented. It makes no submission eligibility decision.
An empty selected cart can have a local preview; that does not permit an empty order.

## Quick path

Explicitly migrate an **owned local fixture database** with `postgres.Migrate`,
configure its fictional catalog, and provision/load its opaque `LocalContext` as in
[cart.md](cart.md). Persist a synthetic event, obtain its actual `ClaimJob` result,
and register slots as in [cart-execution.md](cart-execution.md). Select fulfillment
as in [cart-fulfillment.md](cart-fulfillment.md). Opening a store does none of this.

The following native function illustrates checked capture and read. Its arguments
come from trusted local application setup, not model/customer/provider authority;
`nextSlot` and `readSlot` must be distinct unused nonnegative runtime slots.

```go
func localPreview(ctx context.Context, store *cart.Store, local cart.LocalContext,
    eventID string, nextSlot, readSlot int64, job foundation.Job,
    snapshot cart.Snapshot) (cart.SummaryPreviewResult, error) {
    call, err := cart.CaptureSummaryPreviewCall(snapshot.ID, snapshot.Version)
    if err != nil {
        return cart.SummaryPreviewResult{}, err
    }
    plan, err := store.RegisterExecution(ctx, local, eventID, nextSlot, call)
    if err != nil {
        return cart.SummaryPreviewResult{}, err
    }
    captured, err := store.ExecuteSummaryPreview(ctx, local, plan, job)
    if err != nil {
        return cart.SummaryPreviewResult{}, err
    }
    read, err := cart.ReadSummaryPreviewCall(captured.Preview.ID)
    if err != nil {
        return cart.SummaryPreviewResult{}, err
    }
    readPlan, err := store.RegisterExecution(ctx, local, eventID, readSlot, read)
    if err != nil {
        return cart.SummaryPreviewResult{}, err
    }
    return store.ExecuteSummaryPreview(ctx, local, readPlan, job)
}
```

This is an API example, not a CLI or standalone program; it needs the usual
`context`, internal `cart` and `foundation` imports inside this backend module.
The read result's `OperationID` identifies the read slot; its
`Preview.SourceOperationID` still identifies the original capture. Read and exact
capture replay do not reserve another mutation result or increment cart version.
To refresh, register a **new capture slot** with the current cart version. Never
replace an old slot's intent. No API accepts a caller-selected preview ID for
capture, a `DraftResult`, names, prices, total, actor, estimate, workload, or clock.

## Complete result and immutable copies

| Field | Meaning |
| --- | --- |
| `SummaryPreviewID` | Opaque typed canonical UUID; parse/string/text/JSON support proves syntax, not ownership |
| `SourceOperationID` | Original registered capture identity in the existing `cart_operations` namespace |
| `CartID`, `ConversationID`, `SourceCartVersion` | Scoped source identity and version, captured without increment |
| `CapturedAt` | Trusted UTC database capture time sampled after blocking resource locks; **not workload as-of** |
| `Units`, `UnitCount` | Stable unit/menu IDs in cart order, one unit per entry, exact raw name/note and stored unit money |
| `Currency`, `Exponent`, `Quote` | Checked integer subtotal, separate fee/total, derived payment, settings version/configured fee basis |
| `Fulfillment` | Explicit pickup or exact delivery address/phone; no default pickup |
| `NumericEstimate` | Always `nil`; canonical storage encodes `null`, never an invented zero |
| `EstimateReason` | Typed `WorkloadNotConnected`, exactly `workload_not_connected` |
| `Text` | Exact deterministic server-generated English LOCAL text, not a model-rewritten total |

Names come from scoped persisted catalog rows locked in deterministic menu-ID
order on **new capture only**. Name validity is exact nonblank UTF-8 without NUL,
at most 200 Unicode code points. Notes retain their existing exact 500-codepoint
bound and are unpriced. Changing today's menu price or availability does not drop
or reprice an existing cart unit. Returned values are detached; modifying them
cannot alter storage or authorize another capture.

Migration `00008_local_summary_preview.sql` leaves migrations 1–7 unchanged and
adds one initially empty `app.cart_summary_previews` table. It has explicit server
UUID, restaurant/conversation/cart scope, source version/registered operation,
capture timestamp and complete canonical body text. Composite foreign keys bind
both scoped cart and scoped registered source call; one capture operation has one
preview. Body and complete operation-result envelope must each fit **512 KiB**.
There are no unit foreign keys (units may later be removed), defaults, seeds,
cascades, TTL/cleanup, current-preview pointers or independent operation ledger.
Only the existing action checks are widened and a scoped source uniqueness key
added. The application never updates a preview row. This is not a sandbox against
privileged arbitrary database writers.

Capture atomically writes preview plus complete ledger result. Read/replay validate
canonical bytes, unit identities/count, selection, money, name/note bounds, exact
text, null estimate/reason, row metadata and originating registered canonical intent
and fingerprint. Both stored preview/ledger copies must agree. They never reload
current menu/settings/cart units to rebuild historical output. Reopen, reclaim,
cart edits/removal and settings/name changes preserve old IDs, capture time, names,
prices, notes, fee and text. There is no inferred current or presented preview.

## Exact text layout

No timestamps or IDs appear in text. Equivalent captured state therefore produces
equivalent text. There is **no trailing newline** after the closing question.
Each unit is ungrouped and has quantity 1. Names, notes, address and phone use
Go `strconv.Quote` escaping; embedded newlines cannot impersonate summary lines.
Raw structured fields are not trimmed, normalized, clipped or presentation-escaped.
Money is `CURRENCY decimal`, using the configured exponent and integer arithmetic,
with no floats, grouping or assumed two-decimal currency convention.

For two USD 6.50 units, one with note `no salt`, pickup is exactly:

```text
Local draft preview (not submitted)
Items:
- "Fictional sandwich" | quantity: 1 | price: USD 6.50 | note: "no salt"
- "Fictional sandwich" | quantity: 1 | price: USD 6.50 | note: ""
Fulfillment: pickup
Payment method: pay_at_pickup
Subtotal: USD 13.00
Delivery fee: USD 0.00
Total: USD 13.00
Preparation estimate: unavailable (workload not connected).
Would you like anything else?
```

Delivery replaces the fulfillment/payment/totals block with:

```text
Fulfillment: delivery
Delivery address: "fictional address"
Recipient phone: "fictional phone"
Payment method: cash_on_delivery
Subtotal: USD 13.00
Delivery fee: USD 3.00
Total: USD 16.00
```

An empty pickup preview is exactly:

```text
Local draft preview (not submitted)
Items:
(empty)
Fulfillment: pickup
Payment method: pay_at_pickup
Subtotal: USD 0.00
Delivery fee: USD 0.00
Total: USD 0.00
Preparation estimate: unavailable (workload not connected).
Would you like anything else?
```

For a raw name containing a newline, its item-line rendering contains literal
`\n` inside quotes. Exact-output tests also pin Unicode/quote/tab escaping and
zero-, three- and six-decimal money formatting. The explicit unavailable line and
natural anything-else closing are not permission to send or submit this draft.
The pure preparation calculator is unchanged and **not called**: there is no
connected workload source, invented empty kitchen, workload timestamp or readiness
promise.

## Authority, errors and rollback

All three result seams share whole-event/slot binding, the opaque `Execution`,
`loadExecution`, actual persisted job/event validation and `authorityGuard`.
`Execute` and `ExecuteDraft` reject preview calls with `ErrInput`/zero result;
`ExecuteSummaryPreview` rejects their old call kinds the same way. Legacy request,
result, draft-v1 bytes and fingerprints are unchanged. Old and new mutation IDs
share the same conflict namespace, not parallel replay stores.

Capture lock order is restaurant → conversation → source/call → actual job/event
→ cart → settings/selection → scoped catalog rows. Quote construction stays on the
same transaction via the shared private helper; no public quote call or second
transaction authorizes capture. Current actual claim authority is checked before
cached output, before first write/read release, after the preview insert and,
through common `commitOperation`, after the possibly blocking ledger insert.
Historical reads hold preview/source/result locks through the final output guard.
Each authority guard uses a separate trusted database-clock statement after blocking
work. A stale/expired claim cannot release even old cached output.

Expiry/cancellation after either insert rolls back **both preview and ledger**
and returns zero business output; source cart version never increments. Retain the
same plan and intent for retry with a new actual claim. An ambiguous commit error
is not proof of rollback. The guarded post-blocking point is a linearization point,
not an exact physical-commit/network-delivery clock guarantee.

| Error | Meaning |
| --- | --- |
| `cart.ErrInput` | Invalid ID/descriptor/handle/local context or wrong result seam |
| `cart.ErrNotFound` | Missing/foreign context, plan, cart or preview |
| `cart.ErrConflict` | Changed slot/event binding or operation identity already used for a different request |
| `cart.ErrStale` | Expected capture cart version differs from locked source version |
| `cart.ErrFulfillmentRequired` | No persisted selection; never implicit pickup |
| `cart.ErrStoredData` | Invalid/cross-inconsistent canonical intent, preview, metadata, name, settings, selection or result |
| `cart.ErrLimit` | Live arithmetic or complete body/result resource overflow |
| `foundation.ErrStaleLease` | Actual persisted claim is stale, mismatched, expired or terminal |
| `cart.ErrUnavailable` | Cancellation/database/randomness failure, with driver details redacted |

No order/candidate/presentation/receipt, payment, control/takeover, outbox/send or
job-completion writes occur. Opaque LOCAL fixture authority is not authenticated
customer/provider identity. There is no public HTTP/CLI/UI/model tool and no worker
dispatch. This deliberately leaves real workload, production `prepare_summary`,
presentation evidence, contextual confirmation and submission for future work.

## Focused writer verification

Tests use existing isolated system-temp PostgreSQL fixtures and public synthetic
claim/registration APIs, not inherited DSNs. SQL hashes/counters and positive
lock/database-clock observations supplement public output, readback and replay;
they are not advertised as a public diagnostic API. No sleep-based barrier or
production test hook is added.

From `backend`, use the prepared offline environment:

```sh
export GOTOOLCHAIN=local GOCACHE=$PWD/.cache/build GOPATH=$PWD/.cache/gopath
export GOMODCACHE=$PWD/.cache/modules GOFLAGS=-mod=readonly GOPROXY=off
export GOSUMDB=off GOTELEMETRY=off GOWORK=off

go test ./internal/cart -run '^TestSummaryPreview(Input|Exact|MoneyExponents)$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreview(HistoryAndNamespaces|SimultaneousAndActualClaim|CaptureClockAfterLocks)$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreviewRejections$/(unselected|stale|foreign-.*|unknown-preview|name-.*|settings.*|selection|overflow|oversize)$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreviewRejections$/(body|metadata.*|result.*|estimate|reason|text|duplicate-unit|source-plan|copies)$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreviewBlockingAuthority$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreview(PreservesOldCallKinds|SchemaScopeAndTextBoundaries)$' -count=1 -timeout=20s
go test ./internal/postgres -run '^TestCartSummaryMigrationPreservesPopulatedV7$' -count=1 -timeout=20s
go test ./internal/cart -run '^TestSummaryPreviewIndependent(Guide|IntermediateMoney|MissingSources)$' -count=1 -timeout=20s
```

All initial core public tests and the populated-v7 test were authored before
production writes. Initial cart **compile RED** reported missing preview APIs;
it is not per-case runtime failure. The migration observed **runtime RED** for
missing `app.cart_summary_previews`. Exact-output/history/claim/clock, rejection,
blocking-authority and migration groups subsequently passed. One rejection run
failed in fixture setup because a parameterized pgx statement contained two SQL
commands; splitting those fixture commands produced GREEN without changing
expected behavior. Preservation and supplemental hardening tests are GREEN-only,
not reconstructed pre-implementation RED. Supplemental self-review tests also
observed runtime RED for an infinite metadata timestamp and a tampered cached ID
pointing to a missing immutable row. Both now reject as `ErrStoredData`/zero output
rather than respectively `ErrUnavailable` or `ErrNotFound`; unknown caller read
IDs still return `ErrNotFound`. Supplementary canonical-copy and foreign-preview
probes strengthen the original cases without claiming retroactive RED.

A focused race run of input/exact/money/history/simultaneous/capture-clock tests
passed, as did `go vet ./internal/cart ./internal/postgres`. Unchanged direct-cart,
execution-authority, rich-fulfillment/resource and worker startup acknowledgement
selections also passed. These do not replace the parent full checks.

The earlier unchanged preflight linker disk-quota failure remains historical
infrastructure evidence, not behavior RED. New candidate checks linked successfully;
no storage diagnosis, cleanup, cache relocation or fallback was used. These are
**focused writer observations**; final parent and independent results are recorded below.
Earlier increments' local proof dispositions were not reused.

## Final LOCAL evidence

Parent full checks on `go1.27.3-X:nodwarf5 linux/amd64` each exited **0** in one
173-second run, using the prepared offline environment above:

```sh
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=120s ./...
```

Cart normal/race timings were 78.740s/89.127s; PostgreSQL 11.994s/15.379s and
integration 11.524s/14.321s. A parent focused exact/input/money spotcheck also
passed (0, 4.461s). These are parent observations, not independent executions.

The final independent assessment **passed the bounded LOCAL preview scope**, with
no reproduced blocker or advisory. Its source-only copy and used-package dependency
inventory succeeded; no full-cache copy, full-module lookup, network access,
installation, storage diagnosis, relocation or cleanup was used. Independent
exact/history/scope/corruption/limits/upgrade/legacy/clock/rollback checks and a
targeted race group all passed. Blocking authority took 19.069s under a 20-second
test deadline; this close timing margin is recorded, not treated as a failure.

Three additional scratch-only public probes executed the guide's actual native
function and literal pickup/delivery/empty text, covered currency exponents 1/4/5,
and verified historical replay/read after all current units, menu and settings
were removed. Tampered noncanonical stored bytes rejected with zero output and
unchanged state. Captured test stdout contained package results; stderr was empty.
These supplemental GREEN probes do not reconstruct pre-implementation RED.
Their returned inline source was formatted and adopted as
`summary_independent_test.go`; the parent focused execution passed (0, 6.102s).
The original scratch artifact was no longer present when retrieved; its recorded
hash is historical, not a newly validated byte-identity claim. No production code
changed after the full suite or independent assessment.

Before/after SHA-256 matched for 139 selected original source/document files and
329 selected dependency inputs, and Git porcelain status was byte-identical.
Those claims exclude mutable compiler output, unused dependency inputs, the
standard toolchain and unrelated filesystem paths. Scratch is retained.

This PASS does not establish real workload integration, numeric readiness,
production authentication, customer/provider identity, complete `prepare_summary`,
presentation, contextual confirmation, submission, receipts or dashboard orders.
Application immutability is not protection against arbitrary privileged database
writers. Exact commands, attribution and work-unit commits are retained in
`odd/tasks/local-summary-preview.md`.
