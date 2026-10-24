# Local synthetic worker

`cmd/local-worker` is a separate, foreground Go process. It turns each durable
synthetic job into exactly one **logical outbox record** containing
`Local test acknowledgement.` It does not interpret inbound text, make orders,
call a model, or integrate with Meta. One logical record does not establish
exactly-once external delivery.

## Run locally

First provision and explicitly migrate a dedicated, disposable local database
using the parent-owned setup/integration instructions. This process never
migrates or provisions a database. `LOCAL_DATABASE_URL` has no default and must
satisfy the [store's numeric-loopback and database-name guard](store.md).
Set `FAKE_PROVIDER_URL` to the explicit numeric-loopback HTTP(S) endpoint of your
local fake provider; there are no provider credentials or live-provider defaults.

From `backend`, after setting those two variables:

```sh
go run ./cmd/local-worker -wake-socket .cache/worker.sock -recovery-interval 30m
```

The socket's immediate parent directory must be same-user and private (mode
`0700`); the worker creates it if absent, but will not loosen or change an
existing directory's permissions. In particular, an existing `.cache` directory
must already meet this requirement, or select a dedicated private directory
under `.cache` with `-wake-socket .cache/worker/worker.sock`. Its parent must
already exist. Symlink directories, symlink lock/socket paths, foreign owners,
non-socket replacements, and a second active worker on the same socket are
rejected. No home or system configuration is used. `-h` lists both flags and
exits successfully without reading database configuration.

SIGINT/SIGTERM cancel processing, interrupt the current send, join the socket
reader, close resources, and remove only the socket inode this process created.
The same-user exclusive `.sock.lock` file is deliberately retained after exit:
unlinking a lock could permit two processes to lock different inodes at one
pathname. An inactive same-user socket left by a crash is reclaimed only after
taking that lock and checking it is not still accepting datagrams. These are
local cooperative-process protections, not isolation from malicious code running
with the same OS user. Filesystem directory ownership is part of the boundary.

## Scheduling and recovery

The exported component is `Run(ctx, store, sender, Options)`. The caller owns the
store; Run owns only its socket, reader, and timers. Zero duration options select:

| Option | Default |
| --- | --- |
| `RecoveryInterval` | 30 minutes |
| `Lease` | 30 seconds |
| `SendTimeout` | 5 seconds |

Configured durations must be positive and `SendTimeout` must be shorter than
`Lease`. A sender must honor its context. Work is serialized, including sends;
multiple processes rely on the shared Store's generation fencing and per-sender
ordering, not an in-memory worker lock for database correctness.

The worker drains on startup, ignored-payload Unix datagram hints, the earliest
persisted `NextDue` deadline, and the recovery interval. Missed hints cannot erase
committed work. Between these triggers it makes no store queries. A due deadline
racing another worker has a 10ms timer floor, avoiding an immediate busy loop.
The Store excludes ineligible blocked successors from its deadlines. Database
errors terminate Run rather than retrying queries without backoff; restart is an
explicit process-owner decision. The command emits only a fixed redacted error
and exits 1 on configuration/persistence failure; normal cancellation exits 0.

All claims/completions use the shared Store. Sends occur after durable send
intent, outside Store transactions. If cancellation or failure prevents saving
a send result, expiry of the persisted send lease becomes **unknown**, not a new
send. A job crash instead permits generation-checked job reclamation. Unknown
sends block subsequent sends to that sender and require reconciliation, which
this slice intentionally does not implement.

## Fake outbound protocol

`NewFakeSender(endpoint)` accepts only explicit `http`/`https` numeric-loopback
URLs, without URL credentials or fragments. Hostnames, non-loopback IPs and
other schemes are rejected. It never uses environment proxies or follows HTTP
redirects; its dialer also checks the numeric loopback destination. HTTPS retains
normal certificate verification. The reused client/transport disables idle
keep-alive connections and has a five-second overall request limit, bounded dial,
TLS and response-header timeouts, and a 65,536-byte response/header budget.
Run additionally applies `SendTimeout` (the earlier bound wins).

Every send is `POST` with `Content-Type: application/json`, containing precisely:

```json
{"event_id":"synthetic-event-id","effect_id":"durable-outbox-id","text":"Local test acknowledgement."}
```

The fake server's **200** response establishes acceptance only when its body is
one valid UTF-8 JSON object with a nonblank string `message_id` of at most 128
Unicode code points. That ID is preserved as `SendAccepted/ProviderID`.
Under this explicitly local protocol, **400** and **422** guarantee rejection
before any acceptance and map to `SendFailed` when their bounded response body
is read successfully. All other statuses (including redirects), malformed or
oversized acceptance bodies, response-read failures, transport errors and
timeouts map to `SendUnknown`. A dispatch might already have happened in those
cases. No failed/unknown delivery is automatically resent by this worker.

## Focused verification

```sh
cd backend && GOTOOLCHAIN=local GOCACHE="$PWD/.cache/build" GOPATH="$PWD/.cache/gopath" GOMODCACHE="$PWD/.cache/modules" go test -count=1 -timeout=30s ./internal/worker ./cmd/local-worker
```

Tests use disposable directories, loopback `httptest` providers, in-memory Store
substitutes and the shared throwaway PostgreSQL fixture (`initdb`, `pg_ctl`,
`createdb` required). They never read an ambient DSN. Coverage includes triggers,
idle query absence, failure without spinning, cancellation, socket ownership,
stale restart, send outcome propagation and transport/body guards, plus persisted
retry/restart behavior against the real Store. The combined API/HTTP seam,
full-suite vet/race and delivery review remain parent-owned validation.
