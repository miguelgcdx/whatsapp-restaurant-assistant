# Durable LOCAL menu replies

This is an explicitly configured native Go seam, not default-worker AI routing.
It stores one authorized menu reply and makes at most one LOCAL fake-provider
attempt. There is no UI, new HTTP/CLI route, live account integration, customer
identity, order workflow, reconciliation, or exactly-once delivery guarantee.

## Native entry points

```go
func (*menugate.Gate) RunAndQueue(context.Context, cart.Source, foundation.Job) (menugate.Receipt, error)
func (*menugate.Gate) ReadQueued(context.Context, cart.Source) (menugate.Receipt, error)
func menugate.OpenDispatcher(context.Context, string, string, worker.Sender, time.Duration) (*menugate.Dispatcher, error)
func (*menugate.Dispatcher) DispatchOne(context.Context, time.Duration) (bool, error)
func (*menugate.Dispatcher) Close()
```

`OpenDispatcher` arguments are context, explicit local DSN, configured restaurant,
trusted injected sender, and send timeout. `DispatchOne` accepts the delivery lease
duration. The host owns shutdown: close only after outstanding calls finish.
Tests inject the actual `worker.NewFakeSender` pointed at owned numeric-loopback
HTTP servers. Model tests use the concrete CommandCode client with a fabricated
key and injected transport; no gateway/account compatibility is established.

`Receipt` exposes `OutboxID`, exact `Content`, `ControlVersion`, `State`, `Attempt`,
and `ProviderID`. It is historical data, not a transferable send permit. Every
receipt error returns a zero receipt. Dispatch returning true means **handled
work**, including suppression or expiry, not customer delivery. An error after
claim can coexist with a durable sending attempt; do not interpret it as rollback.

## Queue authority and atomicity

Only a trusted, persisted local fixture source and the exact current job claim
can enter `RunAndQueue`. It accepts no caller reply, permission boolean, model
scope IDs, history, or ephemeral `Result`. Existing `Gate.Run` still returns an
ephemeral proposal with its original priority and error semantics.

1. Precheck configured restaurant, fixture ownership, whole-event source,
   initialized AI control, and the canonical full job claim.
2. Release all locks; run the model/menu tool outside any transaction.
3. In one postcheck transaction, reload source/control and compare the captured
   version, then reuse `postgres.LockExecutionJob` (job, then event locks).
4. Reuse the existing event-unique outbox insertion; set its required AI metadata
   before commit. The transient legacy shape is never committed or observable.
5. After potentially blocking insertion, run the canonical fresh DB-clock guard,
   complete the job, and commit both the original final reply and intent together.

Reply validation has two boundaries. The concrete CommandCode client rejects
empty final content and decoded strings containing NUL with its existing
`commandcode.ErrProtocol`, before a final proposal reaches the queue. Public
empty/NUL cases observe one model request, a zero receipt, and no durable effect;
there is no queue NUL fix or protocol-error remapping.

A final proposal accepted by the client/engine then uses the existing foundation
boundary: valid UTF-8, nonblank, at most **4000 runes**, without trimming,
truncation, or rewriting. Whitespace-only and over-4000-rune final replies return
sanitized `menugate.ErrReply`. Upstream client/engine errors retain their existing
sanitized identities. Reasoning and tool/menu metadata never become outbox
content. Equal text across events is not deduplication.

Completed-job retries return `foundation.ErrStaleLease` before model I/O. To
inspect an uncertain commit, use `ReadQueued`; never rerun the model to resolve
uncertainty. A successful historical read validates bound source ownership,
completed source job and original creation generation/attempt. It does not
require a live execution lease, current AI mode, or equal current control version.
It never mutates state or authorizes dispatch.

The queue is bounded to 20 seconds overall, database phases to five seconds,
rollback cleanup to one second. Context/dependency errors are sanitized, not
converted to empty work. A commit error does not guarantee nothing committed.

## Dispatch, takeover and uncertainty

The coordinator performs separate claim, preflight, I/O, and completion phases:

- Claim/expiry touches only outbox/attempt rows, committing a sending attempt
  before I/O. It never acquires the earlier authority prefix under these locks.
- Fresh preflight loads persisted scope, then locks restaurant, fixture
  conversation, whole-event source, control, completed job/event, outbox/attempt.
  It reloads and compares the persisted delivery (including event, original reply,
  creation and sending generations/attempts), validates finite lease and control
  version, and samples the database clock after all blocking statements.
- Known stale AI control before I/O becomes terminal `suppressed`, with the exact
  matching attempt suppressed too. Same-mode version bumps and takeover/return
  invalidate old intents permanently. No automatic revival exists.
- After preflight commits, the sender runs with no SQL row locks retained.
- Completion fences the exact claim and finite lease, records the actual safe
  result, and does **not** reapply AI mode as a reason to suppress possible I/O.

