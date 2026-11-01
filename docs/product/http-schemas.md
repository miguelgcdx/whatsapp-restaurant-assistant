# Staff API wire schemas

**Status:** Selected application wire design, not handlers, generated types, OpenAPI validation or integration-test evidence. [API contracts](api-contracts.md) own routes/authority/idempotency; [data contracts](data-contracts.md) own transitions; [requirements](../REQUIREMENTS.md) own behavior. No vendor webhook/tool-calling format is specified here.

## 1. Encoding, limits and common types

Notation: `Type[]` is an array, `A | B` a tagged alternative, `T?` a nullable response field. Named records list all required fields. Requests reject missing/unknown fields, duplicate JSON keys, invalid variants and unexpected nulls; optional request fields are explicitly identified. Responses include listed nullable fields as null; clients may ignore future added response fields. No database/provider objects cross this boundary.

- Staff mutation requests use `Content-Type: application/json`, valid UTF-8 and at most **64 KiB raw body** at BFF and Go. Reject unsupported content type with 415 `unsupported_media_type`, oversized body with 413 `payload_too_large`; malformed/invalid fields with 400 `invalid_input`. These are conservative application limits, not claims about Meta's limits.
- `ID`: canonical application UUID-v4 string. `Version` and money `Minor`: canonical nonnegative decimal **strings** (`0` or a nonzero digit followed by digits), at most signed PostgreSQL bigint maximum. JSON numbers are not accepted for these fields. Internal version/money arithmetic remains checked integer arithmetic; strings avoid JavaScript precision loss. A count/page limit is a bounded JSON integer, not a version.
- `Time`: UTC RFC3339 string. `Money`: `{amount_minor: Minor, currency: string, exponent: integer}`. Currency/exponent come from configured restaurant data; no request selects them. No assumed USD/two decimals or chosen sample prices.
- `Unit`: `{id: ID, menu_item_id: ID, name: string, note: string, unit_price: Money}`. Notes may be empty; units have quantity one, grouped only for display. `Estimate`: `{minutes: integer, as_of: Time, provisional: boolean}` with nonnegative minutes; provisional until staff acceptance, always preparation-only. An unavailable/undefined estimate is null, never an invented zero or readiness promise.
- `Fulfillment` is exactly one variant: `{kind: "pickup", payment_method: "pay_at_pickup"}` or `{kind: "delivery", payment_method: "cash_on_delivery", address: string, recipient_phone: string}`. No delivery address/phone fields in pickup objects.
- Menu/confirmed item names are nonblank and ≤200 Unicode code points at setup/ingestion; never clip persisted confirmed content. Staff text is nonblank, at most 4,000 Unicode code points; preserve authored content without trimming/rewriting. Notes ≤500 code points, delivery address nonblank ≤1,000, phone nonblank ≤128; normalization/country validation remains adapter/setup design. Requests never imply geographic coverage.
- Revision change arrays contain 1–100 entries with no repeated target unit; this bounds one request, not a new maximum restaurant order size. Fixed menu availability and unit scope are checked server-side.
- Mutations require a nonempty `Idempotency-Key` of 1–128 printable ASCII characters (no whitespace/control characters). Go derives replay scope, resource and actor; browsers never submit restaurant/user/permission/price/total/payment-state fields. The BFF creates the existing short-lived Go bearer token itself.

Pagination: optional `limit` query parameter defaults to 50, permits integers 1–100; `cursor` is an opaque nonempty string ≤2,048 ASCII characters when supplied; omit it for the first page, never send an empty placeholder. Reject duplicate/unknown query parameters. Cursors are server-authenticated and bound to scope/resource/sort position under [bounded reads](bounded-reads.md). Live collections use `(created_at, id)`, immutable units use their named summary/unit ID, and message text uses character offset. Malformed/wrong-resource cursors → 400. Live paging is not a frozen snapshot.

## 2. Response records

