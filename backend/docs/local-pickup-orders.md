# Connected LOCAL pickup contract

**Status: pickup domain/persistence/history and staff domain are producer-implemented and historically self-checked; required independent acceptance remains unavailable. T3B bridge parent focused normal/race/vet passed; its independent verifier was unavailable before startup, so independent bridge acceptance is missing.**
**Staff target revision: `pickup-staff-v1-r3` — selected reference; r3 wire projection and main service dispatch remain unimplemented/held.**
This is the new approved pickup slice, not acceptance of the stopped menu-runtime proof. `internal/cart/pickup_contract.go` fixes the backend-selected in-process domain/adapter seam. The human-selected parent registration extension passed functional RED/GREEN, unchanged legacy-byte differential, full Cart regression, focused race and vet self-checks. The staff adapter passed its historical self-checks; its first independent verifier stopped before setup/tests because required fresh-source-copy isolation conflicted with the parent's no-copy assignment. The later domain independent verification is also unavailable; no retry or substitution of either stopped verifier is authorized here. The portable AI assessor's exact three sources were human-authorized, imported into MAIN with matching released hashes, and passed parent package normal/race/vet checks (0.012s/1.075s/vet quiet, stderr empty). The fake LOCAL HTTP receiver component is independently verified. These component outcomes do not establish connected acceptance. The original three domain coverage tests remain blocked, as do the stopped r3 producer path and old R7 proof. There is no new public submit endpoint or running service.

## T3B bridge implemented — parent GREEN, independent verification unavailable

`PickupStore.AssessAndSubmit(ctx, execution, runtimeSlot, assessor)` now obtains
trusted stored `CompletionContext`, copies every intervening history turn with
explicit known-speaker conversion, and supplies separate exact summary, closing
question and inbound anchors to the actual `pickupintent.Assessor.Assess`.
Assessment runs outside database transactions. Nil (including typed-nil)
assessors, unavailable history and unknown speakers fail closed; provider and
protocol error identities are preserved with zero submission output. A
`not_complete` proposal refuses without allocating a registered operation.
Only `complete` can bind server-owned candidate, stored inbound UUID, digest,
accepted assessment version and decision. Context is rechecked after assessment,
then the existing `SubmitPickupCall`/`Store.RegisterExecution` and `Submit`
primitives own registration and atomic persistence/fences.

The explicit nonnegative `runtimeSlot` belongs to the trusted host's existing
registration sequence; no implicit/reserved slot is introduced. Registration uses
the source's provider event ID; evidence uses its stored UUID. The caller supplies
no summary, context, history, candidate, digest, semantic decision or assessment
authority metadata. Construct the assessor with `pickupintent.New` and trusted
configuration. Committed replay locates the existing scoped registration, uses
canonical descriptor/ledger and source/claim/control checks, then delegates to
`Submit` again before releasing the original result. It does not require today's
draft/history or another model call, and does not treat identity loading alone as
authorization. No model-call exactly-once guarantee is promised.

The new `TestPickupCompletionBridge...` tests reuse the existing `pickupFixture`
and membership-scoped staff domain ports. They read the actual queued summary,
POST its exact text to the verified `testpickupreceiver` HTTP server, decode and
check HTTP 200 ACK correlation/SHA/reference before recording presentation, and
likewise acknowledge conversational replies. A separate fixture-owned model HTTP
server supplies strict synthetic tool responses to the actual `commandcode`
client and imported assessor, not a Go decision callback. Tests assert exact
portable anchors/all ordered history without authority metadata, one unpaid
immutable order, original replay even after draft/staff changes, staff-domain
processing, and unchanged public cart/orders/intents/processing counters on
not-complete, provider/invalid-result, missing-receipt and stale-control refusal.
Scripted model responses prove plumbing only, never natural-language quality.

