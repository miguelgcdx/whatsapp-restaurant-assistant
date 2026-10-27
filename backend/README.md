# Local synthetic ingestion foundation

Receive a signed fake message, commit one PostgreSQL job, process it in a separate
Go worker, and send a fake acknowledgement. This is **not** a working restaurant
ordering product, Meta integration, AI integration, or production service.
No real provider credentials, customer data, staff accounts or payments belong here.

## Local catalog and pricing foundation

The optional [catalog schema](docs/catalog-schema.md), [scoped catalog store](docs/catalog.md)
and [pure draft pricing](docs/pricing.md) add an explicitly configured fictional-test
foundation. The integration test maps persisted fixed prices/settings into the pure
engine and checks totals and availability without dropping existing draft units.
The additive [local fixture cart core](docs/cart.md) now persists one draft per
explicit local conversation, stable priced units and notes, CAS versions and
complete historical operation results. Native Go APIs only: it is not a persisted
verified-customer identity, customer/staff cart endpoint, contextual confirmation,
order submission or receipt. Unselected fulfillment has a subtotal only, never an
invented pickup choice or final order total. No production menu/defaults are seeded.
The complete Go checks below include this foundation; no new CLI is introduced.
Focused cart checks and populated-v4 upgrade evidence are described in the cart
guide. Parent full normal/vet/race checks passed; independent setup timed out before
runtime tests. The [accepted LOCAL-only proof limitations](docs/cart.md#observed-results-and-local-dispositions)
retain missing independent runtime and supplemental pre-implementation RED history;
they are not production approval or a future waiver.

The additive [local conversation-to-cart execution bridge](docs/cart-execution.md)
now connects an already persisted synthetic event, explicit local fixture plan,
and actual public job claim to native cart create/edit/read operations. Migration 6
adds two empty immutable plan tables, with stable per-event/slot operation IDs.
Authority and late-expiry/cancellation rollback share the cart transaction; bound
reads remain fresh, and direct cart APIs retain their existing semantics. This is
not automatic worker routing, event-text interpretation, customer authentication,
confirmation or order submission. New focused writer evidence is separate from the
historical cart-core checks above. This increment's final parent normal/vet/race
checks passed. Independent setup failed offline before probes; the human accepted
that explicitly documented LOCAL-only proof limitation, not an independent PASS.
See the [execution evidence](docs/cart-execution.md#final-local-evidence-and-disposition).

The additive [local draft fulfillment and quote seam](docs/cart-fulfillment.md)
adds explicit pickup/delivery, exact address/phone validation, and server-calculated
fee/total quotes through `SetFulfillmentCall`, `QuoteCall` and `ExecuteDraft`.
Migration 7 adds an empty scoped selection side table and widens existing action
checks. Legacy subtotal bytes/APIs remain unchanged; rich mutations share their
operation ledger and cart CAS version. Quotes sample settings in the same guarded
transaction and never imply pickup before selection. This remains native-only,
with no automatic worker dispatch, HTTP/CLI surface, confirmation or orders.
Parent full normal/vet/race checks and the final independent assessment passed
for this native LOCAL scope. The [final evidence](docs/cart-fulfillment.md#final-local-evidence)
separates those executions, adopted regression probes and bounded integrity claims;
it does not imply production or downstream ordering approval.

The additive [local provisional preparation calculator](docs/preparation.md)
models three parallel ten-minute slots from explicit native snapshots. It returns
provisional preparation-only readiness, or no estimate for overdue preparation,
without changing statuses or blocking submissions. This is not real-order,
confirmation, summary, HTTP or CLI integration.

The additive [immutable LOCAL summary preview seam](docs/cart-summary-preview.md)
captures exact names, stored unit prices/notes, fulfillment and server totals from
one guarded transaction, then reads or replays that immutable snapshot. Migration
8 adds one empty scoped preview table; captures share the existing mutation ledger
and do not increment cart version. Preparation estimate stays null with explicit
`workload_not_connected`; no invented empty kitchen or workload timestamp is used.
This is **not complete `prepare_summary`**, presentation evidence, confirmation or
submission. Parent full normal/vet/race checks and one final independent assessment
passed for this bounded LOCAL scope. The [final evidence](docs/cart-summary-preview.md#final-local-evidence)
keeps their attribution, integrity limits and historical quota failure explicit;
it does not imply downstream or production approval.

The isolated [offline-tested CommandCode Go boundary](docs/commandcode.md) adds a
bounded one-turn client and fabricated-key wire tests for the selected model. It
is not wired into this pipeline, executes no tools, and proves no live gateway or
account compatibility. The guide's native example uses an injected transport only.

The optional [claim-bound durable LOCAL menu reply seam](docs/claim-bound-dispatch.md)
adds native `RunAndQueue`, historical `ReadQueued`, and one-attempt `DispatchOne`.
Migration 10 binds AI intents to source/control/creation claims, isolates them
from legacy acknowledgement scheduling, and adds known-no-I/O suppression.
The explicitly configured coordinator uses the existing injected fake sender;
it does **not** route the default worker through AI or add live-provider/UI/order
integration. Unknown sends are never automatically retried. Focused D2 evidence
is not parent full-suite/independent approval; see the guide's evidence limits.

The [LOCAL menu-message route](docs/local-menu-runtime.md) adds migration11
provenance, legacy consumer fencing and native atomic `AcceptMenu`/`ReadAdmission`.
Admission requires existing owned LOCAL setup and authenticated control; historical
receipts never grant current AI permission. Scoped execution, epoch-aware gate
integration and a runnable menu host are still pending.

## Verify the complete local loop

Prerequisites: Go 1.27.3 and local PostgreSQL binaries `initdb`, `pg_ctl`, `createdb`.
Tests start isolated throwaway clusters and loopback HTTP servers; they never read
an existing database URL. Missing prerequisites fail rather than skip.

From the repository root:

```sh
cd backend
export GOTOOLCHAIN=local GOCACHE="$PWD/.cache/build"
export GOPATH="$PWD/.cache/gopath" GOMODCACHE="$PWD/.cache/modules"
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=180s ./...
```

The full race suite uses a 180-second per-package test-runner budget for
race-instrumented real PostgreSQL integration work; the normal suite stays at
120 seconds. All tests, assertions and internal 3-second authority-observer
deadlines remain unchanged. This budget is not a production request, lease or
order-policy timeout, a quality waiver, or evidence that a run passed. Historical
120-second executions and failures remain recorded unchanged.

The process seam test builds and starts the actual API, worker and explicit
migration command. It exercises concurrent replay, persistence while the worker
is absent, startup drain, wake hints, graceful shutdown and restart with backlog,
exact process output, and one logical effect per event. Run just that check with:

```sh
go test -count=1 -timeout=120s ./internal/integration
```

These commands are verification instructions, not an assertion that a particular
run passed. See `../odd/tasks/local-foundation.md` for recorded results and gaps.
Unix socket tests use compact private temporary names rather than full test names.
PostgreSQL test connections are TCP-only. The operating system still imposes a
socket-path limit: an excessively long TMPDIR itself cannot be made usable by
shortening only child names.

## Optional manual processes

Everything below is a **local synthetic fixture**. Do not use Neon, a tunnel,
provider accounts or a database containing valuable data. Run commands from
`backend`, with the cache exports above, in separate terminals as indicated.
The sample ports must be free. Choose fresh dedicated paths; do not remove or
replace an existing database directory to make these commands succeed.

### 1. Create an owned disposable PostgreSQL cluster

```sh
initdb -D .cache/manual-pg -U localtest -A trust --no-locale -E UTF8
pg_ctl -D .cache/manual-pg -l .cache/manual-pg.log \
  -o "-h 127.0.0.1 -p 55432 -k ''" -w start
createdb -h 127.0.0.1 -p 55432 -U localtest restaurant_foundation_manual
```

`localtest` is an isolated PostgreSQL role, **not an application staff account**.
Trust authentication here is only for throwaway data on a loopback-bound server;
it is not a production authentication recommendation.

Set these test-only variables in each service terminal:

```sh
export LOCAL_DATABASE_URL='postgres://localtest@127.0.0.1:55432/restaurant_foundation_manual?sslmode=disable'
export SYNTHETIC_WEBHOOK_SECRET='synthetic-local-test-secret-32-bytes'
export FAKE_PROVIDER_URL='http://127.0.0.1:8091/send'
export WAKE_SOCKET='.cache/manual-run/worker.sock'
```

Apply embedded Goose migrations **explicitly**, before either service starts:

```sh
go run ./cmd/local-migrate
mkdir -m 700 .cache/manual-run
```

Migration success prints exactly `local foundation migrations applied` plus a
newline. Missing/unsafe configuration or migration failure exits unsuccessfully
with a redacted error; `-help` describes the environment on stderr and exits 0.
No database creation or destructive down migration is offered. Reapplying the
current migration preserves existing synthetic records. Interrupted setup is not
a claimed success; retry the explicit idempotent command after diagnosis.

The socket directory must be private and same-user; the worker refuses unsafe
existing permissions rather than changing them. Keep the socket path **relative**
and short, and start both services from the same backend working directory.

### 2. Start a toy fake provider in another terminal

Python 3 is needed only for this optional fixture, not the Go test suite:

```sh
python3 - <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
import json

class Fake(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        if self.path != "/send" or not 0 < length <= 65536:
            self.send_error(422)
            return
        event = json.loads(self.rfile.read(length))
        body = json.dumps({"message_id": "fake-" + event["effect_id"]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

HTTPServer(("127.0.0.1", 8091), Fake).serve_forever()
PY
```

This toy fixture does not deliver messages anywhere or model a real provider's
acceptance guarantees. Its request contract is documented in [worker.md](docs/worker.md).

### 3. Start the API and worker in separate terminals

With the same environment and backend working directory in each:

```sh
go run ./cmd/local-api -listen 127.0.0.1:8080 -wake-socket "$WAKE_SOCKET"
```

```sh
go run ./cmd/local-worker -wake-socket "$WAKE_SOCKET" -recovery-interval 30m
```

Both commands support `-help`. Neither migrates, provisions accounts or installs
services. SIGINT/SIGTERM request owned shutdown. The retained socket lock file is
intentional; it is not evidence that another worker is still running.

### 4. Submit one synthetic message

In another terminal with the same test secret:

```sh
python3 - <<'PY'
import hashlib, hmac, json, os, urllib.request

body = json.dumps({"event_id": "manual-event-1", "sender": "manual-customer",
                   "text": "exact synthetic test text"}).encode()
signature = hmac.new(os.environ["SYNTHETIC_WEBHOOK_SECRET"].encode(), body,
                     hashlib.sha256).hexdigest()
request = urllib.request.Request("http://127.0.0.1:8080/synthetic/events", body,
    headers={"X-Synthetic-Signature": "sha256=" + signature,
             "Content-Type": "application/json"})
with urllib.request.urlopen(request, timeout=10) as response:
    print(response.status, response.read().decode(), end="")
PY
```

Expected ingestion response: `200 {"ok":true}` plus a newline. The worker sends
`Local test acknowledgement.` to the toy provider. Repeating the same event and
content is a no-op; reusing its identity with changed content is a conflict.
HTTP 200 proves durable ingestion, **not delivery**. A lost wake hint is recovered
by startup, a persisted due timer or the configured recovery scan; it may wait
up to the scan interval. A 503 can represent an unknown commit outcome—retry the
same identity/content, do not invent another identity to hide it.

Stop only the processes/cluster you started: Ctrl-C in each foreground service
terminal, then `pg_ctl -D .cache/manual-pg -m fast -w stop`. Keep or explicitly
manage your own fixture data; these instructions do not delete directories.

## Boundaries and navigation

- [API protocol](docs/api.md): exact signatures, input bounds and responses.
- [Worker](docs/worker.md): scheduling, socket ownership and fake send outcomes.
- [Store](docs/store.md): atomic claims, ordering, migrations and dependency pins.
- [Parent component contract](docs/component-contract.md): ownership and wiring.

Unknown sends are never blindly retried and require future reconciliation.
Generations fence database completion, not a request already in flight. There
is no exactly-once external-delivery guarantee, free-tier uptime guarantee,
business ordering workflow or model integration in this pipeline. The separate
[local staff authentication/access boundary](docs/staff.md) has its own setup,
authority and verification limits; it does not add order workflows.