All staff responses carry an opaque server-generated `request_id`. Reads/domain mutations use `{data: Record, request_id: string}`; collection pages use `{data: {items: Record[], next_cursor: string?}, request_id: string}`. GET success is 200, never an outbound-delivery acknowledgement.

| Record | Required fields and meaning |
|---|---|
| `OrderSummary` | `id: ID`, `version: Version`, `created_at: Time`, `fulfillment_status: Status`, `payment_status: unpaid | paid`, `fulfillment_kind: pickup | delivery`, `total: Money`, `preparation_hold: boolean`, `related_order_id: ID?`, `pending_follow_up_count: integer`. |
| `ConfirmedSnapshot` | `id: ID`, `unit_count: integer`, `units_href: string`, `fulfillment: Fulfillment`, `subtotal: Money`, `delivery_fee: Money`, `total: Money`, `fee_basis: independent | shared_original_delivery`, `fee_source_order_id: ID?`. Exact stored confirmed details, not today's menu prices. Shared delivery fee is zero additional with an authorized original reference. |
| `Payment` | `state: unpaid | paid`, `collected_at: Time?`. Staff actor is audited server-side; no browser-selected collector or payment inheritance. |
| `RevisionView` | `id: ID`, `version: Version`, `response: pending | confirmed | declined | superseded`, `candidate: Candidate`, `active: boolean`. Preparation hold remains a separate order field; no deadline/auto-cancel field. |
| `Candidate` | `id: ID`, `kind: submission | amendment | revision | addon`, `base_order_version: Version?`, `snapshot: ConfirmedSnapshot`, `estimate: Estimate?`. Snapshot ID equals candidate ID. Candidate is a bounded header; exact summary text remains stored, not repeated in list payloads. |
| `FollowUpView` | `id: ID`, `version: Version`, `original_order_id: ID`, `state: awaiting_staff | approved | rejected | awaiting_customer | confirmed | superseded`, `staff_approval_required: boolean`, `fulfillment_plan: independent | same_pickup | same_delivery | null`, `candidate: Candidate?`, `resulting_order_id: ID?`. Approval of feasibility is not acceptance of a supplemental kitchen order. |
| `CancellationRequestView` | `id: ID`, `state: pending_staff | resolved | rejected`, `created_at: Time`. Settlement amounts/refunds are not automatically inferred from a resolved flag; exact staff resolution workflow remains gated. |
| `OrderDetail` | All `OrderSummary` fields plus `confirmed: ConfirmedSnapshot`, `payment: Payment`, `estimate: Estimate?`, `preparation_started_at: Time?`, `revision: RevisionView?`, `follow_ups_href: string`, `cancellation_requests_href: string`, `related_orders_href: string`. Duplicate summary/detail payment/total fields must agree. Terminal/held work never contributes to wait. |
| `ConversationControl` | `conversation_id: ID`, `mode: ai | staff`, `version: Version`. Do not return lease keys or bearer tokens. |
| `Message` | `id: ID`, `created_at: Time`, `direction: inbound | outbound`, `source: customer | staff | ai | system`, `text: string`, `text_next_cursor: string?`, `text_href: string`, `intent_id: ID?`. Text is a first fragment, ≤4,000 code points; continuation retrieves the remainder exactly. Private provider payload/credentials excluded. |
| `MenuItem` | `id: ID`, `name: string`, `price: Money`, `available: boolean`, `version: Version`. No price-edit request. |
| `RestaurantAvailability` | `open: boolean`, `version: Version`. This is open/closed, not busy/workload. |
| `Catalog` | `items: MenuItem[]`, `next_cursor: string?`, `restaurant: RestaurantAvailability`. Bounded fixed-menu page, no editor. |

`Status` is exactly `submitted | accepted | preparing | ready | out_for_delivery | picked_up | delivered | rejected | cancelled`. Completion is derived from picked-up/delivered, never another writable state. An awaiting-revision label derives from hold/revision state, not a status override.

