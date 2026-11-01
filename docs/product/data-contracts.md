# Data model and transaction contracts

**Status:** Architect-selected internal design, not implemented, migrated or tested. Product rules remain in [REQUIREMENTS](../REQUIREMENTS.md); topology/authentication remain in [ARCHITECTURE](../ARCHITECTURE.md). This defines logical records and application contracts, not vendor payloads or executable SQL/OpenAPI. Read [API/tool contracts](api-contracts.md) for operation surfaces, traceability and remaining gates.

## 1. Identity and value conventions

- Application IDs: server-generated UUID v4, opaque to callers. Better Auth user IDs and provider identifiers remain opaque strings, not UUIDs. Customer identity is the verified WhatsApp sender within the configured business-number context, never a tool argument granting access.
- One configured restaurant; derive scope from server configuration, verified webhook routing or authorized staff membership. No caller-selected restaurant or multi-restaurant feature. Store restaurant references to enforce ownership, not to expose tenancy administration.
- `version`: nonnegative integer incremented on aggregate mutation; [staff JSON](http-schemas.md) encodes versions as canonical decimal strings, like money, without changing internal integer semantics. Mutations carry `expected_version`; compare under transaction/row lock. Timestamps use UTC/RFC3339 at boundaries, PostgreSQL timezone-aware timestamps internally. Use a monotonic clock for in-process durations, persisted UTC deadlines for restart recovery.
- Money: nonnegative integer minor units plus configured currency; checked integer arithmetic, no floating point. JSON represents `amount_minor` as a decimal string to avoid JavaScript integer loss. Currency/exponent/menu/fee are setup decisions, not assumed USD or two decimals. Go calculates line subtotal, flat fee and total; callers cannot set prices/totals/payment status.
- Model each cart/order unit separately with stable unit ID, menu-item reference, note and price snapshot. Group equivalent units for display only. Two burgers with one note remain two addressable units; quantity edits name affected units so removal never randomly discards a note. Notes cost nothing.

## 2. Logical records and ownership

Names here are logical; [PostgreSQL schema design](postgres-schema.md) maps them to planned tables/types/keys/indexes. Executable migrations follow later. Foreign keys prevent dangling application references; unique/check constraints reinforce, not replace, domain validation.

| Owner / record | Key data and relationships | Required invariant |
|---|---|---|
| Better Auth: users/sessions/keys | Library-owned records | Only supported library APIs/migrations; no Go password/session implementation. |
| Access: staff membership | Restaurant + opaque auth user ID; active membership | Unique pair; Go checks membership on each staff operation. No new admin hierarchy. |
| Catalog: restaurant/settings | Open flag, currency, flat delivery fee; settings version | One configured restaurant; busy never changes open flag. |
| Catalog: menu item | ID, name, fixed price/currency, available flag, version | Fixed catalog; staff can toggle availability, not edit prices/menu. |
| Conversations: conversation/message | Restaurant/business number/sender; control mode/version; messages with direction, source, exact text, provider reference | Unique conversation identity; `ai` or `staff` control independent of order/payment. Retain customer and exact staff text. |
| Cart: cart/units | Conversation, cart version, fulfillment details, unit rows/notes | Draft only; pickup has no delivery fee; delivery needs address/recipient phone. |
| Orders: confirmation snapshot | ID, kind (`submission`/`amendment`/`revision`/`addon`), subject ID/version, exact summary, units, fulfillment/payment/fee/total, estimate/as-of, originating outbound intent | Server-owned immutable candidate; later accepted evidence must identify this candidate. Refresh creates a new ID, never mutates an already-presented candidate. |
| Orders: confirmation evidence | Snapshot ID + verified inbound message ID, validation-policy version, result | Record verified source message, current candidate and contextual intent assessment; a free-standing model `confirmed: true` is insufficient. Same message cannot authorize different snapshots. Evaluation details remain gated. |
| Orders: order/units | ID, source snapshot ID, conversation, fulfillment status/version, confirmed details, unpaid/paid, queue sequence, preparation timestamps | Unique submission snapshot → one order. Unpaid confirmed units/data change only through confirmed amendments/revisions. Paid contents/totals remain immutable; normal fulfillment may progress. Supplemental orders reference the original; no reopening. |
| Orders: revision/units | Existing order ID/base version, candidate snapshot, response (`pending`/`confirmed`/`declined`/`superseded`), preparation hold | At most one actionable candidate per order; proposal preserves active order items/total. Decline does not release hold, resume work or cancel. |
| Orders: follow-up request | Original order/version, added units, candidate, staff decision/version, fulfillment feasibility and fee basis | Unique source-message operation; pending request never changes/pauses original kitchen work. Approval binds the concrete candidate/version, not blanket permission. |
| Orders: transition/collection history | Order ID, actor, operation ID, prior/resulting version, transition/time | Record actual changes atomically; payment collection independent of fulfillment. Not event sourcing. Retention pending. |
| Ingestion: inbound receipt | Business number/provider message identity, verified routing, normalized event kind, received time | Unique inbound customer-message identity; callbacks/status events have distinct adapter-normalized deduplication keys. Never deduplicate unrelated events by text alone. |
| Worker: job | Inbound/operation reference, conversation sequence, due time, state, attempt, lease deadline/generation | One durable job per applicable inbound event; bounded claims, persisted recovery state. |
| Messaging: outbox/attempt | Logical effect ID, source operation, recipient/conversation, kind, immutable content, control version for AI, state, attempts/provider reference | Unique logical effect, not unique message text; original retry never creates another logical effect. No exactly-once external-send claim. |
| Operations: idempotency result | Scope/key, request fingerprint, operation/result/resource IDs | Same key + different request rejected; committed result can be replayed without another mutation/outbox effect. Retention pending. |

