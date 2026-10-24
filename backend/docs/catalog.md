# Explicit local catalog storage

`internal/catalog` is a scoped, app-owned persistence boundary for isolated local
experiments. It is not an authenticated staff endpoint, public catalog API, cart,
public pagination contract or standalone authorization service. Its
`ApplyAvailability` operation persists atomic staff mutation/results and requires
an explicit fresh-membership callback; the [PATCH handler](staff-availability.md)
provides the verified JWT boundary. Startup opens stores lazily without setup.

## Explicit setup

Run the existing guarded `postgres.Migrate` separately. Open a lazy store with
`catalog.OpenStore(ctx, dsn, restaurantID)` and close it when finished. It reuses
`postgres.LocalPoolConfig`; opening does not connect, migrate, or seed. Restaurant
and menu identifiers must be canonical UUID strings. Identity-only restaurants
remain unconfigured until a caller explicitly invokes `Initialize`.

For **fictional isolated testing only**, pass these settings and items explicitly:

| Setting | Value |
| --- | --- |
| Currency | USD |
| Currency exponent | 2 |
| Flat independent delivery fee, minor units | 300 |
| Open | true |

| Fixed ID | Fictional name | Price, minor units | Initially available |
| --- | --- | --- | --- |
| 10000000-0000-4000-8000-000000000001 | Garden Sandwich | 650 | true |
| 10000000-0000-4000-8000-000000000002 | Tomato Soup | 450 | true |
| 10000000-0000-4000-8000-000000000003 | Sparkling Water | 200 | true |

These are test inputs, not production defaults. The package contains no fixture
seeding function. `store_test.go` supplies the fixture through the exported API.

## Operation contracts

- `Initialize(ctx, Settings, []Item)` validates every input before any write and
  creates identity/settings/menu in one transaction. It reuses the foundation's
  identity creator, without granting membership or accessing auth-private data.
  Locking the restaurant identity serializes concurrent setup for that scope.
- Settings require uppercase three-letter ASCII currency, exponent 0–6, and
  nonnegative int64 fee/version. Items require unique canonical UUIDs, matching
  currency, nonnegative int64 price/version, and valid UTF-8 nonblank names of at
  most 200 Unicode code points. PostgreSQL text cannot contain NUL, so it is
  rejected before writing. Zero prices/fees and an empty fixed menu are allowed.
- New settings/items always receive version zero. Supplied nonnegative versions
  have no authority. Retrying compares all fixed settings plus menu IDs, names,
  prices, and currency, independent of input order. It ignores mutable settings open/version and item availability/version
  during retry comparison, preserving staff closure and inactive state. Conflicts,
  including global item-ID collisions across restaurants, roll back all writes.
- `Settings(ctx)` returns `ErrNotFound` for an unconfigured identity. `Menu(ctx)`
  returns all scoped items (including inactive items) ordered by canonical ID;
  unconfigured scopes return an empty list. No arbitrary menu-size business cap
  or public cursor is introduced.
- `SetAvailability(ctx, itemID, expectedVersion, available)` atomically returns
  the complete updated item and increments its version, even if the boolean is
  unchanged. It cannot edit names, prices, currencies, or settings. Negative
  versions/invalid IDs return `ErrInput`; stale, missing, cross-scope, or exhausted
  versions return `ErrStale`, with zero mutation. MaxInt64 cannot overflow.
- `ApplyAvailability(ctx, AvailabilityIntent, authorize)` returns complete
  committed response bytes. It locks identity before current local membership
  authorization/replay and commits CAS plus scoped complete result atomically.
  See [availability controls](staff-availability.md) for ownership, replay/errors
  and the extra `ErrForbidden`, `ErrItemNotFound`, `ErrIdempotencyConflict` and
  `ErrStoredData` sentinels; it never substitutes a caller actor/scope.
- Rejections return zero result values. Legacy safe error sentinels are `ErrInput`,
  `ErrUnavailable`, `ErrNotFound`, `ErrConflict`, and `ErrStale`; connection details
  and raw driver errors never escape the catalog API. Use `errors.Is` in callers.

SQL is handwritten only inside this new package for the fixed parent-owned
schema. It uses pgx transactions and the existing generated identity operation;
there are no shared SQLC, migration, dependency, configuration, or helper changes.

## Evidence and limits

External `catalog_test` tests use `testpg.Start` and `postgres.Migrate`, never an
inherited DSN. They cover fixture readback, validation/retry rollback, preserved
inactive flags and versions, isolated scopes, concurrent initialization and
version updates, safe lazy-open failures, numeric/name boundaries, and unchanged
foundation `Inspect` state. The parent owns race/full-suite and independent seam
verification. Historical CLI differential is not applicable to this increment: the changed
catalog helpers have no other runtime CLI consumers (only tests/fixture setup);
local-staff-api is the explicitly requested command and retains its help/options
and legacy HTTP regression tests. No auth-private query or live service is used.