[Bounded reads](bounded-reads.md) specifies the **512 KiB serialized, uncompressed response limit**, count/byte-limited pages, scoped cursors and exact long-text continuation. Detail and mutation responses contain headers/links, not unbounded nested arrays. Collection links are allowlisted same-origin BFF URLs, never arbitrary upstream URLs.

## 3. Endpoint-specific requests and results

Every body below lists exactly the permitted fields. `expected_version` refers to the named aggregate; not all order-linked routes version the order itself. Successful domain mutations return 200; outbound sends return 202. Replays return the committed status/payload without another mutation/effect after current authorization.

| Route | Request | Success `data` |
|---|---|---|
| `GET /v1/staff/orders` | Pagination only; no body. | Page of `OrderSummary`. |
| `GET /v1/staff/orders/{id}` | Scoped order ID; no body/query. | `OrderDetail`. |
| `GET /v1/staff/orders/{id}/summaries/{summary_id}/units` | Pagination; scoped confirmed/history/candidate summary. | `{items: Unit[], next_cursor: string?, summary_id: ID, unit_count: integer}`. |
| `GET /v1/staff/orders/{id}/follow-ups` | Pagination; scoped original order. | Page of `FollowUpView` headers. |
| `GET /v1/staff/orders/{id}/cancellation-requests` | Pagination; scoped order. | Page of `CancellationRequestView`. |
| `GET /v1/staff/orders/{id}/related-orders` | Pagination; actual direct order links. | Page of `OrderSummary`, excluding self. |
| `GET /v1/staff/orders/{id}/messages/{message_id}/text` | Optional text `cursor` only; actual conversation/message relation. | `{message_id: ID, text: string, next_cursor: string?}`; exact fragment continuation, not item paging. |
| `POST /v1/staff/orders/{id}/actions` | Tagged action variants below, order version. | `OrderDetail`; status/payment updates do not imply message delivery. |
| `POST /v1/staff/orders/{id}/revisions` | `{expected_version: Version, changes: Change[]}`; unpaid eligible order. | `{order: OrderDetail, revision: RevisionView}`. Existing confirmed snapshot remains active pending customer response. |
| `POST /v1/staff/orders/{id}/follow-ups/{request_id}/decision` | Tagged decision variants below; follow-up and original-order versions. | `{order: OrderDetail, follow_up: FollowUpView}`. Never creates an accepted kitchen order merely by approval. |
| `GET /v1/staff/orders/{id}/conversation` | Pagination only; no body. | `{items: Message[], next_cursor: string?, control: ConversationControl}`. |
| `POST /v1/staff/orders/{id}/messages` | `{expected_version: Version, text: string}`; **conversation** version. | `{intent_id: ID, state: "pending", control: ConversationControl}` at 202. No provider-delivery promise. |
| `POST /v1/staff/orders/{id}/control` | `{expected_version: Version, mode: "staff" | "ai"}`; **conversation** version. | `ConversationControl`. No fulfillment/payment/revision side effects. |
| `GET /v1/staff/catalog` | Pagination only; no body. | `Catalog` page plus current restaurant settings. |
| `PATCH /v1/staff/catalog/items/{id}/availability` | `{expected_version: Version, available: boolean}`; **item** version. | `MenuItem`. Existing orders unchanged. |
| `PATCH /v1/staff/restaurant/availability` | `{expected_version: Version, open: boolean}`; **settings** version. | `RestaurantAvailability`. |

### Actions and revision changes