Address/phone/message content are sensitive business data, not JWT claims or log fields. Application history does not justify retaining raw provider bodies indefinitely; raw payload policy remains open.

Auth tables remain library-owned. Goose uses the `app` business namespace; staff membership stores an opaque auth user ID without a foreign key into library-private tables. Validate actual auth namespace/configuration, provisioning/deletion behavior and combined migration ordering before executable migrations. Do not run business migrations against auth tables.

## 3. Independent state and transitions

| Dimension | Allowed design transitions / constraints |
|---|---|
| Fulfillment | `submitted → accepted | rejected | cancelled`; `accepted → preparing`; `preparing → ready`; pickup `ready → picked_up`; delivery `ready → out_for_delivery → delivered`. Staff own acceptance/progression; customers may cancel only before acceptance. Rejected, cancelled, picked-up and delivered are terminal. |
| Payment | Start `unpaid`; authorized staff collection action → `paid`. No fulfillment/chat transition collects cash; repeating the same collection operation is a no-op. Paid contents/totals are immutable; extras start a linked unpaid order. No automatic payment transfer, extra cancellation fee or refund promise; staff settle paid/shared-delivery cancellations with the customer. No unpay/refund workflow selected. |
| Conversation | `ai ↔ staff` by explicit staff operations. Returning AI requires staff action; confirmation, payment and fulfillment never change control mode. |
| Revision | Proposal places accepted work on preparation hold and creates a candidate. Explicit confirmation updates the same order and sets accepted/waiting with a new tail sequence and recalculated wait. Decline marks the response but preserves hold/items/total until staff follow up, propose again or cancel. No timeout. |
| Cancellation request | Verified customer request after acceptance, or involving payment/shared delivery, records a pending staff decision, not automatic `cancelled` or settlement. Staff cancellation after acceptance is subject to the unresolved late-stage/refund policy. |
| Worker/send | Jobs: runnable → leased → completed, delayed retry or failed. Expired lease can be reclaimed. Outbound effects distinguish pending, sending, provider-accepted, delivered, failed, outcome-unknown and suppressed stale AI work; provider acceptance is not delivery. Mapping/reconciliation policy pending. |

`completed` is a view of `picked_up`/`delivered`, not another status to set. `Awaiting customer response` is a dashboard preparation-hold view, not a replacement for fulfillment/payment/control. Hold excludes the order from workload; decline does not resume preparation. Once the response is declined, show that fact without pretending another customer answer has arrived.

Staff cancellation from accepted/preparing/ready/out-for-delivery and proposals involving partly prepared food need the pending product decisions before those paths are enabled. This is an implementation gate, not a newly selected eligibility rule. Never infer discarded effort, refunds, overdue-slot release or delivery-to-pickup conversion.

Queue sequence is allocated transactionally for acceptance/requeue. Preparing start/elapsed effort and accepted waiting work feed the three-slot/ten-minute model; do not run the synchronized baseline formula for arbitrary runtime work. Keep ready/terminal/held/unaccepted work out. Read FR-10–FR-12 for the 24-minute example. Non-held Preparing work at or beyond ten minutes makes the numeric estimate unavailable until staff update the workload; it never releases a slot automatically.

## 4. Atomic operations and confirmation

