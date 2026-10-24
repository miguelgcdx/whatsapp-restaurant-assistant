# Local component handoff

Parent-owned integration choices for `odd/tasks/local-foundation.md`, T2–T4.
Read that feature document's Specs before this file. This file selects wiring,
not additional product scope. Workers must not edit it or the shared store.

## Shared ownership

Reuse `internal/foundation.Event.Validate` and `foundation.Store` without changing
them. Use `internal/postgres.Open` for runtime connections. Explicit setup alone
calls `postgres.Migrate`; runtime processes never migrate implicitly. Tests may
use `internal/testpg.Start(t)` and `postgres.Migrate` on its throwaway cluster.

All process database configuration is `LOCAL_DATABASE_URL`; no default DSN.
All external HTTP in this slice is numeric-loopback-only. No real provider,
restaurant domain endpoint, auth account, model, or production configuration.
No worker may edit go.mod/go.sum, shared packages, other workers' paths or Git.
Parent owns dependency changes, integration tests, migrations, docs here and
local reviewable commits. Do not fetch a new dependency to finish a task.

## T2: API surface

Allowed files: `backend/internal/api/**`, `backend/cmd/local-api/**`,
`backend/docs/api.md`. These are disjoint from T3.

Public Go entry points:

```go
func New(store foundation.Store, secret []byte, wake func(context.Context) error) (http.Handler, error)
func Notify(ctx context.Context, socketPath string) error
```

Expose `POST /synthetic/events`, accepting the exact `foundation.Event` JSON
shape. It is deliberately not a Meta payload. Limit the raw body to 65,536 bytes;
reject malformed/trailing JSON and unknown fields. Check raw UTF-8 before JSON
decoding (Go's JSON decoder otherwise replaces invalid bytes). Validate exact
UTF-8 event text using the shared helper. Verify HMAC-SHA256 of the exact raw bytes from
`X-Synthetic-Signature: sha256=<hex digest>` with constant-time comparison.
The configured secret must contain at least 32 bytes; there is no default.

Successful new or identical duplicate ingestion responds 200 only after
`Store.Accept` succeeds. Same-identity/different-content is 409; signature failure
is 401; malformed event/JSON is 400; oversized body is 413; persistence failure
is 503. Use small fixed JSON errors, never raw credentials, DSNs or database
errors. Wake hints happen after commit and are best-effort; their failure cannot
turn a durable accepted event into a rejected HTTP response. Unknown routes and
methods must not reach storage. Any liveness route must not query storage or
claim database readiness. Document exact bodies/statuses chosen by this unit.

`Notify` sends a constant tiny hint over a Unix datagram socket, with a bounded
context/deadline and no event payload. Hint receipt carries no authority.

`cmd/local-api` uses net/http ServeMux-compatible handling, owned signal shutdown,
bounded server timeouts and a numeric-loopback-only `-listen` flag (default
`127.0.0.1:8080`). Use `-wake-socket` default `.cache/worker.sock` and environment
`SYNTHETIC_WEBHOOK_SECRET`. No setup or migration occurs on startup. Give every
flag useful help; invalid configuration must fail without secret disclosure.

## T3: worker and fake sender surface

Allowed files: `backend/internal/worker/**`, `backend/cmd/local-worker/**`,
`backend/docs/worker.md`. These are disjoint from T2.

Public Go entry points:

```go
type Sender interface {
    Send(context.Context, foundation.Delivery) foundation.SendResult
}
type Options struct {
    SocketPath string
    RecoveryInterval time.Duration
    Lease time.Duration
    SendTimeout time.Duration
}
func Run(ctx context.Context, store foundation.Store, sender Sender, opts Options) error
func NewFakeSender(endpoint string) (Sender, error)
```

Zero-valued durations select documented defaults: recovery 30 minutes, lease
30 seconds, send timeout 5 seconds. Require positive configured values and send
timeout shorter than the lease. Work is serialized/bounded in this first worker;
multiple processes are fenced by the store's conversation ordering and leases.

The fake job operation produces the fixed text `Local test acknowledgement.`;
it does not interpret the customer message, create an order, or call a model.
Drain at startup and on hint, persisted due-time timer and recovery scan. Use
`NextDue` to schedule retries and expired leases without repeated idle queries.
Every job/delivery claim and completion goes through the shared Store. Do not
hold a database transaction around Send or blindly retry unknown sends. A DB
failure must not cause an unbounded immediate query loop. Cancellation releases
owned resources; normal context cancellation should let Run return nil.

Own a Unix datagram socket under a private same-user directory. Guard against
active-worker replacement, symlinks, and deleting a socket created by another
process. An owned stale socket after a crash must permit safe restart; an
exclusive same-user local lock with safe file handling is acceptable. Socket
payloads are ignored. Close the socket on shutdown and remove only owned
resources. Do not add a daemon or touch system/home configuration.

`NewFakeSender` only accepts explicit http(s) numeric-loopback URLs with no URL
credentials. Disable environment proxies and redirects, bound dial/request time
and response bodies (65,536 bytes). POST JSON `event_id`, `effect_id`, `text`;
the fake provider's known acceptance is 200 with valid nonblank `message_id`
(at most 128 UTF-8 code points). Map that to SendAccepted/ProviderID. Document
which explicit fake rejection responses are definitely failed; timeouts,
malformed acceptance, ambiguous responses and failures after possible dispatch
are unknown, never definitely failed. No exactly-once delivery claim.

`cmd/local-worker` uses `LOCAL_DATABASE_URL`, explicit `FAKE_PROVIDER_URL`, and
flags `-wake-socket` (default `.cache/worker.sock`) and `-recovery-interval`
(default `30m`). Give flags help, validate config and own signal cancellation.
No implicit migrations, provider credentials or test database provisioning.

## Checks and reporting

Use test-first for each assigned behavior. Run focused package tests and report
actual RED/GREEN, not test presence. Preserve exact stdout/stderr/exit outcomes
for probes. Add prior-behavior checks only where shared changed code warrants
them; no previous Go commands exist to compare with the base. Keep tests and
unit docs with the behavior. Use disposable state for mutable probes.

Cache environment from backend: `GOTOOLCHAIN=local`,
`GOCACHE="$PWD/.cache/build"`, `GOPATH="$PWD/.cache/gopath"`,
`GOMODCACHE="$PWD/.cache/modules"`. Format only your allowed files, not the whole
backend while the sibling writes. Parent runs the combined suite and HTTP seam.
Do not commit or publish. Report coverage for S1/S2 and unmet/uncertain items.
