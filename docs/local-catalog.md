# Local staff catalog

## Open the catalog

Complete the explicit [local staff setup](local-auth.md), configure and start the
numeric-loopback Next and Go listeners, then sign in at `/sign-in`. Choose
**View catalog** on `/dashboard`. Access confirmation and sign-out remain on the
dashboard. This is local fictional-data work only, not orders or deployment.

Go also requires `STAFF_CATALOG_CURSOR_KEY`: exactly 64 lowercase hexadecimal
characters encoding 32 separately generated random bytes. Keep it private and
independent of authentication secrets. Missing key/settings/dependencies returns
a safe unavailable state; no default menu is invented. See the
[Go catalog guide](../backend/docs/staff-catalog.md) for stored-data setup and
cursor semantics. Runtime, build and migrations never seed a catalog. There is
no new production setup command or menu/price editor.

## Read and navigate

`/dashboard/catalog` shows the **current page**, item names, configured-currency
prices, availability and current restaurant open/closed state. Money is rendered
as the exact currency code and fixed decimal amount at the stored exponent,
including int64-sized values; it does not assume USD or two decimal places.
An empty array explicitly says the menu is empty. A partial page is not presented
as the entire menu. **Next page** follows the opaque cursor; **Start again** drops
it. Default page size is 50; `?limit=1` through `?limit=100` can select a smaller
page. Only `limit` and `cursor` are accepted. Availability/settings can change
between pages; no snapshot across requests is promised.

Plain links deliberately avoid prefetching catalog pages. Each navigation checks
the authoritative uncached session and Go's current membership again; a cursor
is never authority. Missing sessions go to sign-in, denied membership to access
denied, and outages/malformed data show safe errors without old menu data.
Loading, empty, invalid-page, end-of-menu and start-again states are explicit.
The [staff availability controls](local-availability.md) now allow only item availability
and restaurant open/closed changes. Read names, exact prices, current-page navigation and
read errors are unchanged. There are still no cart, order, name or price-edit buttons.

## BFF boundary

`GET /api/staff/catalog` is the read catalog BFF route. The two separate protected
PATCH BFF routes are described in [local availability](local-availability.md).
A supplied Origin must
match the configured Next origin; GET may omit Origin. Caller identity/role,
restaurant and Authorization headers grant nothing. Body streams, framing other
than an optional exact `Content-Length: 0`, Trailer headers, unknown/duplicate
query fields and malformed encoding are rejected. Unsupported methods return
405 with `Allow: GET`. Every success/error is `no-store` with a safe envelope and
request ID; internal errors and upstream messages are never echoed.

The server obtains its JWT through public Better Auth APIs after an authoritative
session lookup and sends only that token plus Accept to fixed
`/v1/staff/catalog` at the validated Go loopback origin. Transport uses direct
Node HTTP, no agent/proxy/redirect/retry, a two-second absolute upstream deadline
and a 512 KiB raw-response bound. Success must be strict UTF-8 JSON matching the
strict Zod catalog contract. Invalid data cannot become a successful empty menu.
Go input/cursor rejection stays 400 and membership denial stays 403; other Go
errors/protocol failures become safe 503. Tokens, cookies, SQL, DSNs and provider
payloads are not returned. No shared response cache or browser JWT storage exists.

## Verification ownership

Focused component checks:

```sh
pnpm exec vitest run tests/contracts/staff-catalog*.test.ts tests/auth/staff-catalog.test.ts
```

These tests exercise exported DTO/query/formatting, real generated Better Auth
sessions in owned temporary PostgreSQL, loopback HTTP, route exports and server
rendering. The loopback catalog provider here is a **component seam**, not proof
of Go authorization. They cover exact prices, invalid scalars/input, no authority
forwarding, safe upstream failure/timeout, logout continuation and preserved
session/dashboard behavior.

Parent-owned real journey:

From the repository root, select the already-prepared local browser cache before
Node starts (the `env` passed to `chromium.launch` does not select its registry):

```sh
PLAYWRIGHT_BROWSERS_PATH="$PWD/backend/.cache/playwright" pnpm test:dashboard
```

The existing `tests/integration/dashboard.mts` provisioner creates its own
PostgreSQL/private generated auth state, explicitly builds/runs the test-only
`backend/internal/catalog/testdata/browser-fixture` using public
`catalog.OpenStore`/`Initialize`, generates a private cursor key, then launches
real Go and Next. Its explicit fictional setup is three items priced 650/450/200
USD minor units, exponent 2, open, flat fee 300; this is not a runtime default.
Browser navigation requests `limit=1` for real continuation. It retains the
original anonymous/sign-in/sign-out/session-isolation/revocation/JWKS/database
journeys, adds catalog prices/current-page/next/start-again, revoked continuation,
Go outage and unchanged catalog data after rejected reads. It cleans up its own
processes/state/database, without inherited DSNs, traces, HAR, screenshots,
storageState or emitted credentials.

Observed evidence is recorded in [the feature log](../odd/tasks/catalog-dashboard.md):
parent full suite (12 files / 31 tests), nonincremental typecheck, production build
and actual owned browser journey passed. The original browser launch failure and
the subsequent ambiguous-alert locator RED are retained; the corrected journey
passed in 46 seconds, including dependency outages. The independently supplied
boundary subset was promoted to `tests/contracts/staff-catalog-boundaries.test.ts`;
its four tests passed separately after packaging (not a claim of a repeated full
suite).

Independent security verification passed one focused invocation (2 files / 9
tests, 5.08 seconds) and 56 additional public DTO/query/price assertions in a
separate probe process. It used fresh private source/dependency copies, owned
PostgreSQL and real Better Auth sessions; it did **not** independently repeat the
Go/browser journey or production build. Neither native RDD nor publication ran.
The module-type warning remains. Historical auth/Go proof waivers and unresolved
elapsed-expiry, immediate Go-token revocation, asset and unchanged development
Suspense diagnostics are not resolved or broadened by this catalog evidence.