All adapters call the owning Go operation; no generic `update_order`/`set_status` tool or SQL passthrough. Within one short transaction: load scope/version/control, validate, apply state/history, store idempotent result and outbound intents. External calls occur only after commit. Lock order and related conversation/control consistently; concrete SQL/lock ordering remains an implementation detail to test.

### Submission

1. Prepare a server-priced snapshot and persist its complete summary/outbound intent followed by the natural closing question: would the customer like anything else? Store provisional wait with `as_of`; it is not an acceptance promise. An unsent/failed summary cannot be treated as presented.
2. The assistant interprets natural customer intent against the conversation and current summary, like an employee; no keyword/code parser. For initial submission, clear completion in response to the closing question confirms the current summary; no second confirmation prompt. Wanting more continues cart edits and refreshes the snapshot/question. “No onions” is an edit, not completion; silence/ambiguity is not confirmation. An intervening clarification does not invalidate an unchanged summary. Go binds the assessment to a stored verified message/candidate and checks scope, versions and prior consumption; it validates state/authority, not natural-language meaning independently. Unrelated assent, unresolved edits or ambiguous references require natural clarification. Changed order details require a revised summary and fresh confirmation. Grounding/presentation correlation and semantic evaluation remain implementation gates.
3. On submission, recheck open state, applicable availability, fulfillment/payment and prices against the candidate. Never silently substitute a changed total. If material data changed, return `summary_required` and obtain fresh confirmation; workload-only estimates remain provisional and are refreshed in output.
4. Insert order/units unpaid, evidence consumption, result and receipt intent atomically. Uniqueness on submission snapshot makes retries return the original order even after the cart advances. A repeated tool call must not recreate the cart/order.

### Revision

Staff proposal locks order/version, preserves its active units/total, stores candidate/hold and summary intent atomically. Confirmation locks the same order and current candidate, checks ownership/evidence/version, replaces active units/totals, appends history, allocates a new queue-tail sequence and stores notification intent. Order ID, payment state and conversation control are unchanged. Reject amendments/revisions of paid confirmed contents/totals; extra items use a linked unpaid order. Staff resolve paid-order exceptions with the customer outside an automatic refund/transfer workflow.

### Later additions (FR-24)

Before acceptance of an unpaid order, prepare an amendment against the submitted order/version, preserving confirmed details until customer completion of the refreshed summary. Atomically update the same order/units/total/history/version, dashboard update and receipt intent. Acceptance competes on that version; if acceptance wins, return a conflict and route the addition for staff review, never overwrite. Recheck payment in the same transaction: if collection wins the race, do not amend the paid order; route extras to a linked unpaid order with a fresh summary.

A paid original always routes extras to a linked unpaid order, even before acceptance; shared-trip fee eligibility still needs staff approval. After acceptance/preparation, persist a follow-up request; do not change or pause original kitchen work, queue, total or payment. Staff approve/reject concrete items and combined-fulfillment feasibility. Customer completion of the approved summary/extra total creates one linked **supplemental order** with independent unpaid/preparation state; retries return that ID. It enters workload only on normal staff acceptance. Feasibility approval is not fulfillment acceptance or a promise of the original ETA. This addition path does not invoke the replacement/removal hold/requeue workflow.

Persist `related_order_id` and `fee_basis` (`independent` or `shared_original_delivery`) on candidate/order. Shared delivery requires staff approval, same address/recipient and a feasible original trip: zero additional fee, reference the original fee, never a model-selected discount or original-total change. Independent delivery uses the flat fee; pickup zero. Recheck original status, approval and candidate/version at confirmation. If sharing is no longer feasible, explain the separate-order alternative and get fresh confirmation of its fee; never silently charge.

Ready/out-for-delivery/completed originals are not reopened: create a new independent order, optionally linked for conversational context. Clarify ambiguous targets. No inherited payment, readiness or cascade cancellation. Paid/shared-delivery cancellation routes to staff to resolve amounts with the customer, never automatic fee addition, payment transfer or refund promise. Exact offline settlement recording and combined-departure coordination remain gates; do not invent refund or dispatch infrastructure.

### Takeover and sends

Takeover increments control version and invalidates unsent AI conversational intents; status notifications and exact staff messages remain independent. Dispatch checks current control/version and lease generation. This prevents known-stale dispatch, not the race after the last check: already-in-flight messages cannot be recalled. User-visible in-flight ordering remains a gate; no promise of perfect fencing across Meta.

Provider timeouts with unknown send outcome do not trigger blind resend. Preserve attempt/reference for reconciliation. Workers cannot treat an expired lease as proof the old sender stopped; conditional generations fence database completion only.

