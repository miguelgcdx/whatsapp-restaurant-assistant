# Local synthetic callback API

This is a local ingestion fixture, **not a Meta webhook or restaurant API**. It
creates no customer orders and calls no model or external provider. The API and
worker are separate processes; PostgreSQL is the durable authority, not the Unix
wake hint. See [component wiring](component-contract.md) and [store setup](store.md).

## Start after explicit database setup

Run `cmd/local-api` from `backend` using the module's pinned toolchain and cache
settings described in store.md. Set `LOCAL_DATABASE_URL` explicitly to your
dedicated loopback `restaurant_foundation_…` database and set
`SYNTHETIC_WEBHOOK_SECRET` to a local test secret containing at least **32 bytes**.
There are no default credentials or database URL. Startup does not migrate, query
readiness or provision a database. Do not use real provider credentials or data.

Flags (`-help` lists descriptions):

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8080` | Numeric loopback host and numeric port, including bracketed IPv6 loopback; port 0 permits an OS-selected port. Wildcards, hostnames and non-loopback addresses are rejected. |
| `-wake-socket` | `.cache/worker.sock` | Nonempty worker Unix datagram socket path; relative to the process working directory. The API does not create the directory/socket. |

Invalid configuration exits unsuccessfully without echoing secrets or DSNs.
`-help` exits successfully. SIGINT/SIGTERM cancel the service and allow up to five
seconds for HTTP shutdown, then close remaining connections. Header/body read,
write and idle timeouts are 2/5/10/30 seconds; maximum request headers are 8 KiB.
No liveness or readiness route is exposed.

## Signed protocol

Send **POST `/synthetic/events`** with JSON using this exact field shape:

```json
{"event_id":"local-event-1","sender":"local-sender-1","text":" exact text 🥘 "}
```

`event_id` and `sender` are nonblank UTF-8 strings of at most 128 code points;
`text` is nonblank UTF-8 of at most 4,000 code points. Values are preserved, not
trimmed or normalized. Validation is shared with the store through
`foundation.Event.Validate`. Raw body UTF-8 is checked before JSON decoding;
unknown fields, malformed JSON and trailing JSON are rejected. JSON whitespace
is permitted. The body limit is **65,536 bytes inclusive**, including whitespace.
No particular request Content-Type is required.

Set exactly one `X-Synthetic-Signature` header to `sha256=<hex digest>`, where the
digest is HMAC-SHA256 using the configured secret and **the exact raw body bytes**.
Whitespace changes require a new signature. Comparison is constant-time. This
synthetic signature scheme is not a verified Meta integration; it has no timestamp
or replay window. Identical event replay is handled by durable identity.

All responses have `Content-Type: application/json`; bodies below include a final
newline. Routes/methods are checked before reading/authenticating/storing. For a
POST on the callback route, body-size checking precedes authentication, followed
by JSON/event validation and durable acceptance.

| Status | Exact response body (before newline) | Meaning |
| --- | --- | --- |
| 200 | `{"ok":true}` | Store.Accept completed successfully, including identical replay. |
| 400 | `{"error":"invalid_event"}` | Body read failure, malformed/trailing/unknown-field JSON, invalid UTF-8 or invalid event. |
| 401 | `{"error":"unauthorized"}` | Missing, duplicate, malformed or mismatched signature header. |
| 409 | `{"error":"event_conflict"}` | Event identity already has different content. |
| 413 | `{"error":"body_too_large"}` | More than 65,536 raw body bytes. |
| 503 | `{"error":"storage_unavailable"}` | Store acceptance did not report success. |
| 404 | `{"error":"not_found"}` | Any other path, with no storage access. |
| 405 | `{"error":"method_not_allowed"}` | Callback path with a method other than POST; includes `Allow: POST`, no storage access. |

No raw storage errors or secrets appear in response bodies. No rejected callback
initiates a job, outbound effect or wake. A transport/storage failure may leave an
**unknown commit outcome**: 503 does not prove rollback; retry the same identity
and content to use the store's idempotent acceptance. Tests prove rejection leaves
stored state unchanged for validation, authentication and identity conflicts.

## Wake and durability

After successful Store.Accept, including duplicate success, the API attempts a
wake hint. Hint failure does not change the 200 response or roll back acceptance.
`api.Notify(ctx, path)` sends only the four bytes `wake` to a Unix datagram socket,
with a 250ms maximum deadline (or the caller's earlier deadline) and cancellation.
Hints carry no event payload or authority. The worker must scan durable storage
on startup/recovery; the callback does not require a running worker.

## Focused checks

From the repository root:

```sh
cd backend && GOTOOLCHAIN=local GOCACHE="$PWD/.cache/build" GOPATH="$PWD/.cache/gopath" GOMODCACHE="$PWD/.cache/modules" go test -count=1 -timeout=30s ./internal/api ./cmd/local-api
```

Tests use a throwaway PostgreSQL cluster and temporary Unix sockets, never an
ambient DSN or live provider. They cover exact status/body responses, persistence
before success, duplicates/conflicts, rejection without stored effects, failed
wake tolerance, hint bytes/cancellation, configuration and owned HTTP shutdown.
Full API-to-worker-to-fake-provider and OS-process signal verification belong to
the parent integration checks; this focused suite is not evidence of those seams.
