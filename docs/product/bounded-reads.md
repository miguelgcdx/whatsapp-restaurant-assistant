# Bounded staff reads

**Status:** Selected design, not implemented or load-tested. This resolves the detail-response bounds left open in [staff wire schemas](http-schemas.md). [API contracts](api-contracts.md) own authority and mutations; [data contracts](data-contracts.md) own confirmed content/state. Pagination is a transport concern, not permission to discard content or add a maximum order-size business rule.

## 1. Small headers, explicit collections

`OrderDetail` contains scalar status/payment/totals, the current confirmed snapshot header, the current revision header if present, and links to collections. It does not embed every unit, follow-up, cancellation request or related order. Mutation responses use that same bounded detail shape.

A snapshot header includes `id`, `unit_count`, `units_href`, fulfillment, subtotal/fee/total and fee basis/source. It does not embed units. Candidate headers use the same snapshot header and estimate; they do not repeat the potentially long stored summary text. Staff render structured items/totals and can read authored conversation text through the message routes. The exact summary remains stored; this changes retrieval, not confirmation evidence or historical content.

New collection links always target explicit allowlisted same-origin BFF routes `/api/staff/...`; the BFF maps them to Go `/v1/staff/...`. Never accept arbitrary upstream URLs. IDs, links, cursors and a shared conversation do not confer access: resolve resource relationships and current staff authority on every request.

| Go read route | Result / relationship |
|---|---|
| `GET /v1/staff/orders/{id}/summaries/{summary_id}/units` | Page of immutable `Unit`; include `summary_id` and `unit_count`. Summary must be the order's confirmed/history snapshot or its actual amendment/revision/follow-up candidate, not merely another summary in the restaurant. |
| `GET /v1/staff/orders/{id}/follow-ups` | Page of `FollowUpView` headers belonging to this original order, including their states/versions and candidate headers. No nested unit arrays. |
| `GET /v1/staff/orders/{id}/cancellation-requests` | Page of `CancellationRequestView` for this order. A request is not evidence of financial settlement. |
| `GET /v1/staff/orders/{id}/related-orders` | Page of `OrderSummary` for direct parent/children links, excluding self. No recursive family graph or inference from customer phone alone. |
| `GET /v1/staff/orders/{id}/messages/{message_id}/text` | Exact message text fragment plus continuation cursor. Resolve the order's actual conversation/message relationship; no raw provider body or arbitrary message lookup. |

Existing order/conversation listings keep pagination. `GET /v1/staff/catalog` now pages fixed menu items and returns restaurant availability alongside the page; paging adds no menu editor. All collection routes accept the existing `limit`/`cursor` parameters. Message text accepts only its text cursor, not an item-count limit.

## 2. Count and byte budgets

- Every successful staff response is at most **512 KiB of serialized, uncompressed UTF-8 JSON**, including envelope/metadata. Check the serialized size, not a Go struct estimate or compressed network size. BFF must preserve the bound when adding its own envelope; do not double-wrap responses.
- Collection pages default to 50 and permit 1–100 records, but can return fewer when the byte budget would be exceeded. `next_cursor` identifies the last **emitted** record, never a prefetched/unreturned row. Never skip a record merely because it would not fit this page.
- A next cursor explicitly means more content remains; null means no further matching rows at that read. Do not return a successful empty page with a nonadvancing cursor. If even one valid-shaped record cannot fit, return safe 500 `internal_error` and investigate the stored-data/serialization violation, rather than silently truncating it or looping forever.
- Bound normal record scalars at ingestion/setup/output construction: menu and confirmed item names ≤200 Unicode code points; notes/address/phone follow the existing wire limits. Long message text and summary text are not scalar fields in collection headers. Never shorten an existing confirmed name/note to fit: reject invalid setup/input before persistence, or surface a safe stored-data error.
- Do not solve response pressure by imposing a maximum number of confirmed items. A large legitimate order is retrieved through pages; its stored unit count and final total remain complete.

