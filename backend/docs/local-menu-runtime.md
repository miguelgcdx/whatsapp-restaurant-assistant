# Reserved LOCAL menu-message route

Migration11 and native admission are **prerequisites**, not a runnable menu pipeline.
`cart.Store.AcceptMenu` admits pinned LOCAL events; `ReadAdmission` resolves their
historical receipts. Menu claims, epoch-aware gate integration and the runtime host
remain pending. The existing synthetic API/worker still produces legacy ACKs.
No new command is available; the native legacy menu-gate callers remain supported.

## Persisted format

| Route | Required shape |
|---|---|
| `legacy_ack` (default) | Restaurant, conversation, admission mode/version and no-reply reason are all NULL. Every pre-11 job retains its previous fields and history. |
| `menu_message` (reserved) | Restaurant/conversation match the event's composite execution-source binding; admission mode is `ai` or `staff`, admission version is nonnegative. |
| Staff admission | Completed, zero attempts/generation, no lease, reason `staff_at_admission`; no outbox producer exists in this unit. |
| AI admission | Queued/leased without a no-reply reason; leased authority requires positive attempts/generation. Completed work either has positive claim counters or the explicit terminal reason `control_changed`. |

Menu attempt/generation counters are either both zero or both positive. Unknown
routes, incomplete scope, illegal mode/reason/state combinations and mismatched
source bindings are rejected by the schema. These fields do not themselves grant
AI permission: admission locks real staff-initialized control authority.
LOCAL fixture binding is not verified provider/customer identity.

## Isolation and rollback

Legacy ClaimJob, CompleteJob/RetryJob/FailJob, and both queued/leased NextDue job
branches exclude menu jobs. Predecessors still span **both** routes; unknown send
history remains a delivery fence. LockExecutionJob rejects menu jobs even with an
otherwise exact full claim. Its canonical identity, full source/event, lease and
fresh database-clock checks remain intact for legacy native callers. Legacy Accept
rejects a menu identity even with an identical digest instead of reporting replay.

Migration 11 Down refuses **any** menu-job history, including staff-completed jobs
with no outbox. It never deletes or reclassifies admissions. A legacy-only Down
restores the v10 schema while preserving existing rows; route-aware current code
requires migration 11 and must not be run against the downgraded schema.

## Native admission

Require an owned migrated database, catalog/settings, current staff membership,
explicit `ProvisionLocal`/`LoadLocal` scope and actual staff-authenticated
`conversation.Store.Initialize`; startup/intake never initializes AI permission.
Pass operator-owned scope, matching sender, exact foundation.Event and service time
into `AcceptMenu(ctx, local, event, now)`. Staff admits completed/no-reply work with
zero attempts and no outbox; AI admits queued work pinned to the locked mode/version.
The returned boolean is true only after a known successful new commit, never replay
or error. Commit errors may mean an unknown outcome, not guaranteed rollback.
Exact duplicates retain original source/job/epoch/disposition across reopen, takeover,
return and same-mode changes without consuming another sequence. Route/scope/digest
conflicts fail closed. `ReadAdmission(ctx, local, eventID)` locks ownership, source,
control and job before observing durable state, waiting for earlier transactions.
Completed AI reply history resolves its original exact `OutboxID`/`Reply` under
lock; a missing matching durable effect returns `ErrStoredData`, not a completed
reply receipt. Its original epoch/effect remains historical after control changes,
not current model authority; an absent `ReadQueued` probe is never replay permission. No model/provider I/O or claim is performed here.

## Dependent work still pending

Scoped lifecycle, gate epoch checks and the fixture host follow in that order. The selected return-to-AI policy is **only new messages**: keep older inbound
history, but never make it newly eligible for delayed AI conversational replies
just because staff returns control, including replay/restart. This policy is not
fully implemented until the epoch-aware lifecycle and gate are connected. Staff messaging and order-status
notifications are not governed by that AI-reply policy. No live model/provider,
production delivery, independent verification or complete runtime proof is claimed.
