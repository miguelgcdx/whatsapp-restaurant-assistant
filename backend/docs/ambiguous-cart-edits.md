# Ambiguous cart edits: trusted backend contract

**Status: implemented; parent executable checks and independent source-and-receipt verification passed.**
The parent observed functional RED before implementation: native Go compilation
and isolated PostgreSQL fixtures succeeded; 14 new tests failed at the explicit
unimplemented sentinel, and the unmarked clear-edit/pickup regression passed
(14.141s, exit 1, empty stderr). The sentinel/stubs have now been removed.
Additive migration 00014 and schema tests accompany the implementation. All 18
new behavior/schema tests passed normally and under the race detector; `go vet`
passed. The full affected cart, PostgreSQL and concrete-decoder suites passed
146 top-level tests without skips. An independent verifier checked S1–S5 against
the pinned source, exact pre-edit baselines and raw parent receipts. That audit
was not an independent Go/PostgreSQL behavioral rerun. Existing completion and
concrete proposal decoders are unchanged.

## What the host supplies

A clear request such as “add two burgers” can authorize a concrete edit directly.
Do not require another confirmation merely because it is an edit. “Remove that
one” with no established target needs clarification, and must keep the order
unsubmitted even though the cart version has not changed.

The selected AI semantic carriers are separate from the existing decoders:

- `unresolved_target{kind}` proposes marking ambiguity for `add`, `remove`, or
  `set_note`. It supplies no target, source, cart/version, nonce or authority.
- `resolve_target{pending_selector,edit}` proposes resolving one displayed pending
  request through an existing concrete edit proposal. The selector is untrusted
  data, not a backend reference or an authorization assertion.

These are semantic contract descriptions, **not new accepted JSON variants of
`cartintent.Decode`** and not implemented AI transport code. That decoder still
accepts only concrete Add/Remove/SetNote proposals. Invalid arguments return its
own exact `cartintent.ErrInput` and a zero `cart.Call`; they never mark ambiguity.
`pickupintent` still supplies only `complete|not_complete`. `not_complete` means
“do not submit this assessment”, not persist, resolve, abandon, or expire a marker.
A later `complete` cannot clear one either. Go does not parse customer phrases.

The trusted host supplies the actual `LocalContext`, stored source's `MenuClaim`,
current cart/version, and a nonnegative host-owned runtime slot. The claim binds
source and admitted control epoch; the backend must reload actual current
claim/control, not trust mutable caller fields or model-generated IDs.

## Public APIs

All methods below belong to `*cart.Store` and take `context.Context` first.

```go
MarkUnresolvedEdit(ctx, local, claim, hostSlot, cartID, expectedVersion, kind) (PendingEditRef, error)
ReadPendingEdits(ctx, local, claim, cartID, expectedVersion) (PendingEditView, error)
AdmitResolvedNativeEdit(ctx, local, claim, hostSlot, view, ref, concreteCall) (NativeActionRef, error)
```

`UnresolvedEditCall(cartID, expectedVersion, kind) (Call, error)` is the canonical
registered descriptor constructor. It returns an ambiguity descriptor, never an
ordinary concrete `Call`. Registration alone cannot create an ACTIVE marker;
marking requires current real menu admission authority.
`UnresolvedEditKind` has exactly `UnresolvedAdd`, `UnresolvedRemove`, and
`UnresolvedSetNote`; zero/unknown values are invalid.

### Mark and inspect

`MarkUnresolvedEdit` must use the existing canonical event/slot registration,
including its immutable intent, fingerprint and server-issued operation identity.
It reads the original request from that source's stored inbound content, preserving
whitespace/Unicode exactly. No model-supplied original-request text is accepted.
It changes neither cart units nor cart version and admits no native edit.

`PendingEditRef` is opaque and comparable, has no parser, and JSON-encodes as `{}`.
It is server metadata, not a bearer credential. Multiple markers coexist; a mark
never replaces, coalesces, or implicitly resolves another one. Exact mark replay
returns the same ref even after resolution, without reopening it. Reusing the
same canonical slot with changed cart/version/kind conflicts. An occupied
registration without a marker is not implicitly promoted into business state;
the explicit mark must use a free host slot.

`ReadPendingEdits` returns the complete ACTIVE set for this restaurant,
conversation and cart, including requests from prior source events. A view is
bound to its actual reading source, cart version, current control epoch and exact
active-set digest. A successful empty view is valid; its zero value is invalid.
`view.Entries()` returns detached `PendingEdit{Ref, Kind, Request}` copies. Mutating
these copies cannot change the retained binding. No ref or view is reconstructed
from a list position, “newest”, kind, or a model-provided identifier.

