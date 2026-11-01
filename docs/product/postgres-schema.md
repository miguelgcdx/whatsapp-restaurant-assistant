# PostgreSQL schema design

**Status:** Architect-selected persistence design, not executable DDL, applied migrations or tested database behavior. [Data contracts](data-contracts.md) own business identities/states/transactions; [API contracts](api-contracts.md) own operations; [REQUIREMENTS](../REQUIREMENTS.md) owns all 28 rules. This document maps those contracts to tables, keys and indexes without resolving outstanding business/provider policies.

## 1. Conventions and migration ownership

Use PostgreSQL schema **`app`** for Go-owned business tables. Goose creates/version-controls only that schema and its migration bookkeeping; configure the bookkeeping location explicitly so it cannot collide with auth migrations. Better Auth owns its own records/migrations; its actual namespace/table configuration is still a separate integration gate. No auth table definitions, passwords, credentials or real fixture records belong here.

Application primary keys are server-generated `uuid`; external/provider/auth IDs are `text`. `staff_memberships.auth_user_id` is an opaque external subject, not a foreign key into library-private auth tables. Library session validation precedes Go token/membership checks; membership alone never creates identity. Supported provisioning creates the auth user before membership, without a cross-owner cascade. User deletion/revocation handling must be verified with auth configuration; the existing short token window remains explicit.

Default columns where applicable: `id`, `restaurant_id`, `created_at`/`updated_at` (`timestamptz`), and `version` (`bigint`, nonnegative). Foreign-key targets are actual application tables, never model output or arbitrary strings. Give each restaurant-scoped target a unique `(restaurant_id, id)` key and use composite references where needed to prevent cross-scope links, including fixtures. This is consistency protection for one configured restaurant, not tenancy administration or a claim of PostgreSQL RLS.

Use `text` with check-constrained state values rather than PostgreSQL enums, keeping later migrations explicit. Money uses nonnegative `bigint` minor units plus configured currency/exponent; versions, attempts and sequence values are nonnegative, with positive sequence allocation for queued work. Notes/text are bounded at input boundaries; exact limits remain configuration work. UTC dates never automatically change food readiness or cancel a proposal.

## 2. Tables and key relationships

Columns below are load-bearing fields, not every implementation column. Foreign keys default to restrictive deletion; do not cascade away paid records, messages, evidence or recovery state. A dedicated retention design must precede deletion jobs.

### Catalog, access and conversations

| Table | Important fields / references | Keys and checks |
|---|---|---|
| `restaurants` | Open flag, currency/exponent, flat fee, version, next queue sequence | Configured singleton selected by server, not browser; nonnegative fee. Setup chooses menu/currency, not migrations. |
| `menu_items` | Restaurant, fixed name/price/currency, available, version | Unique scoped ID; currency agrees with restaurant through domain validation. No staff price editor. |
| `staff_memberships` | Restaurant, opaque auth user ID, active | Unique `(restaurant_id, auth_user_id)`; no application passwords or new role hierarchy. |
| `conversations` | Restaurant, configured business-number ID, customer sender ID, control mode/version; processing lease owner/deadline/generation, next inbound sequence | Unique restaurant/business-number/sender identity; mode `ai` or `staff`. Processing lease and conversational control are different fields. |
| `messages` | Conversation, direction, source (`customer`, `staff`, `ai`, `system`), exact text, provider ID if known; inbound-event/outbox reference | Scoped conversation FK; stable local ID even before provider ID exists. Delivery statuses live with send attempts, not customer text deduplication. |
| `carts` | Conversation, optional target original order, purpose (`new`, `amendment`, `addon`), version, fulfillment/address/phone, state | Draft can be incomplete; no application-wide unique customer cart that would silently select among active orders. Source order must belong to conversation. |
| `cart_units` | Cart, stable unit ID, menu item, unpriced note | One row per unit; counts are derived for display. Removing one unit never implicitly removes another unit's note. |

An order's conversation reference remains explicit when multiple orders share a chat. If customer intent does not identify a target, clarify rather than guessing by timestamps or selecting the most recent order automatically.

### Immutable candidates, orders and requests

