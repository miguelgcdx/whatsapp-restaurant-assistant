# AI cart-edit bridge — implemented, final audit partial

**Status: accepted development deliverable with independent audit PARTIAL.** Parent
executable checks passed; the audit limitation is explicitly retained. The distinct
parser/interpreter, trusted bridge and source-bound clarification queue are
implemented. All 12 focused tests and their race checks passed; vet and the seven
affected-package suites passed (72 top-level tests, zero failures or skips).
The independent source-and-parent-receipt audit found no concrete candidate-code
blocker and supported the bounded S1 rules, but automated verification of all 215
manifest entries and complete patch reconstruction remain unverified following
an infrastructure helper failure. This is not final acceptance or an independent
Go/PostgreSQL behavioral rerun. No helper repair, setup retry or replacement
verifier was launched.

Parent observed functional RED before implementation: job11 compiled all four
packages and ran isolated PG fixtures, with 10 top-level behavior failures at the
explicit stubs and 2 passes (configuration and legacy queue preservation), exit 1,
empty stderr. Package times: editintent 0.045s, carteditflow 0.004s, menugate 8.105s,
testpickupeval 8.195s. Parent strengthened the selector test to resolve original
`p1` while keeping the newer same-kind marker. Focused job12 again compiled and
ran the fixture, then failed at the explicit Host stub (0.920s, exit 1).

Raw parent RED receipts (outside the worktree, not newly executed by the writer):
`/tmp/restaurant-ai-ambiguity-xqWM2b/phase1-red.{command,stdout,stderr,exit}` and
`/tmp/restaurant-ai-ambiguity-xqWM2b/selector-red.{command,stdout,stderr,exit}`.
Parent-owned post-implementation compilation, focused/race/vet and affected-suite
results are recorded below. Earlier unavailable proof and the old Go7 gate remain
separate; none of these results closes or replaces those gates.

## Actual boundary

This is a reusable, explicitly invoked HANDLE-EDIT library bridge for S1 in
`odd/tasks/ai-ambiguity-host-integration.md`, not a general menu/chat turn router,
ordering loop, worker, default behavior, production mount or old LOCAL host task 5
implementation. That old task remains blocked on its separate task-4 human-selected
Go7 assessment. This work neither supplies nor disposes that gate or retries old
proof. Existing menuengine, cartintent, pickupintent, cart guard and cmd files are
unchanged. Existing `testpickupeval` helpers are test fixtures, not production.
No ambient credentials, alternate SDK, dependencies, migrations or live provider
are added; interpreter construction requires an injected `commandcode.Client`.

## Semantic contract

Use the sole `interpret_cart_edit` tool once. Supply actual stored inbound text,
current cart snapshot and complete ordered pending presentation facts as one user
JSON object, with separate trusted instructions. The prompt labels all strings
(especially stored requests and notes) as untrusted data, never instructions or
authority. Content/reasoning cannot substitute for tool output or claim effects.
The parser and serialized input have a 512 KiB inclusive byte bound; pending input
has the existing complete-view 64-record bound. Interpreter timeout is 20 seconds,
Host.Admit timeout 30 seconds, with the injected client's existing 10-second call
and wire budgets (1 MiB request, 512 KiB response). Output tokens are explicitly
configured in 1..8192. No retries, repair or truncation. Snapshot IDs and Version
use their existing public MarshalText implementation, yielding JSON strings rather
than opaque `{}`; unit/menu IDs remain usable facts, not authority.

The distinct parser accepts one variant at a time:

```json
{"proposal":"unresolved_target","kind":"Remove"}
{"proposal":"resolve_target","pending_selector":"p1","edit":{"action":"remove","expected_version":"3","unit_ids":["20000000-0000-4000-8000-000000000001"]}}
{"proposal":"no_edit"}
```

The exact unresolved wire kind enum is `Add | Remove | SetNote`. It maps to backend
`cart.UnresolvedAdd (add)`, `UnresolvedRemove (remove)`, and
`UnresolvedSetNote (set_note)`, respectively. No synonyms or case coercion.
Pending presentation `kind` uses those existing backend lowercase constants;
selectors are invocation-local strings, never serialized backend references.

Ordinary concrete Add/Remove/SetNote JSON remains the exact existing
`cartintent.Decode` format (lowercase `action`, including `expected_version`).
The wrapper delegates unchanged concrete JSON, including resolution's `edit`, to
that decoder with trusted cart ID and checks its version echo against the host's
trusted version. That echo supplies no authority. No new authority source,
cart/version/slot fields are accepted on semantic carriers; no model ID, prose or
JSON can establish source/claim/control, select a cart or allocate an operation.
`cartintent.Decode` itself is unchanged and rejects the new carriers.

