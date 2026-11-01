# WhatsApp Restaurant Assistant — Product Requirements Document

**Status:** Agreed product definition; implementation and commercial validation are not complete.  
**Current stage:** Learning and testing prototype, not a production launch.

## Product definition

A WhatsApp ordering assistant that turns customer conversations into confirmed food orders, with a dashboard where restaurant staff manage fulfillment and handle exceptions.

The AI handles routine ordering conversations like a restaurant employee: natural, context-aware replies rather than a command interface. Customers can ask questions, change their minds and confirm in their own words; the assistant clarifies genuine ambiguity without requiring keywords or confirmation codes. Staff retain control over order acceptance, fulfillment, substitutions, and cancellation. Customers explicitly confirm orders and proposed revisions; trusted backend logic controls prices and totals.

## Problem and value hypothesis

Manual message-based ordering can leave order details scattered across conversations, require repetitive questions, and make confirmation and fulfillment difficult to track. Restaurants introducing WhatsApp ordering also need a structured process for receiving and managing those orders.

The product hypothesis is that combining conversational ordering with an order dashboard can make these workflows more organized and reduce routine staff messaging. These benefits have not yet been validated with restaurants; they are not proven commercial outcomes.

## Target customers

Small restaurants that want to introduce or improve WhatsApp ordering for pickup and restaurant-managed delivery.

| Segment | Intended value |
|---|---|
| Restaurants already taking WhatsApp orders manually | Reduce repetitive messaging and organize confirmed orders in a staff dashboard. |
| Restaurants starting WhatsApp ordering | Introduce a conversational ordering channel with a clear staff workflow. |

Both segments use the same product. Targeting both does not imply separate implementations or additional MVP onboarding features.

## Users and responsibilities

| User | Product interaction |
|---|---|
| Customer | Browse the menu or request items directly, build an order, confirm its total, and receive updates. Communicate with staff when exceptions require attention. |
| Restaurant employee | Review and accept or reject orders, update fulfillment status, take over conversations, propose order revisions, and cancel accepted orders when necessary. Coordinate delivery manually. |
| Restaurant owner | Potential future buyer interested in introducing or improving WhatsApp ordering. A separate owner interface has not been specified. |

The delivery person participates in restaurant operations but is not an application user in the MVP.

## Goals

### Current: learn and test

- Understand the complete WhatsApp → AI → backend → dashboard flow.
- Demonstrate correct pickup and delivery ordering with one test restaurant.
- Test staff control of fulfillment and direct customer communication when an order needs attention.
- Verify correctness and explicit customer confirmation rather than prioritize AI complexity.

### Future: evaluate a commercial product

- Validate usefulness with real restaurants from both target segments.
- Evaluate whether the product reduces routine messaging and improves order organization.
- Potentially offer the product for sale after validation and appropriate production preparation.

Commercialization is a future direction, not a commitment to a launch date, pricing model, billing system, or multi-restaurant MVP.

## Core product journeys

1. **Discover or order directly:** A customer asks to see the menu or immediately requests known items. Both paths use the same predefined menu and lead into the same cart flow.
2. **Confirm an order:** After required details and the current summary/total, the agent asks whether the customer wants anything else. If so, continue adding items and refresh the summary. A clear answer that they are finished confirms submission without a separate confirmation prompt; ambiguity or silence does not.
3. **Fulfill an order:** Staff receive the order in the dashboard, accept or reject it, and update its progress. The customer receives WhatsApp updates.
4. **Resolve an exception:** Staff take over the conversation and message the customer directly. AI conversational replies pause until staff explicitly return control.
5. **Revise or cancel:** For an unavailable item, staff propose a replacement or removal; the customer confirms the revised order and recalculated total before changes apply. If the customer does not want a replacement, staff can cancel the accepted order and notify them.

6. **Add something later:** Before staff acceptance of an unpaid order, confirm an amendment to the same order. Never amend a paid order's confirmed contents/total: extras use a linked unpaid order. Once accepted/preparing, ask staff about an add-on; never silently change kitchen work. Once ready or later, start a new order without reopening the original. Staff-approved extras travelling together share the delivery fee; otherwise disclose the separate fee before confirmation. Staff resolve paid/shared-delivery cancellation amounts with the customer; the agent cannot automatically add charges, transfer payment or promise refunds.

These journeys describe product intent. [MVP.md](MVP.md) summarizes agreed first-release scope and boundaries; [REQUIREMENTS.md](REQUIREMENTS.md) defines precise planned behavior and acceptance examples.

## Success for the prototype

Success means demonstrating the agreed journeys with test data, not proving market demand or production readiness.

| Outcome | Evidence to obtain during implementation |
|---|---|
| End-to-end ordering works | A test customer completes pickup and delivery orders through WhatsApp and staff receive them in the dashboard. |
| Orders are accurate and confirmed | Items, notes, fulfillment details, and server-calculated totals match the confirmed summary. |
| Orders are not duplicated | Repeated events or submission retries do not create duplicate orders. |
| Staff can handle exceptions | Staff chat and takeover work; confirmed revisions update the existing order; staff cancellation notifies the customer. |
| Customers remain informed | Order receipts and staff-driven status updates reach the test customer's WhatsApp. |
| The system can be understood | The message, AI, business-operation, and dashboard flow can be explained from the implementation. |

These are future verification outcomes, not completed test results. Detailed acceptance examples linked to numbered rules live in [REQUIREMENTS.md](REQUIREMENTS.md#observable-acceptance-examples). No commercial time-saving, conversion, revenue, or adoption targets have been established.

## Product boundaries

- The MVP is a single-restaurant testing experience; [MVP.md](MVP.md) is the release-scope reference.
- Restaurant staff, not the AI, control acceptance and fulfillment decisions.
- The product is not a delivery-driver platform. The restaurant coordinates its own delivery person.
- Online payment processing is not part of the MVP.
- The original brief's broader roadmap and technical proposals do not automatically become approved product scope.

## Document ownership and open decisions

| Document | Responsibility |
|---|---|
| [PROJECT_BRIEF.md](PROJECT_BRIEF.md) | Preserved original product and technical blueprint. |
| This PRD | Product purpose, target customers, users, goals, journeys, and success outcomes. |
| [MVP.md](MVP.md) | Agreed first-release scope and workflow summary. |
| [REQUIREMENTS.md](REQUIREMENTS.md) | Precise numbered rules, observable acceptance examples, and unresolved behavioral details. |

Requirements now record agreed planned behavior, not implemented or verified functionality. [ARCHITECTURE.md](ARCHITECTURE.md) records the partial agreed architecture baseline and open implementation decisions; it is not an implementation-ready design. This PRD does not choose frameworks, service boundaries, deployment infrastructure, or final data models.

Real restaurant validation, commercial pricing, production launch criteria, and detailed operational policies remain future discussions. The unresolved MVP details are listed in [REQUIREMENTS.md](REQUIREMENTS.md#open-decisions-and-boundaries).