The complete view is bounded by `PendingEditViewLimit` (64 records) and the existing
canonical result byte budget (512 KiB). An oversized view returns zero+`ErrLimit`,
never a partial view that could conceal a blocker. This first contract deliberately
has no pagination, expiry, abandonment, or emergency unblocking operation. More
than 64 active records therefore fail closed and cannot be resolved through this
bounded view until a separately designed recovery contract exists; callers must
not treat overflow as an empty set. This resource limitation is not resolution.

For EACH model invocation the host retains the exact returned view and a separate
selector-to-ref mapping for those displayed entries. It maps a selected value only
through that retained association; an unknown/ambiguous selector must not cause
an admission call. It must not rebuild the mapping from a later view, copy the
selector into a backend ID, or guess a record by kind. Backend admission still
checks that the ref belongs to the retained exact view and current active set.

### Explicit linked admission and application

The host builds a concrete `Call` with the existing validated constructors or
`cartintent.Decode`, using trusted cart/version. `AdmitResolvedNativeEdit` validates:

1. Current real source/claim/control, with the established lock prefix.
2. The retained view's scope, exact reading source/control/cart version/active set.
3. The selected ref's actual stored identity, membership and ACTIVE status.
4. The concrete edit's matching kind, cart/version and valid concrete targets.

Admission registers the concrete edit **and immutable one-record linkage** in the
existing canonical operation namespace, then admits existing native work. Same
slot with a changed ref/view/edit conflicts; replay with identical intent returns
the same native ref. Ordinary admission cannot strip or acquire a resolution link
at an occupied slot. Read-only canonical registration is never business authority.

An exact already-admitted replay may return its ref after the marker resolves,
with current admission authority and the identical historical view/ref/edit. This
replay check precedes mutable fresh-admission status/view checks. It cannot reopen
a marker, allocate another operation, or authorize a fresh use of a resolved ref.

`RecoverNativeEdit` and `ExecuteNativeEdit` keep their actual independent business
lease. The reply job is immutable provenance, not new native business permission.
The marker remains ACTIVE through admission, recovery, stale lease, stale target,
and domain failure. ONLY successful actual native application changes it to
RESOLVED, in the SAME SQL transaction as units, cart version and operation result.
The existing apply/commit owner must stage resolution in that transaction; failure
or an expired final lease rolls back everything. Native historical replay returns
the original result without applying/resolving again.

| Before | Event | After |
| --- | --- | --- |
| absent | authorized fresh mark | ACTIVE, cart unchanged |
| ACTIVE | identical mark replay | ACTIVE, same ref |
| ACTIVE | valid linked admission | ACTIVE, native work admitted |
| ACTIVE | admission/lease/domain/version failure | ACTIVE, no partial edit/result |
| ACTIVE | successful linked native application | RESOLVED, exactly one cart/result commit |
| RESOLVED | identical mark/admission/native replay | RESOLVED, original identity/result |
| any other ACTIVE record | ordinary or differently linked edit | ACTIVE |

One record resolves through one successful concrete edit. If two requests refer
to the same physical unit and the first removes it, the second may remain blocked:
its target is now missing. Do not silently group/coalesce the requests, infer
abandonment, apply an empty edit, or clear both. No automatic escape is provided.

### Public errors and no-effect rejection

All rejected calls return zero results. No rejection changes units/version,
active records, reply job attempts/effect counters, orders or receipt intents.
Rejected fresh slots stay reusable; no canonical/native operation is leaked.

- `ErrInput`: malformed/zero view/ref/kind/descriptor, invalid host slot, mismatched
  concrete edit kind, or a non-edit passed as resolution.
- `foundation.ErrStaleLease`: absent/retired/wrong-scope actual menu claim.
- `ErrNativeAdmissionClosed`: real claim but current AI mode/epoch no longer permits
  admission or pending-view disclosure, including cached mark/admission replay.
- `ErrNotFound`: inaccessible cart/ref or missing concrete target after valid scope.
- `ErrStale`: expected cart version mismatch, or a fresh admission using a view
  whose source/control/cart version/exact active set is obsolete.
- `ErrConflict`: occupied canonical slot with a different immutable intent/link.
  A resolved record cannot authorize a fresh admission: its historical view is
  stale, and the ref is absent from a new active view (`ErrNotFound`).
- Existing concrete eligibility/lease/resource errors retain their identities.

Check occupied immutable identity after authority, before fresh mutable eligibility
checks. This distinguishes exact historical replay from changed-link conflicts.
A zero ref at an otherwise fresh slot is input error; changing a previously linked
slot to zero is a conflicting immutable intent, not a way to strip linkage.

## Pickup guard and replay

The shared pickup `material` path must reject ACTIVE scoped markers with
`ErrUnresolvedEdit` (`"cart edit target requires clarification"`). This blocks new
`Prepare`, `CompletionContext`, and `Submit`, including evidence created before a
marker with the same cart version. It creates no summary, order or receipt. A
`not_complete` submit still returns `ErrPickupConfirmationRequired`; neither path
changes pending records. Admitting a valid resolution is not enough to unblock.

