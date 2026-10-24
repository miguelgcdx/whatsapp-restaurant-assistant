# Pure draft pricing

`internal/pricing` is a standard-library-only calculation boundary. It has no
storage, HTTP, authentication, customer-text parsing, or submission behavior.

## Trusted inputs and lifecycle

Construct an immutable `Engine` with `pricing.New(Settings, []Item)` using trusted
application settings and the complete fixed catalog. Construction validates all
entries, including unavailable and unused entries, and copies the catalog. There
are no currency, exponent, fee, or menu defaults. Identity-only restaurants must
remain unconfigured until an explicit application setup occurs.

For isolated fictional testing only, use USD, exponent 2, delivery fee 300 minor
units, Garden Sandwich 650, Tomato Soup 450, and Sparkling Water 200. These are
explicit fixture choices, not production recommendations or package defaults.

A draft is `[]pricing.Unit`. Each unit has a unique canonical lowercase UUID ID,
a fixed catalog menu ID, and an exact UTF-8 note of at most 500 code points. Units
have no price field: only the trusted engine determines prices. The caller
allocates stable unit UUIDs; no randomness, implicit quantity, or quantity cap is
introduced here. Repeated menu IDs represent separate units with separate notes.
An unselected fulfillment may accompany an application draft but cannot be quoted.
`Engine.Subtotal(draft)` uses the same existing checked validation without a
fulfillment choice, fee, final total, payment method or input mutation. It returns
zero for an empty valid draft, retains unavailable existing units, and rejects
malformed/duplicate/unknown units and arithmetic overflow just as Quote does.
`Quote` still rejects unselected fulfillment; no pickup default is introduced.

- `Add(draft, unit)` appends exactly one known, available unit.
- `Remove(draft, unitID)` removes exactly that unit, preserving other units' order.
- `SetNote(draft, unitID, note)` preserves IDs and all other unit fields. Notes
  remain exact, including whitespace; notes never affect prices.
- `Quote(draft, fulfillment)` derives subtotal, fee, total, currency, exponent,
  payment method, and a detached unit list.

Successful operations and quotes own their returned slices; changing them does
not mutate the input. Failures return nil units or a zero Quote and leave all
inputs untouched. Inputs must not be concurrently mutated by callers. The engine
itself is immutable and safe for concurrent reads. Its zero value is invalid.

## Fulfillment and totals

`Fulfillment{Kind: pricing.Pickup}` requires empty address and phone, charges zero
fee, and derives `pay_at_pickup`. Delivery requires a nonblank valid UTF-8 address
of at most 1000 code points and phone of at most 128, charges the configured fee
exactly once, and derives `cash_on_delivery`. Delivery strings are preserved, not
normalized or interpreted as geographical coverage or phone verification.

Two sandwiches (one with `no onions`) and water total 1500 minor units for pickup
and 1800 for delivery (1500 subtotal plus 300 fee). Removing the exact unnoted
sandwich preserves the noted sandwich and water, with subtotal 850.

All arithmetic is checked nonnegative int64 minor-unit addition, with no floats.
An empty pickup draft totals zero; an empty delivery draft still incurs its fee.
Neither result grants permission to submit an empty order. Addition checks the
subtotal; final Quote separately checks the fulfillment-dependent total.

## Availability and validation errors

Create a new engine from a new trusted catalog snapshot to observe availability
changes. Unavailable items cannot be added but existing draft units remain
quoteable and editable: availability never filters units out. Catalog prices are
fixed by the owning application; availability updates must not change them.
This package always derives prices from the supplied trusted snapshot and stores
no caller-provided unit prices.

`New` returns `invalid settings` for invalid currency (three uppercase ASCII
letters), exponent (0–6), or negative fee. It returns `invalid catalog` for
malformed/duplicate menu UUIDs, currency mismatch, negative prices, or invalid
names (nonblank valid UTF-8, at most 200 code points).

Operations reject malformed units/notes and validate the entire existing draft,
including duplicate IDs and unknown menu references, before returning a change.
Error messages are `invalid engine`, `invalid unit`, `duplicate unit ID`,
`unknown menu item`, `menu item unavailable`, `unit not found`, `invalid note`,
`invalid fulfillment`, and `amount overflow`. Errors do not partially return or
silently shorten draft contents. UUID validation checks canonical lowercase
hexadecimal spelling and hyphen placement; it does not impose a UUID version.

## Verification boundary

Package tests use exported APIs and no services, DSNs, database fixtures, or
network access. They cover the canonical totals, exact unit removal, notes,
caller-owned slices, invalid inputs, catalog snapshot ownership, unavailable
items, explicit fulfillment, Unicode boundaries, and int64 overflow. There is no
existing CLI or shared helper change, so historical CLI differential checks do
not apply. The additive subtotal tests cover its shared-validation behavior and preserve
Quote's rejection of unselected fulfillment. The [local fixture cart](cart.md)
uses stored per-unit price snapshots rather than caller amounts or fresh repricing;
its PostgreSQL text boundary also rejects NUL notes without changing this pure
package's historical validation. Parent full normal/vet/race passed; the cart guide
records [evidence ownership and accepted LOCAL-only limitations](cart.md#observed-results-and-local-dispositions),
including unavailable independent runtime. Those limits are not independent runtime approval.
