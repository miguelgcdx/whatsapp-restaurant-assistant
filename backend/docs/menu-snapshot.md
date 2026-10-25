# Read a complete, scoped menu snapshot

`catalog.Store.Snapshot(ctx)` returns one consistent settings-and-items view for
its server-configured restaurant. Closed restaurants remain browsable: `Open`
stays `false`, all stored items remain present, and browsing neither reopens the
restaurant nor authorizes an order.

## Public contract

```go
type MenuSnapshot struct {
    Settings Settings
    Items    []Item
}

func (*Store) Snapshot(context.Context) (MenuSnapshot, error)
func (MenuSnapshot) Validate() error
```

A successful snapshot has a nonnil `Items` slice (including an empty menu), in
ascending canonical UUID order. It includes unavailable items without modifying
names, prices, currencies, availability, settings, or versions. There is no price
calculation or persistence side effect.

`Validate` checks the existing catalog domain rules, plus the complete projection's
nonnil slice, strict UUID ordering, and local item budget. It does not sort,
normalize, or mutate caller-owned input. Invalid projected/stored values return
`ErrStoredData`; an oversized projection returns `ErrSnapshotLimit`. Database
constraints need not be weakened to test invalid projected money, currency,
precision, identity, name, or version fields.

## Consistency and execution budgets

| Concern | Behavior |
| --- | --- |
| Scope | Only `Store.restaurant`, configured by the server through `OpenStore`; no per-read restaurant/model selector. |
| Atomic view | One explicit PostgreSQL `READ ONLY REPEATABLE READ` transaction for settings and items. |
| Completeness | Up to **1000** items; the query fetches at most **1001** rows to detect overflow. |
| Overflow | `ErrSnapshotLimit` and a zero `MenuSnapshot`, never a truncated or partially successful menu. |
| Time | Snapshot work has a **5-second** context cap, honoring an earlier caller deadline. |
| Cleanup | Rows close on every path; rollback uses an independent context capped at **1 second**, including after cancellation. |
| Errors | Every error returns a zero snapshot. Nil context/invalid store returns `ErrInput`; missing settings returns `ErrNotFound`; other database failures return safe `ErrUnavailable`. Context cancellation/deadline identities remain observable through `errors.Is`. No raw driver, SQL, or connection details are returned. |

The item and time budgets are local engineering safeguards, **not restaurant
business limits or provider guarantees**. There is no schema quantity cap. A
caller must surface a failed read, not treat it as an empty menu or partial
success.

The existing `Page` API is unchanged. Its at-most-100-item pages, keyset cursor,
and independently current settings are still useful for staff browsing, but
looping through those pages cannot establish a complete atomic snapshot.

## Trust boundary and next consumer

The reader is a reusable local component, **not worker control authorization** or
a trusted inbound-message processor. A future caller must establish current
conversation/control/read authority before invoking it or a model. Restaurant
scope is server-owned; neither model parameters nor a caller-supplied permission
flag can grant that authority. No persistent staff takeover state is implemented
by this reader.

The dependent menu-turn component can consume the public `MenuSnapshot` and
`Validate` API. It is not implemented or wired here. This unit adds no HTTP/CLI
entry point, model calls, order/cart actions, outbox processing, or worker changes.

## Focused proof

Run from `backend` with the project's isolated PostgreSQL prerequisites and
approved offline Go environment:

```sh
go test -count=1 -timeout=20s -run '^TestMenuSnapshot' ./internal/catalog
go test -race -count=1 -timeout=20s -run '^TestMenuSnapshot' ./internal/catalog
go vet ./internal/catalog
```

The new-prefix tests exercise empty/open/closed menus, unavailable items, complete
105-item reads, exact 1000/1001 boundaries, two-restaurant isolation, validation,
zero-result failures, unchanged records, and the prior `Page` limit/lookahead
behavior. PostgreSQL prerequisites fail rather than silently skipping proof.

For coherence, a fixture holds an `ACCESS EXCLUSIVE` menu-table lock, starts the
reader, and positively observes its menu-query lock wait via `pg_stat_activity`
and `pg_locks` after the settings read. The gate updates settings and item values
and commits. The blocked read must return the old coherent pair; a subsequent
snapshot must return the new pair. This detects a mixed read-committed view;
fixed sleeps are not used as correctness barriers. Separate blocked reads prove
cancellation, earlier caller deadline, the local timeout, and successful reuse
after cleanup. Observation and goroutine joins are bounded.