| Table | Important fields / references | Keys and checks |
|---|---|---|
| `summaries` | Conversation; kind `submission`/`amendment`/`revision`/`addon`; cart/source version or target order/base version; optional follow-up request; exact summary, fulfillment/payment method, fee basis/source, subtotal/fee/total/currency, provisional wait/as-of, validated unit count | Immutable candidate details; kind requires appropriate typed source FKs, not an unenforced polymorphic ID. Nonnegative amounts; checked subtotal + fee = total. Presentation is established through the summary's outbox/attempts, not a model boolean. |
| `summary_units` | Summary, stable unit ID, catalog reference, confirmed name/unit-price/currency/note snapshots | Unique `(summary_id, unit_id)`. Do not reconstruct historical names/prices/notes from today's catalog. Server verifies unit count and price sum against summary metadata in the creation transaction. |
| `confirmation_evidence` | Summary, verified inbound message, assessment/policy version, source conversation/control version, consumed operation/result | One consuming evidence row per inbound message; candidate belongs to that message's conversation. Store grounded semantic assessment, not invented customer text. Rejected/ambiguous interpretation does not consume authorization or submit. |
| `orders` | Conversation; unique originating creation-summary ID, current confirmed summary ID, fulfillment state/version, payment state/collection actor/time; queue sequence, preparation start, preparation hold; related original order if supplemental | One creation candidate → one order; amendments change current summary pointer, never originating identity. Paid confirmed pointer is immutable through application operations. Self-related order prohibited. Normal status progression remains available after payment. |
| `revisions` | Order/base version, candidate summary, response, active flag, proposed actor; hold belongs to order | Partial unique active revision per order. A decline can retain the hold while allowing a later explicitly superseding candidate; never automatically resume or time out. Cannot apply to paid contents/totals. |
| `follow_up_requests` | Original order/base version, source message, request state, candidate; staff decision/version, approved candidate/fulfillment and actor | Unique trusted source operation, not item text. Approval binds concrete items/fee basis and versions. Pending addition never sets original preparation hold. Paid-before-acceptance independent extras need no invented staff permission, but sharing a trip requires approval. |
| `cancellation_requests` | Order, verified source message/operation, state, staff decision/actor/time | Idempotent source operation; paid/shared-delivery or accepted cancellation is a staff request, not automatic cancellation/settlement. Terminal rules still apply. |
| `order_history` | Order, prior/result version, operation, actor kind/reference, event kind/time, summary references where changed | Unique order/result version for aggregate changes. Append history with the mutation. This is audit/history, not event-sourced state or a complete accounting ledger. |

`orders.current_summary_id` references normalized `summaries`/`summary_units`: no second mutable order-item/total copy. Fulfillment/payment live on the order, independently of its confirmed content. Reads join the immutable current snapshot; amendment history retains old snapshot references. Materialize no separate read-model service.

Summary kind constraints: initial submission references a cart; amendment/revision references an existing order/base version; add-on references a follow-up/original and candidate source. Application checks agree on conversation, item availability, currency and source version. Cyclic references are created safely within one transaction: insert the request, then its candidate, then approval/link pointers. No externally visible half-approved request.

Confirmed delivery summaries require address/recipient phone, appropriate offline payment, and fee basis. Pickup requires zero delivery fee and pay-at-pickup; independent delivery uses the configured flat fee. `shared_original_delivery` requires zero additional fee, a different related original order and staff-approved same-trip linkage. Cross-row feasibility and original-trip status are application checks under lock, not a misleading standalone SQL check. A zero configured independent fee must not be mistaken for a shared trip.

### Durable execution and outbound delivery

| Table | Important fields / references | Keys and checks |
|---|---|---|
| `inbound_events` | Business-number scope, adapter-normalized event kind/dedup key, sender/provider references, received time, normalized bounded payload | Unique `(restaurant_id, business_number_id, event_kind, dedup_key)`. Keys come from verified provider data, not message text or an assumed universal provider event ID. Raw-body retention remains open. |
| `operations` | Actor/scope, canonical action/resource, idempotency key/fingerprint, result status/body/resource references | Unique scope/action/key. Staff replay requires current authorization; fingerprint mismatch conflicts. Result and domain changes commit together. No persisted in-progress operation in a separate transaction that can strand a key. |
| `jobs` | Origin event/operation and work kind, conversation/sequence, due time, state/attempt, lease owner/deadline/generation, safe failure code | Unique source/work kind so one event may have different intentional jobs without duplicate same-kind work. Source refs are typed FKs. Leased state requires owner/deadline; no network I/O within claim transactions. |
| `outbox` | Operation + effect role/ordinal, conversation/recipient, kind/content, optional summary/order, expected AI control version, due time/state | Unique operation/effect role/ordinal. Only current domain transaction creates an effect; equal text is not deduplication. Link summary presentation without circularly requiring an outbox row before candidate creation. |
| `send_attempts` | Outbox, attempt number/generation, start/finish, outcome, provider message/reference if known, safe error code | Unique outbox/attempt number; outcome unknown is explicit, not equivalent to definite rejection or safe retry. Delivery callbacks reconcile provider references through the adapter. |