**Parent functional RED → GREEN observed; independent bridge verification is unavailable.**
Parent job6 ran `/usr/bin/go test -v -mod=readonly -count=1 -timeout=180s -run
'^TestPickupCompletionBridge' ./internal/cart` from backend: native exit 1,
package 4.687s/10s wall, stderr empty. Compilation and existing isolated PostgreSQL
fixtures succeeded; all five top-level groups/six leaf cases ran and failed on
the explicit scaffold error, with no skips or old test names. This was behavioral
RED, not setup/compilation failure. The same writer then implemented the bridge;
only formatting/static inspection was performed by the writer. Parent job7 then
passed the same focused tests: normal exit 0/4.648s, race exit 0/6.495s and vet
exit 0/quiet, all stderr empty. All five groups/six leaf cases passed with no
skips. Raw command/exit/stdout/stderr records are retained under
`/tmp/restaurant-pickup-bridge-Boc1NK/bridge-{red,green,race,vet}.*`.
The integration test source is unchanged from RED. The new trusted
assessment/registration/replay boundary requires separate bridge-only
independent verification. That verifier (`mvwtud9d-c-iw28`) could not start:
`could not write agent instructions: EDQUOT: unknown error, write`.
No agent setup, source copy, independent Go/PostgreSQL checks or probes ran.
The write destination and new pressure cause are unknown; this does not establish
that the previously verified quota override reverted. The unavailable verifier
is not retried, repaired or substituted. Parent passes remain self-checks only,
with independent acceptance held for explicit missing-proof disposition. No full S1–S3/S5–S7 completion is claimed. JWT staff
HTTP/main dispatch/r3 projection and broader independent verification remain
separate and incomplete; these tests exercise staff **domain** operations only.
Existing stopped proofs, coverage gaps, delivery/live-provider deferrals and
no-activation limits remain.

## Connected path

1. Persist a synthetic inbound event and use the existing trusted cart execution path to edit the draft. `LocalContext`, a parsed ID or detached `SummaryPreview` alone authorizes nothing.
2. `PickupRuntime.Prepare` validates the current pickup cart/version and actual execution authority, obtains server prices and authoritative workload, and stores an immutable candidate plus its exact complete summary and natural anything-else question.
3. A LOCAL fake receiver actually receives that stored text. Only its correctly correlated `RecordLocalPresentation` callback establishes synthetic presentation. Enqueue, failed send and provider acceptance do not.
4. `CompletionContext` must return stored inbound text/event, active presented summary/question, complete ordered intervening history, cart/control versions and a server-computed digest. The authoritative assembler/history digest is producer-implemented with historical self-checks, not independently accepted. The imported AI assessor interprets the natural-language context through the new bridge; bridge focused parent normal/race/vet passed, but its independent verifier was unavailable before startup.
5. `Submit` validates trusted assessment correlation and current state, then atomically stores one unpaid order, immutable units, consumed evidence, original replay result and receipt intent. It makes no external call inside the transaction.
6. Authenticated staff read the confirmed snapshot and manually process the pickup order. Staff updates create notification intents; neither elapsed time nor conversation changes fulfill or collect payment automatically.

## Authority and replay

| Boundary | Required behavior |
|---|---|
| Model inputs | `prepare_summary`: current cart version. V1 `assess_pickup_completion` supplies only `complete`/`not_complete`; the trusted adapter attaches stored candidate/source/digest and server-configured `pickup-completion-v1`. No model identity, total, price, clock, operation key, presentation flag or generic `confirmed` boolean. An optional snapshot selector elsewhere never grants authority. |
| Trusted execution | `PickupExecution` carries opaque local/source bindings, the registered `Execution`, actual persisted job claim and current control version. The host explicitly registers the matching `PreparePickupCall(cartID, version)` or `SubmitPickupCall(evidence)` with `Store.RegisterExecution(local, persistedEventID, runtimeSlot, call)`. Slot is a nonnegative host-owned sequence value, never implicit/reserved/model-supplied. Prepare/Submit reload the exact registered descriptor, source/hash/scope and real claim, including after blocking reads; zero or mismatched execution grants no operation authority. Read-only `CompletionContext` validates Source/actual claim/current control before an assessment/submit plan exists, without allocating an operation or requiring a fake descriptor. |
| LOCAL presentation | `PickupLocalPresentation` contains intent/candidate IDs, stable fake-receiver reference and digest of the text actually received. Verify stored scope/linkage/text; assign receipt ID/time server-side. This callback is not a model tool or HTTP route, and proves no live delivery. |
| Assessment | The trusted AI adapter supplies inbound reference (`Source.EventUUID()`, the canonical stored UUID, not the provider event-ID string used for registration), context digest, assessment policy/version and `complete`/`not_complete` decision. Backend reconstructs persisted correlation under authority checks; it does not independently parse semantic meaning. Other decisions fail closed without order/evidence/receipt mutation. |
| Freshness | Require the presented current snapshot, current material cart/quote/availability/open state and no unresolved edits. A material change requires a new summary, never silent repricing. An intervening clarification alone does not invalidate unchanged order terms. |
| Control | Check current control/version and claim. Existing explicit takeover semantics remain: conversational AI replies stay paused; properly grounded bounded confirmation and system notifications do not themselves return AI control. No takeover HTTP feature is added here. |
| Exactly once | Unique candidate-to-order and consumed-inbound evidence, immutable operation intent and atomic result/receipt prevent duplicate outcomes. Same trusted operation/intent returns the original committed domain result even after the draft advances. Changed intent conflicts; no second receipt/cart recreation. |
| Deferred extras | Existing confirmed contents remain immutable. This initial-submit slice must not silently turn another edit/summary on an already submitted draft into an amendment, linked extra or second initial order. Those workflows remain gated. |

