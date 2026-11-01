# API and tool contracts

**Status:** Architect-selected internal design, not implemented or tested. Read [data and transaction contracts](data-contracts.md) first; [REQUIREMENTS](../REQUIREMENTS.md) owns product rules and [ARCHITECTURE](../ARCHITECTURE.md) owns topology/authentication. These are internal application contracts, not verified provider wire formats.

## 1. Staff HTTP surface

Proposed application contract, independent of provider SDKs. Browser uses same-origin Next.js `/api/staff/...`; an explicit allowlist maps only the routes below to Go `/v1/staff/...`. BFF checks authoritative session and origin/CSRF, creates the existing short-lived JWT, never forwards browser-supplied authority headers. Go verifies token/membership itself. Better Auth retains its own auth endpoints; no Go login/token endpoint.

Go derives restaurant from membership/configuration. Customer-scoped resources are never authorized by possession of an ID. Every mutation below requires an `Idempotency-Key`; updates also require the aggregate `expected_version`. Unknown fields, invalid IDs/enums, oversized bodies/text and unexpected content types are rejected; JSON types and application limits are selected in [staff wire schemas](http-schemas.md); provider-specific limits remain separate. Server-calculated fields are output-only.

| Go route | Input / response | Owner / constraints |
|---|---|---|
| `GET /v1/staff/orders?cursor=&limit=` | Opaque page cursor/bounded limit → order summaries + next cursor | Orders; no unbounded customer-data dump. |
| `GET /v1/staff/orders/{id}` | ID → bounded confirmed/status/payment/fee/total header, version/wait/revision/hold and collection links | Orders; full units/notes and related records are retrievable through explicit pages, never silently truncated. |
| `GET /v1/staff/orders/{id}/summaries/{summary_id}/units` | Page → immutable confirmed/history/candidate units and total unit count | Orders; validate actual order-summary relationship; never mix snapshot versions. |
| `GET /v1/staff/orders/{id}/follow-ups` | Page → follow-up headers/candidate links | Orders; original-order ownership, no embedded unbounded units. |
| `GET /v1/staff/orders/{id}/cancellation-requests` | Page → cancellation request records | Orders; request status is not financial settlement. |
| `GET /v1/staff/orders/{id}/related-orders` | Page → directly linked parent/children summaries | Orders; actual links only, not phone-based association or recursive graph. |
| `GET /v1/staff/orders/{id}/messages/{message_id}/text` | Text cursor → exact message fragment/continuation | Conversations; authorize actual message/order relation; no raw provider body. |
| `POST /v1/staff/orders/{id}/actions` | `action`, `expected_version` → updated order | Named actions only: `accept`, `reject`, `start_preparing`, `mark_ready`, `mark_picked_up`, `send_for_delivery`, `mark_delivered`, `cancel`, `mark_paid`; domain transition/coverage gates apply. |
| `POST /v1/staff/orders/{id}/revisions` | `expected_version`, targeted unit IDs and fixed-menu replacement IDs/removals → pending candidate + order view | Orders; no caller prices, direct application or automatic cancellation. |
| `POST /v1/staff/orders/{id}/follow-ups/{request_id}/decision` | Request `expected_version`, original `expected_order_version`, `approve`/`reject`, combined-fulfillment decision → follow-up/candidate | Orders; staff approve concrete extras/feasibility, never caller prices or immediate kitchen mutation. Same-delivery approval determines shared-fee eligibility; customer confirmation and normal order acceptance remain required. |
| `GET /v1/staff/orders/{id}/conversation?cursor=&limit=` | Page → messages/control/version | Conversations; follow stored order/conversation relation. |
| `POST /v1/staff/orders/{id}/messages` | Exact `text`, `expected_version` of conversation → outbound intent/control version | Conversations; preserve exact text in either control mode. Pausing AI requires explicit takeover; sending alone never silently changes control. Messaging-window gate applies. |
| `POST /v1/staff/orders/{id}/control` | `mode: staff | ai`, conversation `expected_version` → control/version | Conversations; explicit takeover/return independent of fulfillment. |
| `GET /v1/staff/catalog?cursor=&limit=` | Bounded menu page → items/continuation, open setting and versions | Catalog; read only prices/menu. |
| `PATCH /v1/staff/catalog/items/{id}/availability` | `available`, item `expected_version` → item/version | Catalog; existing confirmed orders unchanged. |
| `PATCH /v1/staff/restaurant/availability` | `open`, settings `expected_version` → setting/version | Catalog; no schedules/maps or implicit busy closure. |

