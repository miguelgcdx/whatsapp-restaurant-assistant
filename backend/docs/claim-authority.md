# Local source binding and execution claim checks

This is a **local fixture seam**, not customer authentication or a model permit.
It separates explicit event ownership setup from immutable cart-call registration,
without adding a table or a second event ledger. Existing direct cart APIs retain
their behavior. The dependent menu gate is not implemented by this unit.

## API and trust boundary

```go
func (*cart.Store) BindSource(ctx context.Context, local cart.LocalContext,
    eventProviderID string) (cart.Source, error)

func (cart.Source) RestaurantID() string
func (cart.Source) ConversationID() cart.ConversationID
func (cart.Source) EventUUID() string
func (cart.Source) EventProviderID() string
func (cart.Source) Business() string
func (cart.Source) Sender() string

func postgres.LockExecutionJob(ctx context.Context, tx pgx.Tx,
    eventUUID, sender string, job foundation.Job) (func() error, error)

var postgres.ErrClaimUnavailable = errors.New("claim verification temporarily unavailable")
```

`Source` has private fields, no setters, parser, JSON deserializer, or public
constructor. Its zero value is invalid. Copying a populated Source copies only
immutable metadata. Possession is **not authority**: it does not establish verified
WhatsApp identity, possession of a provider credential, a current job claim, or
current conversation control. No generic permit abstraction is introduced.

The setup caller must supply an explicitly provisioned current `LocalContext` and
an already accepted event's provider ID. It must not derive ownership selectors
from model output. `BindSource` reuses the cart store's current local ownership
validation, including restaurant, conversation, `local_fixture`, business and
sender. It neither creates a conversation nor accepts an inbound event.

## Binding and registration lifecycle

1. Explicit fixture setup initializes the restaurant and local conversation and
   accepts the synthetic event through the existing foundation store.
2. `BindSource` validates the current context, locks the persisted event and its
   whole-event binding, then creates that binding only if absent and correctly
   scoped. Retry and reopen return the same metadata.
3. Binding does not allocate a cart, runtime slot, call, operation ID, job, or
   outbox entry, and does not mutate control or claim state.
4. Legacy `RegisterExecution` uses the **same in-transaction binder**. Source and
   call commit together. A post-binding registration failure rolls back the
   source too; there is no preliminary committed BindSource call.

The existing `app.cart_execution_sources` namespace remains whole-event: all
slots share one restaurant/conversation binding. Attempts to rebind an existing
source fail with `cart.ErrConflict` before checking a mismatched sender. An
unbound event with the wrong sender fails with `cart.ErrNotFound` before any
INSERT. Concurrent binding and registration converge on this namespace; retries
of a registered slot retain its operation ID and exact original intent bytes.
Operation UUIDs are still allocated only for genuinely new slots. Registration's
original top-level validation, codec/budget rules, and error precedence remain
unchanged.

### Binding errors and bounds

The new binding API rejects nil context, nil/zero store, zero/foreign local
context and invalid provider keys with `cart.ErrInput`. Provider keys use the
existing `text(..., 128, true)` validation. Missing current ownership or event
returns `cart.ErrNotFound`; existing foreign ownership returns `cart.ErrConflict`.
Dependency failures return the static `cart.ErrUnavailable`, without driver data.
All failures return `cart.Source{}`.

Only this new API has a cooperative five-second database-operation cap, honoring
an earlier caller deadline. Observable cancellation retains `context.Canceled`;
an elapsed deadline returns `context.DeadlineExceeded` even before `Err()` has
published it. Rollback cleanup has its own one-second bound. There is no retry,
watchdog, detached operation, or timeout change to legacy registration. As with
any transaction, a commit error is not proof that the server did not commit;
retrying explicit binding is idempotent.

## Shared claim helper: transaction ownership is essential

`LockExecutionJob` moves the original cart execution guard into PostgreSQL
infrastructure. It is not a standalone boolean, pool precheck, or job completion
operation. Call it on the transaction that will retain the protected rows.
Acquire ownership/source/control locks first when a consumer needs those scopes.
The existing cart order remains restaurant → conversation → source/call → job →
cart/resources. The helper itself takes job `FOR UPDATE` and event `FOR SHARE`.

The helper checks the full persisted tuple: canonical job UUID, positive generation
and attempt, exact event UUID, leased state, generation, attempt, provider event
ID, expected sender, copied job sender, exact content, and a finite lease deadline.
UUID validation is the original cart rule: scan succeeds and canonical UUID value
is exactly the original string. Uppercase and unhyphenated inputs are rejected;
a canonical all-zero UUID is syntactically valid but still must match a real row.
The unrelated PostgreSQL lease-ID policy is not reused.

The returned closure is a **transaction-scoped clock recheck**, not a transferable
capability. Invoke it after the helper returns, after later potentially blocking
statements, and immediately before releasing the outcome or committing. It issues
a separate `SELECT clock_timestamp()` against the held transaction and compares
with the finite locked deadline. Transaction-start `now()`, caller time, clocks
in expressions evaluated before LockRows, or a clock sampled before later waits
cannot replace it. The closure retains only the deadline protected by those locks;
it does not reacquire authority after transaction release.

The closure is valid only while the same transaction and its row locks remain
owned. After commit/rollback it fails safely; do not reuse it for another
transaction or execute it concurrently with transaction release. No job state is
written, and foundation Accept/ClaimJob/CompleteJob behavior is unchanged.

### Claim error compatibility

| Condition | Public helper | Existing cart execution adapter |
| --- | --- | --- |
| Missing, mismatched, noncanonical, expired or non-finite claim | `foundation.ErrStaleLease` | same identity |
| Query, scan, clock, closed transaction or dependency failure | `postgres.ErrClaimUnavailable` | `cart.ErrUnavailable` |

The adapter translates **both** the initial helper error and the returned closure
error. It does not expose SQL, DSN, driver error strings, or change existing cart
context-error mapping. Existing Execute, ExecuteDraft and ExecuteSummaryPreview
still check authority before cache/replay and after later blocking reads/writes.
Result types, fingerprints, saved bytes, replay identities and guard call sites
are not changed.

## What remains necessary before a model turn

A future dependent menu gate must independently revalidate Source metadata,
current control/version and the persisted claim in **both** short database phases:
before model I/O and before accepting its result. All database locks must be
released across model/network I/O. This guide does not claim Source already
permits that call, and this unit adds no gate, worker, HTTP route, model request,
job completion, durable history, outbox or provider dispatch.

A later accepted proposal can only be authorized at its postcheck linearization
point, not as a durable send capability. Takeover may commit after a precheck or
during I/O; a later postcheck can reject stale output but cannot prevent every
in-flight request or recall an already-sent provider message. Durable delivery
requires a separate outbox/control/dispatch design.

## Focused proof and remaining verification

`TestSourceBinding*` exercises the public binding API against owned PostgreSQL:
getters/reopen/retry, allocation counts, current ownership, context bounds,
conflict priority, cross-API races, legacy replay, and atomic registration failure.
`TestClaimAuthority*` calls the public helper on real accepted/claimed events:
tuple tampering, canonical/all-zero UUIDs, finite/stale/reclaimed/completed leases,
safe dependency errors, closure lifecycle, and positively observed lock waits
before fresh clock checks. SQL provides fixture setup, lock observations and
supplemental counters; public outcomes are the primary assertions.

These tests do not replace the eight separately selected legacy preservation
tests, their selected late-blocking/cache race cases, the parent's later public
baseline differential, or the feature-end independent security/concurrency check.
