# Local staff orders host wiring

`cmd/local-staff-api` now mounts the existing staff orders handler alongside
access, catalog, and availability. This is source wiring, not service activation
or acceptance of the complete `pickup-staff-v1-r3` contract. The selected wire
contract and its outstanding projection/domain proof restrictions remain in
[local-pickup-orders.md](local-pickup-orders.md#staff-wire-subset). This change
does not resolve those restrictions or establish connected acceptance.

## Mounted routes

| Method | Path |
| --- | --- |
| GET | `/v1/staff/orders` |
| GET | `/v1/staff/orders/{id}` |
| GET | `/v1/staff/orders/{id}/summaries/{summary_id}/units` |
| POST | `/v1/staff/orders/{id}/actions` |

The dispatcher uses `stafforders.Matches` on the original URL path, without
path cleaning or redirects. Existing access, catalog, availability, and
unknown-path fallback behavior remains owned by the existing handlers.

Orders use the existing bearer verifier and current configured-restaurant
membership checks. Actions use JSON `{action,expected_version}` and require an
`Idempotency-Key`. Preserve the exact key/body after an uncertain outcome; do
not automatically create a fresh intent. See the selected contract for action,
paging, envelope, validation, and replay requirements.

## Existing configuration and lifetime

The host retains its numeric-loopback `-listen` boundary and existing required
configuration: `LOCAL_DATABASE_URL`, `LOCAL_RESTAURANT_ID`, `STAFF_JWT_ISSUER`,
and `STAFF_JWKS_URL` (fixed `/api/auth/jwks` path). No new environment variable
or authentication policy is introduced. The optional
`STAFF_CATALOG_CURSOR_KEY` remains catalog-only; orders do not require it.

Startup lazily opens one cart store, constructs one borrowing pickup facade
with server-owned `cart.PickupCompletionAssessmentVersion`, and constructs the
orders handler with the existing verifier. That constant selects the accepted
protocol version; it does not invoke an assessment model. The facade's orders
cursor key is process-local: restarts invalidate old orders cursors, not durable
operations. The facade lives for the host run; the underlying cart store is
closed on exit, including startup failure and shutdown paths.

No migrations, seed data, fixture provisioning, live model calls, or provider
activation are added. This wiring adds no public submit endpoint and does not
start or enable a service by itself. Host-only routing tests use recorders and
lazy synthetic dependencies; they are not database, domain, r3 projection, or
connected acceptance evidence.