Staff coverage review is an application precondition of delivery acceptance, not an automatic address check. The accept action carries `coverage_reviewed: true` as a staff attestation; Go requires delivery address/recipient phone and authorized staff. It does not prove geographic coverage. Reject/contact offers pickup without a conversion endpoint.

No fee/menu/price editor is added: their configured seed/setup values are outside staff mutation routes. Read ordering/cursors are server-owned; [bounded reads](bounded-reads.md) defines per-resource sorting, authenticated cursors, response budgets and complete retrieval. Page default/max and cursor validation are specified in the linked staff wire schemas.

### Result, errors and idempotency

- Successful reads/mutations: HTTP 200 with `{data, request_id}`. Outbound-message enqueue: 202 with `{data: {intent_id, state}, request_id}`; it is not a delivery receipt. Error: `{error: {code, message}, request_id}` with safe text, no tokens/SQL/raw provider bodies.
- 413 `payload_too_large`, 415 `unsupported_media_type`, safe 500 `internal_error`; 400 `invalid_input`; 401 `unauthenticated`; 403 `forbidden`; 404 `not_found` for absent/not-accessible scoped resources; 409 `version_conflict`, `invalid_transition`, `summary_required` or `idempotency_conflict`; 422 `policy_unresolved`/`messaging_policy_unresolved` for deliberately gated operations; 503 `temporarily_unavailable` for transient dependencies. Concrete message wording is not selected product copy.
- Scope a staff key to authenticated subject + restaurant + method + canonical resource/action. Store a canonical body fingerprint. Same key/body replays the committed response, including after `expected_version` becomes old; different body yields 409. Authorize before replay so revoked membership cannot read a cached result. Concurrent same-key requests converge through uniqueness/transaction serialization. A rolled-back transaction has no committed mutation/result to replay.
- Webhooks/tools use their own trusted event/snapshot operation identities, never a model-generated arbitrary key. Deduplication-ledger retention must outlive the supported replay window; provider/business retention policy is still unresolved.

## 2. Webhooks and internal tools

`GET /webhooks/whatsapp` handles provider subscription verification only; it grants no staff authority. `POST /webhooks/whatsapp` verifies signature over raw bounded bytes before parsing/routing, normalizes supported inbound/status events and persists receipts/jobs atomically. Verified duplicates acknowledge without recreating work. Return 200 only after durable persistence/no-op; invalid signature 403, malformed supported payload 400, unavailable durable storage 503. Unsupported verified event handling and exact provider verification fields/deadlines require adapter specification; do not invent a provider event-ID field.

Tools are internal Go operations, **not public HTTP endpoints**. Orchestrator binds conversation/sender/event/control version from trusted execution context; model supplies only operation parameters. Validate all supplied IDs against that scope.

| Tool | Model parameters | Deterministic output / authority |
|---|---|---|
| `get_menu` | None | Stored items/prices/availability. Closed browsing awaits its product decision. |
| `edit_cart` | Cart version; add item/count, remove explicit unit IDs, set note on unit IDs | Validated cart/units/version/totals; no arbitrary prices or silent note loss. |
| `set_fulfillment` | Cart version; pickup or delivery address/recipient phone | Validated cart, appropriate offline payment/fee. Cannot convert a confirmed order. |
| `prepare_summary` | Current cart version | Stored snapshot ID, exact complete summary, provisional wait/as-of; present it with the natural anything-else closing question, not a model-rewritten total. |
| `submit_order` | Current snapshot ID | Clear completion after the anything-else question is submission intent, not a request for another confirmation prompt. Require grounded message/current-snapshot checks; otherwise `confirmation_required`, `summary_required` or policy gate. |
| `get_order` | Order ID | Scoped confirmed view; no cross-customer lookup. |
| `respond_to_revision` | Candidate ID; interpreted response | Go checks stored inbound message/evidence. Confirm applies only current candidate; decline never cancels/resumes or returns AI control. Takeover still permits trusted confirmation processing without conversational AI replies. |
| `request_addition` | Scoped original order/version and requested extra fixed-menu units | Unpaid submitted: amendment candidate; paid: linked unpaid-order draft; accepted/preparing: staff review; ready/later: new draft. No original write, fee waiver or kitchen pause without required checks. Clarify ambiguous targets first. |
| `confirm_addition` | Current candidate ID | Verified contextual completion commits same-order amendment or one approved linked supplement. Check current state/version/approval/fee basis atomically; acceptance, payment or trip changes require rerouting/review and a fresh summary. Not an automatic reopening or acceptance. |
| `request_cancellation` | Order ID/version | Before acceptance cancel only when unpaid and no shared settlement is affected; otherwise record staff request. Terminal orders reject; no automatic payment/fee/refund effect. No staff decision impersonation. |

