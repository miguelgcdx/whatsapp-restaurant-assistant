# Native Add caller

This is an isolated local-fixture proposal-to-admission API, not a worker route,
a conversational ordering feature, a cart update, or order submission. The caller
owns and closes its `AddCaller` after all calls finish. It accepts a real menu
claim, not a customer-supplied cart selector, slot, or permission flag.

The supported request is exactly `add <count> <whole menu name>`, ignoring outer
whitespace and case. Separators are ASCII spaces. Count is a canonical positive
int64 decimal or the English word one through ten. An exact stored name or its
simple trailing-`s` alias must resolve to one distinct menu ID. Collisions and
all other language require clarification before model I/O. For example, `add two
burgers` can resolve a single stored `Burger`; questions, extra clauses, Remove,
and SetNote do not authorize an edit. A clear supported request itself is the
approval; model prose is not approval.

The backend reads the stored event and sole local cart, uses a single bounded
proposal and the existing decoder, and delegates admission to the native cart
domain with the claim and stable slot zero. An admitted opaque reference means
pending business work, not a changed cart or a delivered reply. This API does
not execute the action, queue a reply, or deliver anything. A later reply needs
its own current authorization. A takeover does not erase already admitted work.

Explicit retries with an unchanged original cart and intent can recover the same
reference under fresh authority. A cart change during proposal can be stale; a
later retry after a cart change can conflict with the occupied slot. No automatic
retry, remembered-version cache, or unknown-commit recovery is promised. An
error with uncertain commit outcome is not proof of rollback.

Local fixture ownership is not authenticated customer or provider identity.
Trusted ingress must establish genuine provenance before any production use.
