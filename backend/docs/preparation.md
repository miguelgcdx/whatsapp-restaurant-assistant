# Local provisional preparation estimates

`internal/preparation.Calculate` models preparation-only readiness for a prospective
new order from an explicit local snapshot. It is a pure native Go API, **not an
integration with real orders**, a queue store, a submission gate or a status clock.
No database, provider, customer identity, HTTP endpoint or CLI is involved.

## Native example

Use a server-supplied snapshot time, not customer-provided elapsed time. This fixed
fixture has three orders six minutes into preparation and three accepted orders
waiting. The new order needs **24 minutes**: four remaining, ten waiting, ten for
its own preparation.

```go
import (
    "fmt"
    "time"

    "restaurant-assistant/backend/internal/preparation"
)

asOf := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
workload := []preparation.Entry{
    {State: preparation.StatePreparing, Sequence: 1, StartedAt: asOf.Add(-6 * time.Minute)},
    {State: preparation.StatePreparing, Sequence: 2, StartedAt: asOf.Add(-6 * time.Minute)},
    {State: preparation.StatePreparing, Sequence: 3, StartedAt: asOf.Add(-6 * time.Minute)},
    {State: preparation.StateAccepted, Sequence: 4},
    {State: preparation.StateAccepted, Sequence: 5},
    {State: preparation.StateAccepted, Sequence: 6},
}
estimate, err := preparation.Calculate(asOf, workload)
switch {
case err != nil:
    // Reject the invalid/unrepresentable snapshot; do not invent an estimate.
    fmt.Println(err)
case estimate == nil:
    fmt.Println("Preparation estimate unavailable")
default:
    fmt.Printf("%d minutes; provisional: %t\n", estimate.Minutes, estimate.Provisional)
    // This fixture prints: 24 minutes; provisional: true
}
```

The import is internal to this backend module. Place native callers within that
module; the snippet is not a new executable or public transport contract.

## Model and boundary validation

| Rule | Behavior |
|---|---|
| Capacity and effort | Three parallel slots; ten minutes per order, not per menu item. |
| Active workload | Non-held `accepted` entries wait; non-held `preparing` entries occupy slots with remaining modeled effort. |
| Exclusions | `submitted`, `ready`, `out_for_delivery`, `rejected`, `cancelled`, `picked_up`, `delivered`, and held entries add no work. There is no generic `completed` state. |
| Active sequences | Positive `int64`, unique across accepted and preparing entries. No sequence-based quantity cap. |
| Active start times | Accepted entries must have zero `StartedAt`. Preparing entries require nonzero `StartedAt` no later than `asOf`; at most three may be active. |
| Snapshot boundary | Reject zero `asOf` and unknown states, including unknown held states. Ignored entries' sequence/time fields are not validated as order policy. |
| Simulation | Seed idle slots at zero and occupied slots at remaining effort. Assign each waiting order, then the prospective new order, to the earliest available slot. Uniform waiting effort makes input ordering irrelevant. |
| Output | Ceiling to positive whole minutes; UTC `AsOf`; `Provisional: true`. No delivery travel time or readiness promise. |

For a synchronized baseline only, positions 1–3 take ten minutes, 4–6 twenty,
7–9 thirty, and 10–12 forty. `ceil(position / 3) * 10` is **not** the general
runtime formula: unequal preparation starts use individual remaining durations.

## Unavailable and error results

- A valid snapshot with **any** active preparing entry at or beyond ten minutes
  returns `(nil, nil)`. No numeric estimate is known. The order and occupied slot
  remain unchanged until staff update workload; elapsed time never marks food
  ready, completes an order or releases capacity.
- Any future wire mapping must represent unavailable/undefined estimates as
  **null**, never zero or a readiness promise. This package adds no wire adapter.
- The **whole** snapshot is validated before returning unavailable. Malformed
  entries still return an error even when another entry is overdue.
- `errors.Is(err, preparation.ErrInput)` identifies an invalid snapshot;
  `errors.Is(err, preparation.ErrOverflow)` identifies unrepresentable checked
  duration arithmetic. Errors return no estimate; do not substitute zero, retry
  automatically or treat invalid data as an unavailable-but-valid snapshot.
- Success, unavailable results and errors all leave the input untouched.

The calculator has no acceptance, opening-hours or submission control. Workload
cannot block a submission through this API. Provisional presentation before
confirmation and real staff status updates remain future integration work.

## Focused verification

From `backend` using the prepared offline Go environment:

```sh
go test -count=1 -timeout=20s ./internal/preparation
go test -race -count=1 -timeout=20s ./internal/preparation
go vet ./internal/preparation
```

These deterministic tests use fixed times and public `Calculate` calls, without
sleeps, external prerequisites or short-mode skips. They cover the twelve baseline
positions, the 24-minute example, unequal starts, rounding, exclusions, malformed
snapshots, overdue boundaries, UTC output, repeated calls, reordered snapshots
and unchanged input. Commands here are instructions, not full-suite or live-order
verification claims. The overflow guard is structural; tests do not allocate a
multi-million-entry snapshot just to exhaust duration capacity.

## Observed local evidence

The public behavior tests were authored before implementation. Initial RED was a
compile failure (`no non-test Go files`), not observed runtime failures for every
case. Focused normal/race tests and vet passed; a parent focused spotcheck also
passed (exit 0, 0.002s).

On `go1.27.3-X:nodwarf5 linux/amd64`, the parent then ran the complete backend:

```sh
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=120s ./...
```

All three exited **0** in one 88-second run. Preparation normal/race timings were
0.005s/1.054s. No independent verifier was required for this additive pure package;
these results do not imply independent or native review approval, live workload
integration, customer presentation, order submission or automatic status updates.
Exact commands, attribution and commits are in `odd/tasks/local-preparation-estimates.md`.
