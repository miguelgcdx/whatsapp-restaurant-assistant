# Local staff workspace

The staff UI uses the existing Next.js routes and server-owned access/catalog
contracts. Its quiet sidebar follows `design/staff-workspace/01-sidebar.html`,
with generic **Service desk** branding rather than the artboard's sample identity.
No external fonts, images, icon libraries, or new dependencies are needed.

## Routes and supported work

- `/sign-in`: the **The host** entrance from `design/restaurant-sign-in/02-the-host.html`.
  The Service wordmark, centered form and open-paper header/footer use the existing
  system tokens and OS light/dark preference. **Work email** and **Password** retain
  their existing field names, limits, required validation and autocomplete. A failed
  sign-in remains a generic form-level message, associated with the form alongside
  its manager-access help; it does not identify a particular incorrect field.
  Password visibility is an explicit reversible control; only its boolean state
  is held in React. The real form action drives **Signing in…**, read-only fields
  and a disabled submit/reveal button while pending. The existing server action
  and route-local Suspense remain responsible for authentication and URL-derived
  feedback. No prototype controls, simulated authentication or account changes
  are included.
- `/dashboard`: confirms the signed-in staff identity and access, links to the
  catalog, explains the availability workflow, and provides sign out. This page
  does not read or prefetch catalog data or display invented activity metrics.
- `/dashboard/catalog`: reads the current server-confirmed restaurant state and
  menu page, displays exact configured-currency prices, and provides restaurant
  and item availability controls. Page item counts describe only the current
  page; they are not total menu counts. Next page preserves the requested limit;
  Start again returns to the initial catalog page. Back to dashboard uses client
  navigation without prefetching.
- `/access-denied`: explains missing current staff access, with return-to-sign-in
  and sign-out actions.
- `/unavailable`: explains that local services are unavailable and offers the
  existing retry link. Catalog invalid-input and outage states retain their
  existing messages and share the catalog frame.

Authentication remains in the existing route/action owners, not the visual
frame or root layout. The sidebar's Overview, Catalog and Orders links use client
navigation with prefetching disabled. Catalog has no route-level `loading.tsx`
replacement: the transition can retain the current page while the authorized
server request resolves instead of switching to the sparse loading screen.
This adds no authentication or data cache. The error boundary's same-URL
**Start again** remains a native link so it can reload the document.
`StaffFrame` is a pure presentational component usable by both server-rendered
pages and the catalog client error boundary.

## Availability and feedback

Availability remains server-authoritative, not optimistic. The existing control
owns its request version, idempotency key, body, retry, and refresh behavior.
Saving disables the control and displays feedback. An uncertain result offers
**Retry same request**; do not start a different change. Conflicts require
reviewing refreshed availability before another deliberate change. Feedback
stays beside its named item/restaurant control, with the accepted heading,
output, and described-by associations preserved.

## Navigation and responsive presentation

The named **Staff navigation** marks the active page. A keyboard-visible
**Skip to content** link targets the single focusable main region. The desktop
sidebar becomes a horizontal navigation band at 800px and below. Cards stack
on narrow screens, and long authored names, prices, and feedback wrap rather
than truncate. Controls have at least 44px height and visible focus outlines;
disabled controls also use a distinct border and cursor. Semantic stone/forest
CSS tokens adapt to the system light/dark preference without a theme toggle.

## Verification and limits

The focused `tests/auth/staff-catalog.test.ts` renderer checks cover both active
navigation states, the skip target/single main, existing money/auth/error
behavior, and error fallback structure. The navigation contract also checks
that Catalog has no automatic route loading module. These are wiring checks,
not transition-paint or no-flash proof. They use the existing real
authentication and upstream fixtures.

