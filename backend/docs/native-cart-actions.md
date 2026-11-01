# Native cart admission and execution

`Store.AdmitNativeEdit(ctx, local, claim, slot, call)` is the admission seam.
It saves a deliberately trusted edit; it does **not** mutate the cart or send a reply.
Runtime owns the actual current `MenuClaim` and stable nonnegative int64 slot.
Only validated `EditCall` values for Add, Remove and SetNote are accepted.
Claim/source/slot/epoch/operation/reference metadata must never come from model JSON.

A successful `NativeActionRef` has private metadata and an `OperationID()` accessor.
There is no reference parser or JSON authority constructor. Same eligible event/slot
and canonical intent return the same reference. Changed intent or a legacy occupied
slot returns `ErrConflict`; legacy registrations are never promoted.

Admission serializes ownership, source, control, job and cart validation. It requires
a fresh actual menu lease and current AI mode at exactly the original claim epoch.
The canonical registration and independent native work/provenance commit together.
Cart scope and expected Version are checked without changing cart state.

Invalid arguments/unsupported calls return `ErrInput`; invalid, foreign, stale or
expired claims return `foundation.ErrStaleLease`. Lost AI/epoch permission returns
`ErrNativeAdmissionClosed`: `native cart admission is no longer permitted`.
Scope failures use `ErrNotFound`, cart CAS uses `ErrStale`, and existing domain
errors propagate. Errors return a zero reference. Cancellation/deadline follow the
existing context policy; database/uncertain commits use `ErrUnavailable`, not success.
Transactions are bounded to five seconds with one-second rollback cleanup.

Native work is distinct from reply jobs: retiring a reply cannot erase admission.
Migration 12 performs no backfill, existing-data rewrite or job uniqueness change;
its rollback refuses to discard nonempty native history.

## Business leases and execution

`RecoverNativeEdit(ctx, ref, lease)` acquires a business lease for a known admitted
reference, including completed history. `ClaimNextNativeEdit(ctx, lease)` discovers
unfinished admitted work without a remembered reference or model call. Both return
`(NativeActionClaim, bool, error)`. A claim has private fields and a `Ref()` accessor;
there is no claim parser or JSON authority constructor.

Lease input is inclusive **1 millisecond through 5 minutes**, without defaulting or
clamping. PostgreSQL microsecond precision determines the privately retained actual
deadline. This does not change menu-job lease semantics. True means a known committed
acquisition with a nonzero claim. False/nil means no claimable work now (including a
live competing lease or future due time), with zero claim; it is not model permission.
Errors also return false/zero claim. Invalid input is `ErrInput`; missing/foreign
references are uniformly `ErrNotFound`. Exhausted bigint generation/attempt counters
return `ErrLimit` before increments. Malformed persisted data is `ErrStoredData`.

`ExecuteNativeEdit(ctx, claim)` reloads source, canonical intent, native admission and
current business lease before reading history. Zero, foreign, expired, retired or
superseded claims return `foundation.ErrStaleLease` and zero Result. For unfinished
work, unchanged cart Version and current domain eligibility remain mandatory.
Admitted pre-takeover edits may execute while AI replies stay paused—even after the
original reply job is retired. Current AI mode/epoch is not execution permission.

Native completion/lease retirement are staged in the existing cart transaction;
the existing domain mutation/result writer commits all effects atomically. Failures
roll back that staging. Final freshness checks retain the originally locked deadline,
not the cleared lease field, and sample the database clock after blocking writes.
A database/uncertain commit returns `ErrUnavailable`, never claimed success.

Completed replay requires a newly recovered business claim. It returns the exact
stored Result without a fresh cart CAS, mutation, repricing or availability check;
old claims remain stale. Discovery never selects completed history. Recovery must
precede a model retry when durable work exists; a queue miss cannot authorize one.
No scheduler/dispatcher, model decoder, host integration or end-to-end runtime wiring
is included. Consumer acceptance still requires its separate independent proof.