Before committing a mutation, construct and validate its bounded response/idempotency result in the same short transaction. A response-size failure rolls back domain/result/outbox changes; do not commit a success that cannot be replayed. No provider call or unbounded collection rendering belongs in that transaction.

## 3. Ordering and snapshot identity

Use ascending stable `(created_at, id)` ordering for live orders/messages/follow-ups/cancellation requests/related orders/menu items. Immutable summary units use `(unit_id)` ordering within a named summary; the cursor binds that summary ID. Database unit ID is the stable per-unit ID, not the displayed grouping/quantity. Name, price and note come from `summary_units`, not current catalog state.

Cursors are opaque, versioned, server-issued and authenticated with an application-purpose MAC; never reuse an auth bearer token as a cursor. Bind collection kind, canonical parent/resource IDs, authenticated restaurant scope, sort key and last emitted position. Text cursors also bind message ID and character offset. Cursor signing configuration is supplied only during authorized implementation; no secret is specified here. Enforce the existing 2,048-character bound before decode, validate the token/shape and then authorize the actual resource. Wrong scope/resource, tampering or malformed cursor → 400 `invalid_input`, without returning another resource's data. Cursor possession alone never authorizes reading.

An unchanged immutable summary can be paged without mixing versions. If the order is amended while its old summary is being read, old pages remain tied to that old summary; do not substitute units from the new current pointer. The dashboard compares the latest order version/header before presenting an actionable current view; stale mutation versions remain rejected. It must not label a partial item list as the complete order.

Live collection paging is not a transaction spanning HTTP requests. State updates and new rows can occur between pages; refresh the collection/header to see changes. Null continuation is not a guarantee that no later follow-up will arrive. Do not keep database transactions/connections open between requests just to freeze a dashboard listing.

## 4. Exact message text without hidden truncation

`Message` list records contain metadata, `text` (the first fragment), `text_next_cursor: string?` and `text_href`. First and continuation fragments contain at most 4,000 Unicode code points and must also fit the response byte budget after JSON escaping. This fragment limit does not retroactively reject longer valid inbound messages.

Continuation result `data` is `{message_id: ID, text: string, next_cursor: string?}`. Omit the cursor to read the first fragment. Split only at Unicode code-point boundaries; concatenating decoded fragments in order must reproduce the exact stored text, including whitespace/newlines, with no ellipsis inserted into the content. The dashboard clearly indicates remaining text and can fetch it fully; it must not treat the first fragment as the whole message when a cursor remains.

Message text is immutable once persisted. Edit/correction is a new message, not a change under an old text cursor. Provider-specific message/chunk limits and outbound delivery mapping remain separate integration gates; these staff-read fragments are not WhatsApp sends or automatic rewriting of staff text.

## 5. Planned public checks

These are future tests, not completed execution:

| Scenario | Required observation |
|---|---|
| More units than a page, with distinct per-unit notes | Follow cursors to retrieve each unit exactly once; count equals immutable `unit_count`, all notes/prices match the snapshot and total never becomes a page subtotal. |
| Byte budget reached before item count | Response stays ≤512 KiB; next page starts with the first unreturned record. No dropped unit, hidden clipping or stuck empty page. |
| Amend between unit-page reads | Old summary pages remain internally consistent; refreshing header reveals new version/summary. A stale staff mutation rejects instead of overwriting. |
| Multiple follow-ups/related orders or menu pages | Retrieve every matching record through explicit continuations; no cross-order/scope leakage or unbounded nested arrays. |
| Long message with Unicode/newlines | Concatenated fragments equal exact stored text; continuation is visible; no split code point, inserted ellipsis or lost whitespace. |
| Missing authority, wrong parent or tampered cursor | Safe 401/403/404 or invalid cursor 400 as applicable; no unauthorized content, domain mutation or outbound effect. |
| Oversized invalid record or mutation result | Safe failure instead of partial success; rejected mutation leaves domain/result/outbox unchanged. |

Read-only Markdown/JSON checks do not prove these properties. Execute them later through isolated BFF/Go public interfaces and verify actual encoded response sizes; no live provider, Neon mutation or generated application code is authorized by this document.