The unavoidable last-check-to-egress gap remains: takeover can occur after the
last check, and already-in-flight messages cannot be recalled. Lease generations
fence database completion, not an old sender's external request. If a lease
expires, the intent/attempt become `unknown`, never pending for blind resend.
Late completion cannot overwrite a newer/expired claim. Unknown outcomes block
later same-sender work until future, separately implemented reconciliation.
Earlier pending/sending/unknown work fences later work across **both** kinds.

The original fake protocol maps valid 200/message ID to accepted, explicit
400/422 to failed, and other non-200, timeout, or malformed responses to unknown.
Provider IDs are preserved exactly within the original validated boundary. Fake
acceptance is not proof of a real provider or customer identity/delivery.

## Schema and legacy isolation

Migration 10 adds root `outbox.kind` (default `legacy_ack`) and typed nullable
restaurant/conversation/control/creation-claim metadata. Legacy rows require all
these fields NULL; AI rows require the full valid shape and the existing
(event, restaurant, conversation) source FK. The existing event UNIQUE remains.
No sidecar, new operation ledger, data rewrite, seed, or legacy reclassification
is introduced. Suppression is AI-only, including send-attempt outcomes.

Legacy claim, finish, expiry and both pending/sending due branches hard-exclude
AI. Their earlier-sender blockers remain global across kinds. The foundation
Store/Job/Delivery/SendResult, default worker, and fake sender are unchanged.
SQL source and sqlc-shaped SQL/models were maintained **by hand**, not regenerated;
no generator installation or regeneration proof is claimed.

Down refuses AI or suppressed history before DDL. It must not delete or reclassify
history to make rollback possible. Legacy-only rollback preserves rows/bytes;
it is not an operational cleanup command for a populated AI database.

## Focused evidence and limits

New public test families map to the feature specification as follows:

| Families | Public coverage |
| --- | --- |
| `TestOutboxAIMigration`, `TestOutboxAIConstraints`, `TestOutboxAIDown` | S3/S4/S6: populated v9 upgrade, typed shape/FK, legacy ACK and guarded rollback |
| `TestOutboxAISuppressedHistoryDown` | S3/S4/S6: Down refuses suppressed AI history, preserving columns, root bytes, attempts and goose bookkeeping in a new owned cluster |
| `TestDurableQueue`, `TestDurableQueueRejects` | S1–S3: direct/closed-menu tool queue, retry, pre/post authority drift and zero-effect rejection |
| `TestDurableQueueClaims`, `TestDurableQueueReplyBoundary`, `TestDurableQueueReceipts` | S2/S3: late insert-block expiry, exact 4000/4001 bytes, historical receipt/claim binding |
| `TestIndependentDurableIdentity` | S2/S3: competing prechecked proposals yield one acceptance/one zero-receipt stale rejection; SQL-lock release during model work; retry before model; equal text with distinct event/outbox identities; exact original reply, not reasoning; read-only historical receipt across takeover |
| `TestDurableQueueEmptyAndNUL`, `TestDurableFacadeErrors` | S2/S3/S6: upstream protocol rejection; nil/invalid/foreign/closed-pool/context rejection classes, zero receipts/no I/O/no effects, and valid historical read |
| `TestDurableDispatchSuppression`, `TestDurableDispatchPreflight`, `TestDurableDispatchLease` | S4/S5: stale control, post-claim tuple drift/infinite lease, fresh-clock expiry/no resend |
| `TestDurableDispatchOutcomes`, `TestDurableDispatchInFlight` | S1/S4/S5: actual fake wire/results/timeout and takeover during I/O, no retained SQL locks |
| `TestDurableDispatchInvalidResults` | S4/S5/S6: malformed injected sender results cannot invent acceptance/suppression; durable sending expires to unknown without another call |
| `TestDurableDispatchExpiredInFlight` | S1/S4/S5/S6: actual fake HTTP held across real finite DB lease expiry, with/without public takeover; late completion is stale, unknown is not overwritten, one HTTP call/no resend, both-kind later work remains fenced |
| `TestDurableDispatchOrdering`, `TestDurableDispatchConcurrency`, `TestDurableDispatchLegacyIsolation` | S4–S6: both-kind fences, concurrent claim, legacy claim/finish/expiry/due isolation |

Each new family has at most two owned initdb fixtures; most share one fixture
across independently named turn namespaces. Normal/race runs are separate exact
family selections with count 1 and 20-second Go timeouts, from the original
backend/offline caches. Raw D2 receipts are retained under the ignored
`.cache/diagnostics/durable-menu-reply-worker-01a17072/` prefix; correction captures
are under `durable-menu-reply-correction-01a17072/`. The failed missing-TMPDIR
check remains unmodified under `durable-menu-reply-finish-01a17072/`. After explicit
setup/continuation authorization, the five remaining test selections and scoped
vet all exited zero under `durable-menu-reply-finish-authorized-01a17072/`:
invalid-results race, expired-in-flight normal/race, suppressed-history Down
normal/race, and vet of menugate/postgres/worker. Earlier completed empty/NUL,
facade normal/race and invalid-results normal checks were not repeated. The
incorrect initial ErrReply expectation and foreign-settings fixture failure
remain recorded; neither is a fixed production bug. No production code changed
in this correction.