The parent-owned `tests/integration/dashboard.mts` journey adds authenticated
navigation, keyboard skip focus, 320px overflow, and navigation target-size
checks before returning to the existing 1280×900 availability journey. The
anonymous `tests/e2e/auth.spec.ts` checks access panels at 320px and 1440px,
the selected sign-in copy and unchanged access-denied/unavailable messages/actions,
form-level failure association, password visibility and actual pending feedback,
and no horizontal document overflow. The pending test holds and aborts the action
request locally using public synthetic input, without contacting authentication. Browser assertions require the owned real
Next/Go/PostgreSQL harness; writing them is not evidence of a passing browser
run. Renderer fallback checks do not claim runtime loading capture or manual
assistive-technology testing.

The existing access/catalog delivery does not establish order, conversation,
takeover, payment, analytics or menu-editing runtime support. The additional
pickup source below is a separate offline implementation, not an activated
or connected-product delivery.

## Pickup orders — offline source only

The new source targets backend contract **pickup-staff-v1-r3**. Backend r3/domain,
SQL persistence and staff transactions are not operational; their independent
proof remains held. Do not treat a frontend test as backend or runtime acceptance.

| UI or BFF source route | Purpose |
| --- | --- |
| `/dashboard/orders` | Paginated staff order headers; counts describe this page only |
| `/dashboard/orders/{id}` | Exact confirmed summary, independent fulfillment/payment, preparation-only estimate and server-offered actions |
| `/dashboard/orders/{id}/summaries/{summary_id}/units` | Immutable captured units and cursor pinned to that summary |
| `GET /api/staff/orders` | Proxy `GET /v1/staff/orders` |
| `GET /api/staff/orders/{id}` | Proxy the corresponding Go detail route |
| `GET /api/staff/orders/{id}/summaries/{summary_id}/units` | Proxy the corresponding Go immutable-units route |
| `POST /api/staff/orders/{id}/actions` | Proxy the corresponding Go action route |

Prices, full-order totals, snapshot IDs, preparation estimates and permitted
actions come from the server. List headers stay minimal: capabilities are returned
only on detail/action results, and controls require a detail read. The immutable
unit anchor is `confirmed.id`; there is no `confirmed_summary_id` field. A missing estimate is not zero or a readiness
promise. The frontend does not sum a page into an order total or decide paid/late
policy. It offers only `permitted_actions`; this array is not authorization.
Go must recheck bearer, current membership, order scope and transition policy.

An action submits only `{action, expected_version}` with an `Idempotency-Key`.
Before sending, the browser saves the exact immutable order ID, key and body in
its own sessionStorage entry. No staff/restaurant/role/price authority fields or
authentication credentials are stored in that entry. Storage failures block
sending. A timeout, disconnect, malformed acknowledgement or server failure keeps
the original intent; **Reconcile same request** is manual and never generates a
new key/body. A later controlled rejection does not clear earlier uncertainty.
Do not collect physical payment again during reconciliation.

A committed acknowledgement may be an older replay. It does not replace the
current view or capabilities. **Reload order before another action** performs a
fresh authenticated document read. Reading an order alone cannot prove which
uncertain intent committed. Session storage is recovery metadata, not a durable
ledger; closing/clearing the browser session can lose it.

The BFF validates exact JSON, IDs, query keys, framing and media type; uses
server-owned authentication and a Go JWT rather than a browser bearer; bounds
action bodies to 64 KiB and Go responses to 512 KiB; and sanitizes errors and
response headers. Reads default to 50/max 100 and preserve opaque cursors up to
2048 ASCII characters without clipping. Body/Go dependency deadlines are two
seconds; a browser action acknowledgement has a five-second deadline and remains
uncertain on expiration. No automatic retries, payment collection or fulfillment
coupling are introduced.

Ordinary offline verification uses the installed Vitest CLI with
`--config vitest.pickup.config.mts`. This configuration disables local env-file
loading and selects only pickup contracts/auth-boundary tests plus workspace
navigation regressions. Fixtures are synthetic and HTTP dependencies bind only
owned ephemeral loopback ports. The existing auth suite that provisions PostgreSQL
is not part of this command. Source/type/structural checks do not establish
browser accessibility, no-flash behavior, actual auth/Go/SQL integration,
transaction/replay integrity, operational availability or connected-flow acceptance.
No build, type generation, browser/login, database operation or preview activation
is authorized by these checks.