- Pickup acceptance: `{action: "accept", expected_version: Version}`. Delivery acceptance additionally **requires** `coverage_reviewed: true`; false/missing is invalid. Go checks current fulfillment; pickup does not accept that field.
- Other actions: `{action: Action, expected_version: Version}` where `Action` is exactly `reject | start_preparing | mark_ready | mark_picked_up | send_for_delivery | mark_delivered | cancel | mark_paid`. No extra destination status, payment amount, refund or actor fields. Staff cancellation involving paid/shared orders remains gated on settlement procedure; this body does not falsely assert settlement happened.
- `Change` is `{kind: "remove", unit_id: ID}` or `{kind: "replace", unit_id: ID, menu_item_id: ID}`. Only scoped units and available fixed-menu replacements; preserve the targeted unit's unpriced note in the proposed snapshot. Duplicate/missing units fail the whole proposal, never partially apply. These are replacement/removal operations, not customer add-on approval.

### Follow-up decisions

- Approve: `{decision: "approve", expected_version: Version, expected_order_version: Version, fulfillment_plan: "independent" | "same_pickup" | "same_delivery"}`.
- Reject: `{decision: "reject", expected_version: Version, expected_order_version: Version}`. Reject does not cancel or alter the original.
- Both versions are required and checked atomically. Plan must match actual fulfillment; `same_delivery` requires matching address/recipient and current feasible trip, derives shared-fee basis server-side, and is not an arbitrary discount input. Paid original contents/payment remain immutable. If original becomes ready/later or trip changes before customer confirmation, re-evaluate under FR-24 and refresh the summary/fee instead of reusing stale approval.

Staff operations never supply the customer's confirmation. An agent binds the verified message/current candidate through the internal tools in the API contracts; there is no staff `confirmed: true` shortcut or public submit-order endpoint.

## 4. Errors and literal examples

Error envelope: `{error: {code: ErrorCode, message: string}, request_id: string}`. Message is safe user-readable text, not a stable assertion target for domain clients; branch on code. Do not include SQL, stack traces, raw messages, tokens or provider payloads. `request_id` correlates safe logs, not permission.

| HTTP | `ErrorCode` | Meaning |
|---|---|---|
| 400 | `invalid_input` | Wrong shape/type/variant/ID/limit/cursor or duplicate JSON key. No domain write/outbox effect. |
| 401 / 403 | `unauthenticated` / `forbidden` | Missing/invalid authority or action not allowed for current staff. |
| 404 | `not_found` | Missing/not-accessible scoped resource; no cross-customer existence disclosure. |
| 409 | `version_conflict`, `invalid_transition`, `summary_required`, `idempotency_conflict` | Stale aggregate/state/candidate or same key with changed request. Never silently overwrite/reprice or create another order. |
| 413 / 415 | `payload_too_large` / `unsupported_media_type` | Bounded-body/encoding boundary rejection. |
| 422 | `policy_unresolved`, `messaging_policy_unresolved` | Deliberately unimplemented business/provider policy; no guessed refund, cancellation or messaging-window action. |
| 503 | `temporarily_unavailable` | Dependency temporarily unavailable; retry with the **same** key/body when safe. Unknown send outcomes use reconciliation, not automatic resend. |

Framework/server faults may produce safe 500 `internal_error`; no automatic side-effect retry recommendation. BFF authorization failures use the same safe envelope. Provider webhook acknowledgements remain provider-specific and do not use this staff envelope.

Synthetic examples below are request/error shapes, not implemented tests, selected menu data or prescribed customer wording.

Pickup acceptance:

```json
{"action":"accept","expected_version":"3"}
```

Delivery acceptance after manual coverage review:

```json
{"action":"accept","expected_version":"3","coverage_reviewed":true}
```

Staff message (conversation version, not order version):

```json
{"expected_version":"8","text":"The kitchen is checking your extra item."}
```

Conflict envelope:

```json
{"error":{"code":"version_conflict","message":"Refresh this order before trying again."},"request_id":"example-request"}
```

No example authorizes actual API calls or provisioned accounts. Validate these contracts later through the public BFF/Go surfaces using isolated data, including 400/409/413/415 rejection without domain/outbound effects, independent versions, exact staff text, concurrency and replay behavior.