Clear contextual completion after the stored question submits without another confirmation question. “No, that's all” is a positive example; “no onions” is an edit, wanting more continues edits, and “maybe,” silence or unrelated assent never submit. The exact AI-owned FR13 cases are received in the feature task (L13); scripted backend fixtures are not natural-language recognition proof. Shared `PickupCompletionContext.History` contains server-ordered `PickupContextMessage{SourceID,Speaker,Text}` strictly after acknowledged Q0 and before current M. Speaker is `customer`/`restaurant` data, not provider instructions; nil history is unavailable, while a nonnil empty slice requires a successfully established empty window. Bounds are 62 intervening turns and 64 KiB serialized exact assessment input. No silent truncation, last-N substitution or missing-context fallback; the digest must bind the complete window and actual source identities. The implemented domain assembler/digest and imported assessor are distinct from their declarations; their historical self-checks and the new bridge's focused parent RED/GREEN/race/vet do not establish independent connected acceptance.

## Preparation and snapshots

Reuse fixed-menu pricing and `preparation.Calculate` with a transactionally authoritative workload and database clock. Three slots and ten minutes per order account for elapsed preparation. Keep unaccepted, ready, terminal and held work out. Overdue active Preparing work produces an unavailable/null numeric estimate, never zero, automatic readiness or released capacity. Closed blocks submission; busy does not.

The complete stored summary preserves exact names, quantities, per-unit notes, pickup/pay-at-pickup, server-priced total and provisional preparation-only estimate/as-of. Workload-only changes refresh provisional output without changing confirmed material terms. The original draft-only preview remains a separate API; its `workload_not_connected` result must not be mislabeled complete summary authority.

## Staff wire subset

**Canonical selected target: `pickup-staff-v1-r3`.** This documentation revision supersedes r2, not a Git commit, deployed API version or acceptance receipt. Keep the initial OrderSummary unchanged: confirmed snapshot identity stays on detail. Only detail/action results add `permitted_actions`; shared `PickupOrder.PermittedActions` and its domain computation are producer-implemented, but r3 wire projection and main service dispatch remain unimplemented/held. Required independent domain/staff acceptance remains unavailable. Frontend reports no shipped order DTO/HTTP consumer; its absent product-doc copy is not an existing compatibility constraint.

### Routes, scope and bounds

| Go route | Domain port / result data |
|---|---|
| `GET /v1/staff/orders` | `ListOrders`; `{items:OrderSummary[],next_cursor:string|null}` |
| `GET /v1/staff/orders/{id}` | `ReadOrder`; `OrderDetail` |
| `GET /v1/staff/orders/{id}/summaries/{summary_id}/units` | `ReadUnits`; immutable units page belonging to actual order/snapshot |
| `POST /v1/staff/orders/{id}/actions` | `ApplyAction`; resulting `OrderDetail` |

Success is HTTP 200 `{data,request_id}`; safe errors `{error:{code,message},request_id}`; `Cache-Control: no-store`. Same-origin BFF counterparts are `/api/staff/...` targets the frontend must implement and explicitly allowlist; they are not currently existing routes or arbitrary upstream links. There is no public submit, action-status, proof, chat or takeover route.

Go verifies bearer subject and current configured-restaurant membership on every operation, including cached replay. Restaurant is server/store configuration; browser actor/role/restaurant/price/payment-state fields confer no authority. Resolve actual order/snapshot relationships and return scoped 404; no cross-order summary lookup. BFF retains its own authoritative session/origin-CSRF checks.

IDs are canonical UUID-v4. Versions and money minor amounts are nonnegative decimal strings, not JS floating-point values; currency/exponent are server-configured (three uppercase currency letters, exponent 0..6). Times are UTC RFC3339. Page limit default50/max100, nonempty supplied cursor <=2,048 ASCII characters; cursor is server-authenticated and scope/resource-bound. Mutation JSON <=64 KiB, serialized response including envelope <=512 KiB. Strict unknown/duplicate/null/type/shape validation; no silently truncated unit page or nonadvancing cursor.