Create a received-message row with its inbound receipt/job in the ingestion transaction. An outbound message is linked to its outbox intent; attempts record provider outcomes without rewriting exact authored text. Meta callbacks may be duplicated or out of order; state mapping remains an adapter gate, never a naive last-callback-wins design.

`operations` identifies a committed business request; `jobs` executes durable work; `outbox` identifies logical external effects; `send_attempts` records actual delivery attempts. These are distinct facts, not competing queues or a generic workflow engine. None promises exactly-once external delivery.

## 3. Indexes justified by current queries

Use ordinary B-tree indexes unless the actual query plan proves otherwise. No blanket JSONB/GIN indexes or analytics warehouse.

| Query | Planned index / constraint |
|---|---|
| Staff membership check | Unique restaurant/auth-user pair. |
| Customer conversation lookup | Unique restaurant/business-number/sender tuple. |
| Conversation history | `(conversation_id, created_at, id)` for stable cursor paging; provider-message reference index for callback correlation. |
| Dashboard order list | `(restaurant_id, created_at, id)`; bounded paging. Add status filtering index only with an actual selected filter/query. |
| Snapshot units / related reads | Unique `(summary_id, unit_id)` pages immutable units; `(order_id, created_at, id)` for cancellation requests, `(original_order_id, created_at, id)` for all follow-ups, `(related_order_id, created_at, id)` for child orders; parent via primary key. Menu paging uses `(restaurant_id, created_at, id)`. |
| Current preparation workload | `(restaurant_id, queue_sequence)` partial index for accepted/preparing work not on hold; no indexes that treat elapsed time as a status change. |
| Active revisions / pending additions | Unique active revision per order; `(original_order_id, state, created_at)` for follow-up review. |
| Idempotency / inbound ingestion | Unique scoped operation key, event dedup tuple, order creation-summary ID and consumed inbound confirmation message. |
| Worker drain / recovery | `(due_at, id)` partial index for runnable/delayed jobs, lease-deadline index for leased jobs; conversation/sequence lookup for single-flight ordering. |
| Outbound dispatch / reconciliation | Due/state index for pending effects; provider-reference index on attempts; unique logical effect key. Never include outcome-unknown in blind retry queries. |

Message-text continuation loads the already-authorized immutable message by ID; no substring search index or new text-copy table. See [bounded staff reads](bounded-reads.md) for response/cursor rules. Index uncovered FK lookups. Derive workload on demand, not a continually refreshed wait/queue cache, so idle compute can sleep.

## 4. Transaction and migration checks

Follow [atomic operation contracts](data-contracts.md#4-atomic-operations-and-confirmation), not a second SQL-owned workflow. Constraints protect identity/shape; Go validates child-row price sums, semantic evidence, paid-content immutability and cross-record control/fee rules. Serialize amend/accept/collect on the order row/version; never repurpose stale confirmation. Conditional lease generations protect DB completion, not an already-in-flight Meta send.

Goose owns business namespace/tables/constraints/indexes, not auth migrations. Validate supported PostgreSQL/tool versions, namespace privileges, auth configuration and combined ordering before generating executable files. Seed menu/settings/fictional staff only in authorized isolated tests; no assumed currency/prices, real Neon seeding, destructive rollback or automatic production startup migration.

Future isolated PostgreSQL checks: concurrent uniqueness/replay, scoped FK failures, checked money arithmetic, per-unit notes, paid immutability, acceptance/payment races, independent supplement balances/fees, rejection without domain/outbound effects and restart/lease recovery. None has run.

Remaining gates: executable DDL/query plans, input limits, auth/provider configuration, confirmation grounding/evaluation, partly prepared work, late cancellation/manual settlement, retention/deletion and operations. Never encode an unresolved rule as a default/timer.
