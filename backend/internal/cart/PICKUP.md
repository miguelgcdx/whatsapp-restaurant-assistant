# LOCAL pickup implementation

Canonical scope and ports: [connected pickup contract](../../docs/local-pickup-orders.md).
This implementation is synthetic LOCAL plumbing, not natural-language recognition,
live delivery, staff HTTP activation, or independent/connected acceptance.

`NewPickupStore` borrows the existing cart pool and has no `Close`. Configure the
same restaurant and `PickupCompletionAssessmentVersion` server-side. Register real
prepare/submit descriptors with the actual event and an explicit host-owned slot.
The pickup transaction pairs the shared canonical loader with the existing menu
claim validator, current control, material quote checks and fresh database clock.

A fake receiver reads exact durable text through `ReadLocalIntents`, then calls
`RecordLocalPresentation` after receiving the summary. For intervening existing
conversational outbox messages, `RecordLocalReply` records a scoped, digest-bound
fake receiver callback. These are trusted in-process LOCAL transport operations,
not model tools or HTTP endpoints. Enqueue/provider acceptance is not presentation.
History is all scoped turns after that presentation and before current inbound;
missing bindings and oversized complete windows fail rather than truncate.

Candidates/units/presentations/evidence/intents are immutable. Submission commits
one unpaid order, evidence, original result and receipt together. A submitted
draft cannot acquire another initial order; amendments/extras remain deferred.
Staff reads/actions check current membership inside the transaction, including
replay. Cursors are MAC-authenticated and subject/restaurant/resource-bound;
reopening the facade invalidates cursors, not durable operations. Detail actions
are hints, not authorization. Collection never changes fulfillment and vice versa.

Checks are standard `TestPickup*` tests in cart/postgres plus the ordinary package
suite, focused race and vet commands. Synthetic decisions establish backend
correlation only. The parent owns staff wire/main/AI mapping and missing external
acceptance. No old proof or unavailable verifier is retried by these tests.
