# Durable LOCAL menu replies

## Schema and legacy isolation

Migration 10 adds root `outbox.kind` (default `legacy_ack`) and typed nullable
restaurant/conversation/control/creation-claim metadata. Legacy rows require all
these fields NULL; AI rows require the full valid shape and the existing
(event, restaurant, conversation) source FK. The existing event UNIQUE remains.
No sidecar, new operation ledger, data rewrite, seed, or legacy reclassification
is introduced. Suppression is AI-only, including send-attempt outcomes.

Legacy claim, finish, expiry and both pending/sending due branches hard-exclude
AI. Their earlier-sender blockers remain global across kinds. The foundation
Store/Job/Delivery/SendResult, default worker, and fake sender are unchanged.
SQL source and sqlc-shaped SQL/models were maintained **by hand**, not regenerated;
no generator installation or regeneration proof is claimed.

Down refuses AI or suppressed history before DDL. It must not delete or reclassify
history to make rollback possible. Legacy-only rollback preserves rows/bytes;
it is not an operational cleanup command for a populated AI database.