### Exact selected DTOs

- `Money = {amount_minor:string,currency:string,exponent:integer}`. `amount_minor` is a canonical nonnegative base-10 JSON string (`0` or `[1-9][0-9]*`) bounded by `9223372036854775807` (signed-bigint/int64); no sign, leading zeros, whitespace, decimal point or exponent notation. This preserves the initial scalar contract. `currency` is exactly three uppercase ASCII letters; `exponent` is an integer from 0 through 6.
- `OrderSummary = {id,version,created_at,fulfillment_status,payment_status,fulfillment_kind:"pickup",total:Money}`. `created_at` is server order submission/creation time. No confirmed-ID/capability field is added to the list header.
- `OrderDetail` is that header plus `confirmed`, `payment`, `estimate`, `preparation_started_at:Time|null`, `permitted_actions:Action[]`. Mutation success returns this same detail shape.
- `confirmed = {id,unit_count,units_href,fulfillment:{kind:"pickup",payment_method:"pay_at_pickup"},subtotal:Money,delivery_fee:Money,total:Money,fee_basis:"independent",fee_source_order_id:null}`. Its `id` is the immutable confirmed snapshot ID used by units paging, not a cart selector; pickup delivery fee is zero and subtotal equals total.
- `payment = {state:"unpaid"|"paid",collected_at:Time|null}`; header `payment_status` matches `state`. Unpaid has null collection time.
- `estimate = null | {minutes:integer,as_of:Time,provisional:boolean}`: server-provided captured/provisional preparation-only information, never a client calculation or pickup time. Null means unavailable, not zero/readiness. `preparation_started_at` is a server workflow timestamp.
- `Unit = {id,menu_item_id,name,note,unit_price:Money}`; each immutable unit represents one item. Names/notes/prices are captured snapshot values, not today's menu. No quantity or unsupported choices/contact/customer-label field is introduced.
- Units page `{items:Unit[],next_cursor:string|null,summary_id,unit_count}`. Every page stays on the same immutable `summary_id` even if the order changes; unit count and header total describe complete content, never just a page.
- The sole content link is `units_href="/api/staff/orders/{id}/summaries/{summary_id}/units"`. No deferred-flow links.

### Permitted controls and actions

`Action = accept | reject | start_preparing | mark_ready | mark_picked_up | cancel | mark_paid`. Only OrderDetail/action results return `permitted_actions`; fetch detail before rendering mutation controls, rather than deriving eligibility from the list header. It is a nonnull, unique array of those codes, computed by the server from current membership, state, payment and selected policy at that response's version/read. Empty means no available action. The frontend renders offered controls; it does not reconstruct paid/late eligibility. Codes are hints for that view, not authorization: every POST revalidates current authority/state/version. Cached mutation results may describe an older state; refresh the authoritative detail before another intent.

POST body is exactly `{"action":Action,"expected_version":Version}`; `Idempotency-Key` is required, 1..128 printable ASCII characters without whitespace/control. Result contains the committed authoritative header/version/status/payment and confirmed content. Fulfillment states are `submitted,accepted,preparing,ready,picked_up,rejected,cancelled`; normal progression is submitted→accepted→preparing→ready→picked_up. Rejection is distinct; fulfillment terminality does not invent payment transitions. `mark_paid` records explicitly authorized collection independently, never automatic pickup/ready/cash behavior. Only selected unpaid pre-acceptance cancellation is promised; unresolved paid/late exceptions are not offered and return policy rejection if requested. No delivery/revision/refund actions.

### Conflict, replay and uncertain outcomes

| HTTP/code | Meaning for this attempt |
|---|---|
| 400 `invalid_input` | Invalid request/cursor/shape; no new domain/outbound effect. |
| 401 `unauthenticated` / 403 `forbidden` | Current authority unavailable/insufficient; replay does not bypass it. |
| 404 `not_found` | Scoped absence or wrong order/snapshot relationship. |
| 409 `version_conflict` | Expected version is stale; no new mutation. Refresh, then form a fresh explicit intent if still desired. |
| 409 `invalid_transition` | Action not legal for current state; no new mutation. |
| 409 `idempotency_conflict` | Same scoped key used with a different immutable intent; no new mutation. Do not automatically mint a new key. |
| 413 `payload_too_large` / 415 `unsupported_media_type` | Request rejected without mutation. |
| 422 `policy_unresolved` | Requested exception is gated; no new mutation. |
| 503 `temporarily_unavailable` / 500 `internal_error` | Safe failure response, but a mutation outcome must be treated as uncertain unless independently reconciled. |

