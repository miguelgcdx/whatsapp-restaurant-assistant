# Claim-bound local menu proposals

`menugate` is the enforced **local** entry point for a bounded conversational menu
turn. It checks persisted ownership, control and the current execution job before
calling the model, then checks them again before returning a proposal. Direct
`menuengine` remains a lower-level library whose caller owns authorization.

This is not a WhatsApp adapter, customer login, order operation, durable reply,
worker route, job completion or provider-send capability. Local fixture ownership
is not verified provider/customer identity. A returned reply is ephemeral and is
accepted only at the postcheck's linearization point.

## Public API

```go
func menugate.Open(ctx context.Context, dsn, restaurantID string,
    client *commandcode.Client, maxOutputTokens int) (*menugate.Gate, error)
func (*menugate.Gate) Close()
func (*menugate.Gate) Run(ctx context.Context, source cart.Source,
    job foundation.Job) (menuengine.Result, error)
```

Open requires an explicit local DSN, canonical restaurant UUID, nonnil concrete
client and output-token bound 1–8192. It is lazy: no migration, seeding, control
initialization, binding, key/environment lookup, or model request. It owns its
authority pool and catalog store; its engine reader is **that store's Snapshot**
with the same DSN and restaurant. There is no injected reader/engine, alternate
permission mode, raw text, history, or caller/model-supplied restaurant selector.
Close after outstanding Run calls finish; nil and repeated Close are safe.

Every Run error returns `menuengine.Result{}`, including rejection after a model
success. A direct proposal preserves `Menu == nil` (not catalog-grounded). A
get_menu result preserves stored open status and complete item availability and
prices. Closed menus remain browsable: `Menu.Open == false` neither reopens the
restaurant nor permits an order. This wrapper does not change model quality or
the engine's existing schema, budget, history and tool rules.

## Explicit fixture setup

Before Run, trusted server-side setup must:

1. Initialize the local restaurant/catalog and provision its `cart.LocalContext`.
2. Accept an explicit synthetic event using the foundation store, then obtain a
   real current claim using ClaimJob. Use distinct sender namespaces when testing
   independent events; an earlier leased same-sender event blocks later claims.
3. Call `cart.Store.BindSource` explicitly for that context and accepted provider
   event ID. Source contains metadata only, not authorization.
4. Initialize conversation control explicitly through `conversation.Store`, using
   a concrete `staff.Verifier`, active membership and an authenticated staff token.
   The bot-job turn itself does **not** accept or verify a staff JWT.

Run never performs any of these setup steps. A missing control/source is an error,
not an invitation to initialize it. No dummy cart call, runtime slot, cart,
operation nonce or competing event ledger is needed for the menu turn.

For the Source API and shared claim closure lifecycle, see
[Local source binding and execution claim checks](claim-authority.md).

## Two short authority phases

Both precheck and postcheck use normal transactions with the same lock prefix:

1. Configured restaurant `FOR SHARE`.
2. Current conversation `FOR SHARE`, matching configured restaurant, Source
   conversation ID, `local_fixture`, exact business and exact sender.
3. Whole-event source `FOR SHARE`, matching Source event UUID and the same stored
   restaurant/conversation binding.
4. Current conversation control `FOR SHARE`, with valid mode and nonnegative
   version. Precheck requires AI; postcheck also requires the exact pre-version.
5. Shared `postgres.LockExecutionJob`: job `FOR UPDATE`, event `FOR SHARE`, full
   persisted job/event tuple and finite lease. Source provider ID must also match
   the copied job provider ID and, through that helper, the persisted event.
6. Invoke the helper's **separate fresh database-clock closure** after all blocking
   acquisitions, then commit/release the phase. No caller time, transaction-start
   now(), pre-lock clock or cached boolean can replace this check.

The copied Job text reaches the engine only after equality with persisted content
has been verified. There is no independent untrusted text parameter.

**All authority transactions and locks are released before engine/model I/O.**
The catalog reader also owns its short read independently, not a transaction held
across the next model request. Staff takeover and claim changes can commit while
HTTP is in flight. Postcheck reloads the complete prefix; it does not reuse a
prechecked Source, Mode or closure as authority.

