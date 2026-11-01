# WhatsApp Restaurant Assistant — MVP

**Status:** Agreed scope; not implemented or verified.  
**Purpose:** Build a learning and testing prototype, with possible commercialization later. This is not a production launch or a live restaurant pilot.

## First milestone

Using one test restaurant and one test WhatsApp number, a customer places an order through an AI text conversation, confirms a server-calculated total, and sees exactly one order arrive in the restaurant dashboard. Staff process it manually, and the customer receives WhatsApp status updates.

## Document ownership

| Document | Responsibility |
|---|---|
| [PROJECT_BRIEF.md](PROJECT_BRIEF.md) | Immutable original product and technical blueprint; broader proposals are not automatically MVP commitments. |
| [PRD.md](PRD.md) | Product purpose, target users, and success outcomes. |
| This MVP | Agreed release scope and workflow summary. |
| [REQUIREMENTS.md](REQUIREMENTS.md) | Precise numbered rules, observable acceptance examples, and unresolved behavioral details. |

Requirements below are planned, not implementation evidence. [ARCHITECTURE.md](ARCHITECTURE.md) records the partial agreed architecture baseline and open implementation decisions; this MVP does not choose frameworks, providers, deployment topology, or a database design.

## Included scope

| Area | Scope summary | Rules in REQUIREMENTS.md |
|---|---|---|
| Menu and cart | Fixed menu text lookup or direct ordering, quantity changes/removal, and unpriced per-item preparation notes. No menu/price editor or stock tracking. | FR-01–FR-04 |
| Item availability | Staff toggle availability per fixed item; unavailable items cannot enter new orders and existing orders are not silently changed. | FR-05 |
| Fulfillment | Pickup or delivery with address/recipient phone visible to staff, a separately shown flat delivery fee, and manual restaurant/driver coordination. | FR-06 |
| Coverage | Staff review delivery coverage before acceptance; contact the customer to offer pickup or reject unsupported addresses. No maps, distance, or automated zones. | FR-07 |
| Open/closed and busy | Staff set open/closed; closed blocks submissions. Busy does not block: show a provisional preparation-only wait before confirmation. | FR-08–FR-09 |
| Capacity | Test model: three parallel orders, about ten minutes each, accounting for accepted waiting/preparing workload and elapsed preparation, not per-menu durations. | FR-10–FR-12 |
| Confirmation and dashboard | Explicit confirmation of complete summary and server total, one submitted order/receipt, staff acceptance/rejection and manual fulfillment statuses. | FR-13–FR-15 |
| Staff chat | Order-linked panel, exact staff-authored messages and customer replies; takeover pauses AI conversational replies only, not status notifications. | FR-16–FR-17 |
| Accepted-order revisions | Explicit customer confirmation before replacing/removing items; waiting pauses/releases capacity; confirmed revision updates the same order at the back of the queue. | FR-18–FR-21 |
| Cancellation | WhatsApp cancellation before acceptance; after acceptance flag a request for staff decision/manual cancellation. Completed/already-cancelled orders cannot be cancelled again. | FR-22 |
| Payment | Cash on delivery or pay at pickup; initially unpaid, with manual Mark as paid after cash collection. No automatic payment on acceptance/completion. | FR-23 |
| Later additions | Same-order confirmed amendment before acceptance only if unpaid; paid originals use linked unpaid orders. Otherwise, staff-approved add-ons after acceptance/preparation; new order once ready or later. Combined delivery shares the original fee only with staff approval. | FR-24 |
| Safeguards | Trusted validation/pricing, explicit confirmation, duplicate prevention, protected credentials/inbound events, authorized staff access, and WhatsApp messaging constraints. | NFR-01–NFR-04 |

## Customer ordering flow

1. The customer browses the predefined menu or directly requests available items through WhatsApp text.
2. The assistant builds the cart, accepts quantity changes/removal and unpriced preparation notes, and collects pickup/delivery selection.
3. For delivery, collect the address and recipient phone and apply the flat fee once; pickup has no delivery fee.
4. Before explicit confirmation, show the complete order, fulfillment details, offline payment method, server-calculated final total, and provisional preparation-only wait. Closed prevents submission; busy does not.
5. Ask whether the customer wants anything else after the current complete summary. Clear completion submits exactly one order and sends a receipt without another confirmation prompt; more items continue the cart and refresh the summary. Retries must not duplicate it.
6. Staff review delivery coverage before acceptance, accept or reject the order, and manually update fulfillment status. Customers receive WhatsApp updates.

## Staff workflow and preparation estimates