`no_edit` accepts strictly its sole field: reject extra/mixed/duplicate/null or
malformed input. It means zero mark/admission/edit/resolution, not cancellation,
abandonment, completion, `not_complete`, routing or clearing a guard. Provider,
transport, parsing and unknown-selector errors remain errors; never fabricate
`no_edit`. `pickupintent` stays binary `complete|not_complete` and unchanged.

## Trusted call flow

1. `Host.Admit(ctx, Turn)` receives actual LocalContext/MenuClaim, current cart ID,
   version and a nonnegative host-owned runtime slot. Read the actual cart and
   `Store.ReadPendingEdits` for **each** model invocation. The claim's stored event
   provides inbound text; the backend reloads current authority.
2. Retain that exact complete `PendingEditView` and private ordered `p1..pN -> Ref`
   association while the interpreter runs outside database transactions. Send
   only selector/kind/exact stored request presentation data. Never resolve by
   later list positions, newest/nearest, kind, question wording or model IDs.
3. Ordinary concrete proposals use existing `AdmitNativeEdit`. Unresolved target
   uses existing `MarkUnresolvedEdit`. Original request comes from storage,
   preserving exact bytes; model prose cannot replace it.
4. After a successful mark, call `QueueClarification(actualClaim, sameHostSlot)`.
   This gate API finds that actual ACTIVE marker through its canonical
   source/event/slot inside the existing guarded reply transaction. Scoped joins
   bind the marker operation, immutable canonical descriptor/fingerprint, original
   stored event, cart, admission job/control and generation/attempt provenance.
   It derives the question from stored kind, never caller/model text/kind.
   `queue.go` extracts only the original guarded transaction into `commitReply`;
   RunAndQueue still performs precheck → menuengine → Event.Validate → that helper.
   Source/control/job/outbox locks and final postblocking fresh-lease guard are
   retained; clarification never invokes unrelated menuengine.
5. Explicit resolution decodes its concrete edit strictly, looks up the selector
   only in the retained map, and calls `AdmitResolvedNativeEdit` with that same
   view/ref. Changed active sets fail stale, unknown selectors fail input; do not
   guess, refresh silently or admit fallback work.
6. `Host.Execute(ctx, nativeRef)` uses existing `RecoverNativeEdit` and
   `ExecuteNativeEdit` with the independent native business lease. Only successful
   actual application atomically resolves exactly one marker and updates cart.
   Admission/queueing/failed execution cannot resolve it. Ordinary edits preserve
   unrelated markers. New pickup completion remains blocked while any is ACTIVE;
   successful resolution changes cart version and requires a fresh presented
   summary before completion can submit. No direct SQL clearing or implicit
   resolution, expiry, coalescing or abandonment is introduced. Lease configuration
   is 1 millisecond through 1 minute; `ErrBusy` reports an already-leased native
   reference without polling/retrying or fabricating a result.

### Clarification is queued, not delivered

Exact bounded host questions selected by stored kind:

| Stored kind | Question |
| --- | --- |
| add | Which item would you like to add? |
| remove | Which item would you like to remove? |
| set_note | Which item should I change the note for? |

`Outcome.Receipt()` is a real source-bound durable outbound receipt, not evidence
of delivery/ACK, cart application, resolution or submission. The queued job is
completed only when outbound persistence commits. `Outcome.Question()` exposes
only that receipt's content, not an unpersisted substitute. Same-kind markers
remain distinct; question text never selects one.

**Mark and queue are separate commits.** If marking succeeds and queue fails,
`Outcome.Marked()` must retain the known committed reference alongside the error;
receipt/native remain zero unless independently known successful. The marker stays
ACTIVE and blocks pickup. Do not claim the whole attempt had zero effects.
After uncertain queue commit use existing `ReadQueued(actualSource)`, not another
model run. A known failed queue can be retried through `QueueClarification` using
the original actual claim/slot if still authorized. That direct public recovery
never fabricates a question or abandons a marker; no generic retry loop is added.
Pre-mark/parser/selector/fresh-admission rejections and `no_edit` have zero effects.

## Public rule tests and observed evidence

