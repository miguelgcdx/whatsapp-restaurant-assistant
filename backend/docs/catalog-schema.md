# Optional local catalog schema

Status: additive catalog/pricing foundation, not an order or staff HTTP implementation.

The explicit app migrator now includes `00003_local_catalog.sql`. Apply it only to an owned local test database using the existing `local-migrate` command documented in [the backend guide](../README.md). API/worker constructors do not migrate or seed a catalog. Better Auth continues to own `auth` and its migrations.

## Identity is not operational configuration

`app.restaurants` remains identity-only. The optional one-to-one `app.restaurant_settings` row holds explicit currency, exponent, flat fee, open state and version. An existing staff identity may remain unconfigured. There are no migration defaults for USD, two decimal places, open state, fee or menu data.

`app.menu_items` holds fixed names/prices/currency, availability, version and creation time. A composite `(restaurant_id, currency)` foreign key prevents a menu currency inconsistent with its configured restaurant; scoped `(restaurant_id, id)` uniqueness supports later ownership references. Settings and menu references have restrictive deletion, not cascades. Prices and versions are nonnegative PostgreSQL `bigint`; settings exponent is explicitly 0–6. Menu names are nonblank and at most 200 characters; application setup additionally validates UTF-8 and Unicode whitespace.

The catalog tables themselves contain no order or financial-settlement records.
Additive `00005_local_cart.sql` now supplies the separate [local fixture cart
component](cart.md): `app.cart_conversations`, `app.carts`, `app.cart_units` and
`app.cart_operations`. It installs no fixture data, defaults, TTL or cascading
cleanup. Composite ownership references and stored currency/exponent/price
snapshots keep local drafts scoped without changing the original catalog or
staff `app.operations` contract. Local fixture contexts are not verified customer
identity. Stored catalog availability alone grants no caller authority; staff
HTTP operations retain their existing session/BFF/JWT/current-membership boundary.

## Test-only values

The authorized fixture uses USD/exponent 2, Garden Sandwich 650, Tomato Soup 450, Sparkling Water 200, and a flat independent delivery fee of 300 minor units. Initial open state is explicit. These are fictional setup inputs, not real menu data or production defaults. Pickup pricing uses no delivery fee.

## Verification and rollback boundary

The exported migrator regression proves schema presence, repeat migration without modifying an existing durable inbound event, preservation of an identity-only restaurant, and zero implicitly created operational records. The catalog store tests separately cover validation and concurrent initialization/update behavior; pure pricing tests cover unit arithmetic. This document does not claim those tests have run merely because the migration test passed.

The cart upgrade regression additionally installs migrations 1–4 through the
public Goose provider/fixture FS, populates foundation/catalog/membership/staff
results through their exported APIs, then invokes current public `Migrate`.
Exported snapshots, menu/settings, membership access, staff replay and subsequent
key use are preserved; supplemental SQL hashes check records/counters, and all
four new cart tables start empty. It neither reads Git history nor installs a
production test hook. See [cart verification limits](cart.md#evidence-boundary).

The original catalog schema work unit consists of its guide, migration and public migrator regression. No down migration is supplied: do not delete populated tables to roll back code. Any future data removal requires a separate explicit retention/rollback decision. Existing historical migrations are unchanged.