Interpret customer intent naturally from conversation context, not exact phrases or codes. While staff control is active, customer messages still persist; bounded confirmation/cancellation intent interpretation may run without sending AI conversational replies or enabling unrestricted tools. Go verifies source-message/candidate binding, control and state independently of the model's semantic judgment. Recognition evaluation/presentation correlation remains pending; an intervening clarification alone does not invalidate a summary. Control/read authorization applies before model invocation too.

## 3. Traceability and remaining gates

| Rule group | Contract responsibility / planned public check |
|---|---|
| FR-01–FR-05 | Catalog/cart/unit records and tools; browse/direct paths, edits, per-unit note preservation, availability without retroactive changes. |
| FR-06–FR-09 | Fulfillment/settings/summary/acceptance; address/phone/fee, manual coverage, closed submission, busy provisional preparation-only wait. |
| FR-10–FR-12 | Queue/hold/preparation timestamps; synchronized 10/20/30/40 and elapsed-work 24-minute examples, no automatic readiness. Overdue Preparing work makes the estimate unavailable without releasing capacity. |
| FR-13–FR-15 | Snapshot/evidence/unique order, scoped dashboard/actions; ambiguous confirmation rejects, duplicate retries converge, method-specific staff fulfillment. |
| FR-16–FR-17 | Exact staff outbox/control/version; history, takeover/return, independent notifications, chat cannot edit orders. |
| FR-18–FR-21 | Candidate/hold/same-order requeue; unchanged totals until confirmation, decline/silence no apply/timeout/cancel, explicit staff cancellation. |
| FR-22–FR-23 | Cancellation request/terminal checks and independent collection; no inferred cancellation or payment. |
| FR-24 | State-routed amendment/follow-up/supplement, staff decision route and related-order/fee basis; test before/after acceptance, ready/later, ambiguous targets, fee sharing, stale approval and retries. Original work/payment remains unchanged while extras await approval. |
| NFR-01–NFR-04 | Server validation, identity/idempotency, session/token/membership boundaries, messaging-policy adapter. Rejections leave domain data and outbound effects unchanged; security failure logs may still be recorded. |

Follow-up requests appear in the existing order dashboard/detail, not a new delivery-management app. Preserve the normal open/availability/payment rules for additional submissions. On staff rejection leave the original unchanged; offer an independent order only with fresh customer confirmation. An amendment retry replays its committed version/result; supplement creation is unique per follow-up/candidate. Shared fee is server-derived from approved original-trip linkage, not a fee override field. No new automatic chat takeover/return. Paid contents/totals cannot be amended; `request_addition`/`confirm_addition` route extras to a linked unpaid order and recheck payment under lock. Paid/shared-delivery cancellation requires staff resolution of amounts with the customer; action/tool calls cannot automatically add fees, transfer payment or promise refunds.

Next detail gates: staff offline-settlement recording/combined departure, executable DDL/query plans/migration ordering (see [schema design](postgres-schema.md)), input limits, contextual confirmation evaluation/presentation correlation, late-stage cancellation eligibility and staff exception procedures, partly prepared work, stale in-flight sends, provider event normalization/reconciliation/windows/templates, auth configuration, retention and model compatibility. [Staff wire schemas](http-schemas.md) define the current request/response records, numeric-string encoding, limits and literal examples. [Bounded reads](bounded-reads.md) resolves detail/collection/text response bounds; the listed semantic gates still need resolution before implementation; no live integration or executable test is claimed here.