All new top-level tests start `TestAIAmbiguity`. Unit cases cover strict semantic
carriers, legacy concrete preservation, actual offline HTTP request facts and
single tool, provider/cancellation errors, and missing configuration. Public PG
integration cases reuse `testpickupeval`'s actual fixture/store/control/claim,
summary receiver and outbox reads; they call the real new Host API, not a fake host.
They specify marker + source-bound pending clarification, ordinary Add preserving
ambiguity, linked Remove admission versus execution, fresh summary completion,
no_edit/malformed/unknown selector no-effect rejection with slot reuse, exact-view
staleness during the injected HTTP callback, two same-kind markers with one selected
resolution, and truthful partial commit/recovery after canceled queueing.

Gate tests specify all three stored-kind questions, actual source-bound public
ReadAdmission/ReadQueued/Inspect evidence, missing marker/wrong slot/control
rejection, and one positive existing RunAndQueue preservation case for the shared
helper. No test SQL, fixture schema, internal storage assertions, generic barrier
framework, altered Scenario/Edit.Pending or production activation is added.
Production SQL addition is the scoped marker lookup only; all outbound writes
reuse the original guarded queue transaction, and cart writes use existing APIs.
The HTTP mutation callback is an actual existing injection seam, not a timing race.
These are deterministic wire/side-effect specifications, not evidence of semantic
model quality. No live-model or held-out semantic evaluation has been run.

### Parent executable results

All successful commands exited 0 with empty stderr. Raw `.command`, `.stdout`,
`.stderr` and `.exit` receipts are retained under
`/tmp/restaurant-ai-ambiguity-xqWM2b`.

| Receipt | Observed result |
| --- | --- |
| `phase1-red` | 10 explicit behavior-stub failures / 2 passes; exit 1 |
| `selector-red` | Strengthened original-`p1` test failed at the Host stub; exit 1 |
| `implementation-focused` | 10 passes / 2 test-state failures; exit 1 |
| `corrected-focused` | All 12 top-level tests pass; zero failures/skips |
| `implementation-race` | Same 12 pass under the race detector |
| `implementation-vet` | Seven-package vet passes; empty stdout/stderr |
| `affected-suite` | Seven packages, 72 top-level passes; zero failures/skips |

The initial implementation run's two failures are retained, not relabeled as
success. Existing completion checks require a current presented summary before
checking markers: the lifecycle test now expects the existing summary-required
error after ordinary Add invalidates its summary; the queue-failure test first
prepares/presents a real prior-source summary to preserve its exact marker-blocking
assertion. Backend error precedence and earlier marker/no-effect/recovery/fresh-
summary assertions were not changed.

### Independent audit boundary and remaining proof

The one independent audit inspected actual source/authority/serialization/selector
logic and all seven raw parent receipt sets. Direct `sha256sum` and baseline diff
checks established the manifest and patch artifact hashes, the exact 7,486-byte
pre-edit queue baseline, final queue, every one of the eleven new unit-file hashes,
and the twelve-path unit scope. Existing dirty bytes were assessed against that
baseline, not against HEAD.

Its initial combined Python helper exited 1 with `SyntaxError` before running its
assertions. Setup/copy had succeeded. The helper was not repaired or rerun, and no
new verifier was launched. Original permitted direct read-only checks subsequently
succeeded, but **automated verification of all 215 manifest entries and complete
patch reconstruction did not execute and remain unverified**. The final overall
verdict is therefore **PARTIAL**, despite no concrete candidate-code blocker.
No independent runtime execution or live-model semantic-quality claim is made.
The human explicitly accepted this limited proof scope as a development
deliverable. The audit verdict remains PARTIAL and the two unexecuted automated
checks remain unverified. Acceptance does not authorize additional verification,
helper/verifier retries, production or general-host activation, publishing, or
closure of earlier unavailable proof and the old Go7 gate.

The pinned pre-documentation candidate is
`/tmp/restaurant-ai-ambiguity-candidate-e2v1iqd5/source`, with manifest SHA256
`f404dc3cdd34d796fcbf2477c701dbeb20faa693304dddec80bec8412eef41eb`
and unit-patch SHA256
`7b9db1840bcb46d8b33cb0b96853e2f15dbd6a17876f22405931fec7eded5426`.
Only this evidence documentation was updated after that audit; no runtime source,
schema or test input was changed to resolve the missing audit proof. Verifier
scratch `/tmp/restaurant-ai-static-verifier-RMZIPD` is retained as evidence.
No commit, index/ref movement, publishing or production activation was performed.

Inherited backend limitations remain in [ambiguous-cart-edits.md](ambiguous-cart-edits.md):
complete-view count/byte overflow fails closed; duplicate destructive targets can
leave another marker unresolved. This bridge invents no escape or policy.
