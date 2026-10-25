# Local conversation-to-cart execution

`internal/cart` now offers an **explicit native local bridge**, before confirmation
or orders. It binds an already persisted synthetic inbound event to one provisioned
local conversation, records immutable server-owned call slots, and checks the actual
persisted job claim in the cart transaction. It does **not** dispatch from
`Worker.Run`, interpret event text, expose a customer endpoint, or authenticate a
customer. The existing worker acknowledgement/outbox behavior is unchanged.

## Explicit setup and native calls

Apply public `postgres.Migrate` explicitly to an owned local database, then configure
the fictional catalog and `cart.ProvisionLocal` as described in [cart.md](cart.md).
Migration `00006_local_cart_execution.sql` adds two empty tables:

- `cart_execution_sources`: one actual inbound-event UUID primary key, with a
  restaurant/conversation composite foreign key and explicit creation timestamp;
- `cart_execution_calls`: unique event/nonnegative-runtime-slot, composite source
  foreign key, action, canonical intent, 32-byte SHA-256 fingerprint, globally unique
  server operation UUID and explicit creation timestamp.

There are no defaults, seeds, pending-result states, TTL, cascading deletion or
cleanup. Existing migrations, foundation jobs, staff/catalog stores and dependencies
are unchanged. Opening a store never migrates, seeds or registers plans.

Native application/fixture code supplies this sequence (errors must be checked):

```go
// eventID identifies an ALREADY persisted synthetic event. Local context was
// explicitly provisioned/loaded by trusted application setup, not by the sender.
plan, err := store.RegisterExecution(ctx, local, eventID, 0, cart.CreateCall())
// ClaimJob is the existing public foundation API; do not manufacture a claim.
job, found, err := jobs.ClaimJob(ctx, serverTime, leaseDuration)
created, err := store.Execute(ctx, local, plan, job)

edit, err := cart.EditCall(created.Cart.ID, created.Cart.Version, action)
editPlan, err := store.RegisterExecution(ctx, local, eventID, 1, edit)
changed, err := store.Execute(ctx, local, editPlan, job)

read, err := cart.ReadCall(changed.Cart.ID)
readPlan, err := store.RegisterExecution(ctx, local, eventID, 2, read)
fresh, err := store.Execute(ctx, local, readPlan, job)
```

This is an API sketch, not a new CLI or error-handling example. `action` comes from
existing `Add`, `Remove` or `SetNote` constructors. `Call` is closed and its zero
value is invalid. Parameters contain only existing cart ID/version/action values:
no actor, restaurant, source, claim, runtime slot or arbitrary operation nonce.
The runtime supplies event identity and nonnegative int64 slot separately during
registration. `Execution` is opaque; `OperationID()` exposes its retained server
identity for diagnostics, not a customer credential. Restore a handle by registering
the same event/context/slot/canonical intent again after reopen or reclaim.

Registration freshly validates the stored local fixture context and actual event
sender. The **whole event**, not merely a slot, binds to one restaurant/conversation.
Different-context registration, including another slot or different-sender
context, conflicts. Wrong-sender setup for an unbound event is not found. A retained
slot cannot change canonical intent. The same intent restores the original handle
and operation UUID; the UUID is allocated only for a new slot, never per attempt,
lease generation or model nonce. Different intent requires a new runtime slot.

Registration is immutable **local plan setup**, not an executed mutation or reserved
committed result. A planned edit can fail without a result ledger entry, while its
plan remains immutable. This deliberately differs from direct `Create`/`Apply`:
their existing failed-key-reuse semantics remain unchanged. An application with
direct cart-store access still has the old trusted-local APIs; this bridge is not
an authorization sandbox against malicious application code or database writers.

## Results, retries and errors

Create/edit use the existing complete committed-result ledger, canonicalization,
snapshot validation and version/CAS logic. Replay returns the original cart/unit
IDs, prices, notes, version and subtotal even after later edits. Bound reads return
a **fresh** scoped snapshot every time and never insert mutation ledger results.
Results from `Execute` remain subtotal-only. The additive
[draft fulfillment seam](cart-fulfillment.md) uses separate `SetFulfillmentCall` /
`QuoteCall` descriptors and `ExecuteDraft` rich results, with explicit selection,
separate fee/total and settings basis. The two result seams reject each other's
call kinds with `ErrInput` before domain effects. Neither introduces implicit
pickup, confirmation, receipt or order.