A controlled rejection has no new effect for that attempt; it does **not** prove an earlier attempt with that key never committed. Same current-authorized subject/restaurant/resource/key and identical action/expected-version intent replays the original committed data, even when the live order later advances; no second notification/collection effect. Changed intent conflicts. Replayed data is not a current-state read: do not overwrite a newer local version, and refresh GET before further action.

After a POST timeout, disconnect, malformed/missing response or 5xx, retain the exact key/body and show an uncertain result. If retrying to reconcile, use that same key/body under current authorization; never automatically create a fresh intent or repeat physical cash collection. GET can show current state but cannot alone prove which intent committed. The existing same-key POST is the recovery path; no extra status/lookup endpoint is selected. These are target semantics; the producer-implemented domain and historical self-checks are not independent acceptance of the HTTP/service path.

Before commit, domain validates its bounded mutation response/idempotent result in the same transaction as state/result/notification intents. Do not commit success and only then discover an invalid/oversized result. Internal `ErrPickupConfirmationRequired`/`ErrPickupSummaryRequired` are not a new staff/customer submit API.

## Implementation ownership and checks

- Parent: this contract, common `cart/pickup_contract.go`, task document/mirror and shared `cmd/local-staff-api/main.go` wiring.
- Constructor seam: domain implements `cart.NewPickupStore(existingCartStore, restaurantID, assessmentVersion)` returning a facade implementing both ports and borrowing the existing store. Restaurant and accepted assessment version are server configuration. Adapter implements `stafforders.NewHandler(port, verifier)` using the existing `*staff.Verifier`; no new JWT verifier or auth source.
- Domain writer after contract agreement: new `cart/pickup_*.go` implementation/tests excluding the common contract, guarded migration `00013_*` and focused migration tests. Reuse existing private cart authority/transaction helpers; do not weaken old guards or change legacy codecs.
- Adapter writer after fixed port: new `internal/stafforders` handler/tests against a fake `PickupStaffPort`. No migration/cart/main ownership.
- Integration: a reusable standard LOCAL fixture connects persisted messages, trusted edits, actual synthetic receiver callback, offline assessment, one unpaid order, authenticated staff handlers and replay/concurrency assertions. Use `testpg`/`testsocket`; no custom generic proof runner or copied source/dependency trees.

Parent self-checks passed only: functional RED job8/native1 then GREEN job9/native0; one before/after overlay differential preserves ten legacy constructor/codec vectors with exact stdout/stderr/exit equality, full Cart suite0/131.073s, focused codec race0/1.017s and vet0. These do not establish new database registration, lifecycle, or independent proof.

The new `local-pickup-v1` codec uses distinct `prepare_pickup_summary` / `submit_pickup_order` actions and strict canonical reload; it does not extend legacy, draft or preview bytes. The generic cart `Execute` cannot dispatch pickup calls. Domain-owned migration13 implements the execution-call action CHECK extension and rollback refusal when pickup call/history exists; schema12 alone does not permit these registrations. The domain's persisted registration/lifecycle has historical producer self-checks, not independent acceptance from codec unit checks.

The parent-owned `loadRegisteredExecution` helper now separates canonical persisted source/call validation from route-specific job authority. It grants no execution permission. Existing generic, draft and preview executors still use `loadExecution` and the unchanged legacy-only job guard; pickup domain execution pairs the private loader with the existing `postgres.LockMenuExecutionJob`, current control checks and post-blocking clock checks in the same transaction. This extraction has no intended observable behavior change. Parent job12 passed: full Cart suite exit0/134.770s, focused legacy/draft/preview authority race exit0/40.617s and vet exit0, all stderr empty; gofmt/check exit0. These are parent self-checks, not independent or pickup-domain authority acceptance. The pickup domain has producer implementation and historical public self-checks; required independent claim/authority acceptance remains unavailable.

Types-only declarations have no new executable behavior to demonstrate RED; ordinary structural/compile checks cover shared declarations. Historical domain/adapter self-checks and the new bridge's parent-observed RED → GREEN and focused race/vet passes remain scoped evidence, not full acceptance. Fresh implementation assessment and independent verification cover persistence, authorization and concurrency. No new test outcome substitutes for the unavailable old R7 acceptance.

**Not included:** delivery, live providers, revisions/extras/refunds, new host/service activation, publishing, commits/index/ref movement, or reopening stopped proof runs.