Successful resolution changes cart version, so old presented summary/evidence
remains invalid. Prepare and present a fresh summary before completion can submit.
Existing ordinary clear edits and unmarked pickup still take their normal paths.

Committed Prepare/Submit and completion-bridge replay branches must remain before
material inspection. Later markers cannot rewrite a committed historical result;
actual current scope/claim/control still gates access. Tests retain a still-live
actual claim for permitted replay, then retire it to prove replay is not authority.
Late presentation callbacks are never submission authority and cannot resolve a
marker. This phase does not change presentation callback policy.

## Implementation boundary and evidence

This implementation covers S1–S5 without changing either AI semantic decoder. The tests use existing cart/pickup fixtures and public stores,
real stored sources/claims/control, public job/admission counters, cart snapshots,
order lists and intent readback. They use no new raw SQL or production fixture
copy. API-only descriptor tests need no database. Parent-observed functional RED
was the explicit sentinel, not missing migration tables or failed fixture setup.
The 15 existing new-feature cart tests and their assertions remain unchanged.
The separate schema tests use SQL as the persisted-format boundary and reuse the
existing isolated migration runner: preservation/empty Down, descriptor/history
rollback refusal, uniqueness/kind/immutability checks, and atomic result linkage.
All three schema tests passed against isolated PostgreSQL, normally and under
the race detector. No schema RED is claimed; the pre-implementation RED exercised
the explicit API sentinel.

The tests intentionally omit a deterministic marker-vs-submit barrier test:
the existing barriers inspected in pickup/native tests require test-specific SQL
and do not expose a reusable public synchronization seam. Do not add a generic
barrier framework or pretend nondeterministic concurrent starts prove ordering.
The implementation must serialize both paths through the existing
restaurant → conversation → source/call → control/job → cart/resource order.

After parent-observed RED and bounded co-edit coordination, the implementation
uses only these shared hooks:

- `execution.go`: canonical descriptor representation/decoder dispatch.
- `fulfillment.go`: `encodeCall` dispatch only.
- `native_actions.go`: reuse canonical native admission with explicit linkage.
- `native_execution.go`: validate linkage and stage one-record resolution in the
  existing application transaction, preserving business lease/replay semantics.
- `pickup_domain.go`: material guard only, not committed replay rewrites.
- Additive migration `00014`: scoped marker state, provenance/uniqueness/FKs,
  immutable canonical/link identities, ACTIVE→RESOLVED-only transition, and a
  nullable native `unresolved_operation` link. A deferred scoped result FK prevents
  staged resolution from committing without its matching cart operation result.
  Empty Down restores v13; any marker or new descriptor history refuses rollback.
  Migration `00013` is unchanged.
- The new helper/codec/resolution files and new behavior tests in this unit.

Parent owns the exact Go/PostgreSQL runner, functional/schema verification and
tracker. Independent verification inspected source, scoped baseline preservation
and raw parent evidence without running Go/PostgreSQL itself. These results do
not retry, replace or close earlier unavailable pickup fixture/core/bridge proof.
No commit, ref movement, publishing or production activation was performed.

The affected PostgreSQL test maintenance is complete: latest-schema assertions
expect migration 14 and require the new unresolved table to be empty; the
migration-13-specific pickup fixture explicitly uses `UpTo(13)`. Existing row,
counter, history and rollback assertions remain intact. All four repaired tests
passed before the full affected suites.

| Parent check | Observed result |
| --- | --- |
| New behavior/schema tests | 15 cart + 3 schema pass; 15.429s / 3.619s |
| New behavior/schema race tests | Same 18 pass; 20.211s / 5.628s |
| `go vet` on cart/PostgreSQL | Exit 0; empty stdout/stderr |
| Four repaired migration tests | Pass; 4.083s |
| Full cart/PostgreSQL/concrete-decoder suites | 146 top-level passes, zero FAIL/SKIP; 195.968s / 44.419s / 0.029s |

All successful executable commands exited 0 with empty stderr. The four obsolete
latest-version expectations initially failed (version 14 versus 13) before their
narrow correction; that outcome is retained rather than relabeled as success.
The independent audit verified the 204-file candidate manifest, seven exact
pre-edit baselines and the 16-path feature diff. Evidence documentation was
updated afterward; no runtime source, schema or test input changed after the
verified candidate.

Coverage limitations remain: no new deterministic marker-versus-submit barrier
execution, and no direct linked Add/SetNote success test beyond the existing
Remove-success and Add-failure coverage. Shared restaurant/conversation locks
serialize the paths; a race-detector pass is not a deterministic ordering proof.
Domain/schema/tests/docs exceed 1,000 authored lines as one coherent behavior unit.