The initial schema RED observed missing column `kind`; API RED observed missing
native APIs. Missing APIs alone do not prove individual runtime rejection
assertions. Supplemental constraints/rollback/late-block/ordering/metadata and
later correction expansions lack distinct preimplementation runtime RED. Later
passes prove current behavior only and cannot reconstruct that history. This
process-history deviation was explicitly acknowledged by the owner, separately
from setup authorization. The missing history is not reconstructed or treated as
runtime/security approval. The required subsequent local checks are recorded
below, not a reconstruction of missing RED. Uncertain commit behavior has not
been fault-injected; reconciliation remains future scope.

### Completed LOCAL proof and retained exceptions

Parent full normal/vet/race all exited zero with empty stderr and no skips under
[`durable-menu-reply-full-corrected-dt35fms3`](../.cache/diagnostics/durable-menu-reply-full-corrected-dt35fms3/).
Its unchanged manifests contain **130 Go/SQL/module files** and **156 backend
Go/SQL/module/guide inputs**. The first full normal run remains failed under
[`durable-menu-reply-full-mdzctm18`](../.cache/diagnostics/durable-menu-reply-full-mdzctm18/):
two historical pre-v10 fixtures called current delivery queries requiring `kind`,
and a third compared whole outbox row shapes across six intentional new columns.
The test-only correction freezes the historical accepted delivery format, retains
every old column/counter/attempt hash and public replay check, positively checks
legacy metadata/v10, and exercises current public delivery after upgrade. The
first failure is not retroactively green.

The independent verifier completed the required S1–S5 runtime/migration examples
and functional S6 checks using 12 retained passing checks plus 18 new checks.
The actual-base public differential passed all three families: legacy outbox with
the actual fake ACK worker, old Gate.Run authority, and cart authority. Only Go
wrapper durations were normalized; stdout/stderr/exit otherwise matched. The
initial duplicate cart harness build failure remains in the 31 raw records;
the authorized diagnostic-only correction reused the existing committed cart
test. No production fix or full-suite rerun was inferred from harness repair.
See the [parent evidence audit](../.cache/diagnostics/durable-menu-reply-parent-01a17072/independent-final-review.json)
and retained [sandbox correction evidence](/tmp/restaurant-assistant-durable-reply-verify-01a17072/run.jWaSTq/correction/closure.json).

The verifier's factual disposition remains **CLOSED—PARTIAL**, solely because
four mutable 16-KiB sandbox runtime counters changed:
`home/.config/go/telemetry/local/{compile,go,link,vet}@devel-devel-linux-amd64-2026-10-26.v1.count`.
The parent rejected counter immutability as an unjustified auxiliary gate, not
as a waived security/functional check or a relabeling of the child's verdict.
Exact before/after hashes remain in the parent audit and
[counter mutation record](/tmp/restaurant-assistant-durable-reply-verify-01a17072/run.jWaSTq/correction/artifact-mutations.json);
no restoration was performed. The other **79 of 83** old sandbox artifacts and
all old raw source/receipt evidence were unchanged. `GOTELEMETRY=off` was recorded
but did not prevent local counter writes; their cause is unknown. Neither upload
nor zero egress is inferred from that environment variable. Parent readback
verified 418 original recorded files and 156 candidate/146 base copied inputs.
The older 168-file parent baseline has eight known feature deltas and **160**
unchanged files, not 163 after fixture correction.

### Separately adopted regression probe

`independent_durable_test.go` adopts the retained independent probe with gofmt
only, from source SHA-256
`975022f660b58fa7e6ffd2c61b1736dc81f19497cad60615c2c0a9b196a12c33`.
Original-backend focused checks both passed with count 1 and a 20-second timeout:
normal **0.748s**, race **1.838s**, exit 0, empty stderr, actual named RUN/PASS,
no skip. Separate [adoption receipts](../.cache/diagnostics/durable-menu-reply-probe-adoption-01a17072/)
retain invocations, raw stdout/stderr/exits and before/after preservation hashes.
This new test is **not** included in the older 130/156 full-proof manifests;
those full suites were not rerun for this test-only adoption. No meaningful new
production RED applies to adopting an unchanged, previously executed probe;
the acknowledged historical per-rule RED gap still stands.

These are bounded LOCAL checks, not native review, production readiness, live
provider/customer proof, default-worker AI routing, orders/UI integration, or
reconciliation. Parent readback, exact-path Git delivery and review disposition
remain parent-owned; this evidence does not approve them.