Takeover → return to AI and same-mode assignments still increment control version
and invalidate the turn. Lease expiry/reclaim/completion, event-content drift,
conversation ownership drift and whole-event rebinding also fail closed. Invalid
stored control mode/version returns a stored-data error; the original schema also
constrains those fields. No control, source, job, cart, call, operation or outbox
row is written by the gate.

## Errors and cooperative bounds

| Error | Meaning |
| --- | --- |
| `ErrInput` — `invalid authorized menu turn input` | Nil/zero gate/context, malformed Source or constructor config |
| `ErrNotFound` — `menu turn source not found` | Missing/foreign ownership, source or control; Source belongs to another configured restaurant |
| `ErrControlChanged` — `AI conversation is paused or changed` | Staff mode or postcheck version mismatch |
| `ErrStoredData` — `invalid stored menu turn authority` | Invalid persisted control mode/version |
| `ErrUnavailable` — `menu turn authorization temporarily unavailable` | Database, scan, commit or shared-helper dependency failure |
| `foundation.ErrStaleLease` | Missing, tampered, noncanonical, non-finite, expired or no-longer-current claim |
| Existing engine/client errors | Safe concrete public identities preserved |
| `context.Canceled` / `context.DeadlineExceeded` | Observable cancellation or elapsed deadline, including publication lag |

Database/driver/SQL/DSN details are never returned. The gate does not log keys,
text or reasoning. The concrete client/engine already sanitize their own error
boundaries; arbitrary transport strings do not escape as gate errors.

The cooperative **20-second overall cap includes precheck, engine and postcheck**.
Each database phase has a five-second cap; all honor an earlier caller deadline.
Known cancellation is observed before inferring elapsed deadlines, and elapsed
actual deadlines are recognized even before Err publishes them. Rollback cleanup
has an independent one-second cap. There are no retries, timing padding, detached
watchdogs or attempts to forcibly contain a noncooperative transport. Trusted
client transports must honor context cancellation. A timeout does not prove that
remote provider work stopped.

## Native example: fabricated transport only

This example assumes the explicit owned local fixtures above already produced
`source` and `job`. It deliberately performs **no live provider call**.

```go
type fixtureTransport struct{}

func (fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    if err := req.Context().Err(); err != nil {
        return nil, err
    }
    return &http.Response{
        StatusCode: http.StatusOK,
        Header: http.Header{"Content-Type": []string{"application/json"}},
        Body: io.NopCloser(strings.NewReader(
            `{"choices":[{"index":0,"message":{"role":"assistant","content":"Fixture proposal"},"finish_reason":"stop"}]}`)),
    }, nil
}

func fixtureProposal(ctx context.Context, localDSN, restaurantID string,
    source cart.Source, job foundation.Job) (menuengine.Result, error) {
    client, err := commandcode.New(commandcode.Config{
        APIKey: "fabricated-local-key", Transport: fixtureTransport{},
    })
    if err != nil {
        return menuengine.Result{}, err
    }
    gate, err := menugate.Open(ctx, localDSN, restaurantID, client, 321)
    if err != nil {
        return menuengine.Result{}, err
    }
    defer gate.Close()
    return gate.Run(ctx, source, job)
}
```

Use the shown standard-library packages (`context`, `net/http`, `io`, `strings`)
and the named backend packages from inside this module. The reply above is a
model-shaped, ungrounded fixture proposal with Menu=nil, not a stored menu claim.

## Proof scope and remaining delivery gap

Public tests use an owned PostgreSQL cluster, actual Accept/ClaimJob and local
binding, real fabricated Ed25519/JWKS staff authentication, public control APIs,
and the concrete client with an injected in-memory transport. They check zero
HTTP before authority, full closed/unavailable menu wire data, zero results after
authority drift, staff completion across positive model barriers, fresh clocks
after observed pre/post lock waits, context/phase bounds, safe error identities
and pool reuse. Public cart Read/replay, BindSource retry and foundation Inspect
are supplemented by exact persisted table hashes to detect writes.

These checks do not replace parent-owned baseline differential, full backend
checks or the feature-end independent security/concurrency verification.

There is an unavoidable interval between precheck and model I/O. Takeover may
commit in that interval or during a request; postcheck rejects known-stale output
but cannot promise zero egress or recall. A takeover may also occur after the last
postcheck. Persisting/sending a reply requires a later durable outbox/control and
dispatch design; this gate neither creates nor fences that effect.