The additive [immutable LOCAL preview seam](cart-summary-preview.md) uses
`CaptureSummaryPreviewCall`, `ReadSummaryPreviewCall` and `ExecuteSummaryPreview`.
It shares the same plan/job authority and operation namespace, but stores complete
historical local previews rather than fresh subtotal reads or draft quotes. All
three result seams reject the other call families with `ErrInput`/zero output.
Migration 8 adds preview storage; this is not complete `prepare_summary`, workload
integration, presentation or submission. Historical final evidence below concerns
the earlier execution increment, not this new preview candidate.

A cart commit followed by process failure before job completion can be replayed
under a newly claimed generation with the same registered plan. The old generation
cannot retrieve even a cached mutation result. Completed, failed, expired,
reclaimed, foreign-event and tampered claims reject before business output.

Existing cart errors remain documented in [cart.md](cart.md#concurrency-retry-and-errors).
Additional meanings at this boundary:

| Error | Execution meaning |
| --- | --- |
| `cart.ErrInput` | invalid descriptor/zero handle/context, negative slot, malformed event or cart parameters |
| `cart.ErrNotFound` | missing/foreign source, call, event or stored local context; sender mismatch |
| `cart.ErrConflict` | whole-event context or retained slot intent conflicts with registration |
| `foundation.ErrStaleLease` | missing/mismatched actual claim, terminal state, different generation/attempt/event/sender/content, expired or nonfinite deadline |
| `cart.ErrStoredData` | invalid canonical stored plan, fingerprint, snapshot or result |
| `cart.ErrUnavailable` | cancellation or database/randomness failure, without private driver details |

Errors return zero business results. Rejected/late-failed execution rolls back cart
and result writes. Do not assume an ambiguous commit failure means no commit:
retain the plan and exact intent for a current-generation retry. The bridge adds
no renewal service, retry dispatcher or new completion behavior.

## Transaction and clock boundary

Lock order is restaurant → local conversation → source/call → actual job/event →
cart → menu/settings/target resources. The bridge reloads job ID, event linkage,
state, generation, attempt, deadline and copied event ID/sender/content from stored
rows. A mutable `foundation.Job` alone grants no authority. The current job row
stays locked through the guarded outcome, preventing concurrent completion/reclaim.

Private cart helpers accept an optional same-transaction guard; the direct public
`Create`, `Apply` and `Read` signatures and defaults are preserved. The bridge does
not check authority in a preceding pool call, open a nested cart transaction, or
commit before saving its complete result.

Authority is checked before cached business output, after resource acquisition
before the first write, and again **after late blocking statements, including the
complete-result insert**, before commit/result return. Each check samples a separate
`SELECT clock_timestamp()` statement after the blocking work. It does not use
transaction-start `now()`, an expression evaluated before PostgreSQL `LockRows`, or
caller/constructor time. A finite live stored deadline is mandatory. Holding the
job lock prevents reclaim but does not prevent the deadline expiring; a late expiry
or cancellation rolls back all cart/ledger writes.

The authority linearization point is this post-blocking trusted-clock check with
the job lock held. It is **not** a promise about exact wall-clock time at physical
commit or network result delivery. Existing foundation claim/completion caller-clock
semantics are unchanged: trusted server code still owns those inputs and persisted
leases. This bridge accepts no caller clock to override its authority sample.

## Trust limits

Synthetic HMAC proves possession of the local test secret, not customer,
restaurant, phone or verified WhatsApp identity. Explicit fixture provisioning and
registration are trusted application setup; signed caller fields do not automatically
select restaurant/conversation authority. Native operations are internal Go APIs,
not public HTTP tools. Event text remains opaque. Verified provider provenance,
customer authentication, conversation-control epochs, staff takeover/AI policy,
model tool protocols, contextual confirmation and all order workflows remain future
gates. Local claims and tests are not production/control/provider proof.

## Focused evidence and reproducible commands

All database tests use fresh system-temp owned PostgreSQL fixtures, never inherited
DSNs. SQL counts/hashes and `pg_stat_activity`/database-clock observations supplement
public results, cart readback/replay and foundation `Inspect`; there is no claimed
public ledger-diagnostic endpoint. Wait tests use positive lock and deadline
observations, not sleeps, elapsed-time inference or production test-clock hooks.

From `backend`, use the prepared offline environment:

```sh
export GOTOOLCHAIN=local GOCACHE=$PWD/.cache/build GOPATH=$PWD/.cache/gopath
export GOMODCACHE=$PWD/.cache/modules GOFLAGS=-mod=readonly GOPROXY=off
export GOSUMDB=off GOTELEMETRY=off GOWORK=off

go test -count=1 -timeout=20s ./internal/cart -run '^TestExecution'
go test -count=1 -timeout=20s ./internal/postgres -run '^TestCartExecutionMigrationPreservesPopulatedV5$'
go test -count=1 -timeout=20s ./internal/integration -run '^Test(SignedLocalCartExecution|LocalCartCancellationRollsBackPendingResult|LocalCartAvailabilityWriterOrdering)$'
```

The pre-implementation RED history is real: plan/claim, blocking-authority and
same-slot concurrency tests failed to compile because the bridge API was absent;
the populated-v5 migration test failed because the new source table did not exist;
the signed-ingress integration test failed to compile on absent bridge APIs. The
first GREEN attempt exposed a helper-local `err` declaration compile failure, which
was corrected before successful focused runs. No working implementation was broken
to reconstruct RED.

Initial test cases cover whole-event binding/slot immutability, stable UUIDs,
changed intent, current job before cached output, each copied claim field, reclaim
and terminal-cache rejection, job/cart/menu/cache/result waits, cancellation,
rollback and same-plan retry. Supplemental GREEN-only boundary probes strengthen
foreign-restaurant/sender/malformed setup integrity, nonfinite deadlines, fresh
repeated reads without ledger entries, and failed-plan-versus-result semantics.
Most supplemental probes do **not** have pre-implementation RED evidence.
One strengthened case found a real error-classification defect: re-registering an
already-bound event under a different-sender context returned `ErrNotFound` instead
of `ErrConflict`. That assertion observed RED before the source-first validation
fix, then GREEN; an unbound wrong-sender event still rejects without setup writes.

The migration test embeds v1–5 fixture files and uses public Goose to establish
v5, populates historical cart units/exact notes/replays and foundation/staff/catalog
state through old public APIs, then calls public current `Migrate`. Public readback,
historical replay, staff access/menu/settings/results and foundation `Inspect` remain
identical; supplementary hashes cover all old records/counters and both new tables
must be empty. No Git history, fixed commit, product flags or seeded defaults are used.

Focused writer results cover the new seam plus unchanged direct-cart lifecycle,
CAS, replay, rollback, limits, concurrent creation/edit and both availability writer
paths. These are **writer-focused results only**; final parent and independent outcomes
are recorded below. Earlier [cart-core proof dispositions](cart.md#observed-results-and-local-dispositions)
are historical and do not waive this increment's evidence requirements.

## Final local evidence and disposition

Final parent checks on Go `go1.27.3-X:nodwarf5 linux/amd64`, using the prepared
offline environment above, each exited **0**:

```sh
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=120s ./...
```

The full run completed in 47 seconds. Normal/race timings included cart
18.977s/24.455s, integration 12.492s/16.533s, PostgreSQL 11.461s/15.460s and worker
1.843s/3.470s. These are parent runtime results, not independent executions.

An earlier full run was normal **0**, vet **0**, race **1**, failing
`TestForeignReplacementSurvivesShutdown`. A separate harness-only correction
replaced pathname visibility with observed `Run` startup and a shutdown sleep with
actual completion; foreign socket identity/permissions remain asserted. The new
readiness regression observed RED before correction, then focused normal/race/vet
passed. Production socket ownership and worker dispatch are unchanged. This proves
a harness defect, not the exact interleaving of the historical failure or protection
against hostile same-UID pathname races.

The final independent assessment is **PARTIAL**. Its bounded 104-file source-only
copy succeeded, but `go list -m -json all` exited **1**, reporting
`go: module lookup disabled by GOPROXY=off`. Setup stopped before any tests, builds,
vet, race, guide examples or baseline probes. No retry, substitute inventory,
network access, installation or cache-copy fallback followed. Dependency integrity
could not be established. The selected original source SHA-256 manifests and Git
status matched before/after; static scope was incomplete. No reproduced product
defect was established, but that is not a completed security assessment.

The human accepted closure with this **LOCAL-only missing independent proof**
explicitly retained. S4 independent proof remains unmet; S2/S3/S5 were not independently
verified. Acceptance does not make them an independent PASS, production/customer/
provider approval, a future waiver or permission to publish. The original failure,
RED/GREEN qualifications and exact disposition remain in
`odd/tasks/local-cart-execution.md`. Existing scratch is retained; no cleanup was
performed.