Staff see items, quantities, notes, fulfillment details, payment method/state, total, current status, and delivery address/phone/fee when applicable. Staff coordinate directly with the delivery person, who waits for ready food and takes it to the address; there is no driver app or automatic assignment.

```text
Submitted → Rejected
         or
Submitted → Accepted → Preparing → Ready
                                     ├─ Pickup: Picked up
                                     └─ Delivery: Out for delivery → Delivered
```

This is the normal fulfillment path, not a complete transition diagram; revisions and cancellation follow the requirements. Food is never automatically marked ready by a timer or estimate. Payment is separate: orders start unpaid, and staff record cash collection with Mark as paid. Acceptance and completion do not auto-pay.

The wait model counts accepted waiting and preparing workload, excludes ready/rejected/cancelled/completed orders, and accounts for elapsed preparation. With three synchronized slots taking ten minutes each, positions 1–3 are ready in 10 minutes, 4–6 in 20, 7–9 in 30, and 10–12 in 40. The baseline formula `ceil((orders_ahead + 1) / 3) * 10` is **not** a universal runtime formula. Three orders already preparing for six minutes plus three waiting orders give a new order a 24-minute estimate. No delivery travel time is estimated; overdue preparation behavior remains open. See FR-09–FR-12.

## Staff chat and order exceptions

Staff send their exact text through the restaurant's WhatsApp connection and receive replies in the dashboard. Takeover pauses AI conversational replies until explicit return of control; automated order-status notifications remain active, subject to WhatsApp messaging-window/template constraints. Ordinary chat does not itself edit an order.

For an unavailable item on an accepted order, staff propose a fixed-menu replacement or removal. The customer sees the revised summary and backend-calculated total. Existing items and total remain unchanged until explicit confirmation; “maybe,” decline, or silence does not authorize the change.

While awaiting revision, the accepted order shows Awaiting customer response and pauses/releases capacity. Unanswered proposals stay there indefinitely without automatic timeout/cancellation; staff follow up or cancel. On confirmation, the same order is updated, joins the **back** of the queue, and gets a recalculated estimate. Revision confirmation does not return AI control during takeover. Handling partly prepared food/restart effort is unresolved.

If the customer does not want a replacement, staff can cancel in the dashboard and notify them. Declining alone does not cancel. Customers may cancel through WhatsApp before acceptance unless payment/shared delivery requires staff settlement; afterward their message flags a staff decision rather than automatically cancelling. Completed or already-cancelled orders cannot be cancelled again. Late-stage/refund policies remain open. See FR-16–FR-23.

Later additions follow the current order state, not a five-minute window. Before acceptance of an unpaid order, confirm an updated summary on the same order and flag the update. Paid contents/totals stay unchanged: extras use a linked unpaid order even before acceptance. After acceptance/preparation, staff must approve the addition without silently changing or pausing existing work; the customer confirms the additional total. Ready/out-for-delivery/completed orders are not reopened: create a new order. Staff-approved add-ons travelling with the original share its delivery fee; independent delivery fees are disclosed before confirmation. Clarify ambiguous targets and do not inherit the original payment/readiness promise. Cancellation involving payment/shared delivery requires staff to resolve amounts with the customer; the agent cannot automatically add fees, transfer payment or promise refunds. See FR-23–FR-24.

## Outside this version

- Production launch, real restaurant onboarding, multi-restaurant administration, and subscription billing.
- Menu/price editing, paid extras, size options, and stock tracking.
- Online payments, payment links, and payment-provider integrations.
- Voice messages and advanced interactive messaging.
- Driver applications, automatic assignment, GPS tracking, delivery integrations, maps, automated coverage zones, and distance-based pricing.
- POS integrations, promotions, loyalty programs, advanced analytics/reporting, and marketing campaigns.

Additional features in the original brief remain uncommitted, not implicitly included or permanently rejected.

## Acceptance and remaining discussion

[REQUIREMENTS.md](REQUIREMENTS.md#observable-acceptance-examples) owns the acceptance examples linked to numbered FR/NFR rules. These are future checks, not completed test results.

The [open decisions](REQUIREMENTS.md#open-decisions-and-boundaries) cover sample menu/prices/currency/fee, confirmed delivery-to-pickup conversion, closed-state menu browsing, overdue preparation, partly prepared food/restart effort, late-stage cancellation/refunds, automatic handoff triggers, and detailed security/validation/failure/notification policies.

Next: refine authentication implementation details, backend mechanics, and testing using the partial baseline in [ARCHITECTURE.md](ARCHITECTURE.md), without treating the original brief's technology suggestions as finalized decisions.
