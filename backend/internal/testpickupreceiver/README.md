# Fake LOCAL HTTP receiver

This isolated, standard-library-only helper implements T3R's fake receiver seam.
The parent observed native functional RED against the initial 501 scaffold:
the package compiled and ran, then failed HTTP behavior assertions. After the
ACK server and synchronized recording were implemented, the parent's normal,
race, and vet checks all passed. Independent receiver-component verification
also passed. This does not establish connected pickup acceptance.

## Public API

- `New(t testing.TB) *Receiver` creates an `httptest` loopback server and registers
  its close operation with `t.Cleanup`. No persistent listener or shared globals.
- `receiver.URL()` is the message endpoint; send JSON by HTTP POST.
- `receiver.Client()` is a reusable HTTP client with a five-second timeout.
  Server header/read/write/idle timeouts are also five seconds.
- `Message{CorrelationID, Text}` uses JSON `correlation_id` and `text`.
- `Acknowledgement{CorrelationID, ReceiverReference, ContentSHA256}` uses JSON
  `correlation_id`, `receiver_reference`, and `content_sha256`.
- `receiver.Received()` returns a defensive, mutex-protected snapshot of accepted
  request messages in recording order, including each replay attempt.
  Concurrent arrival ordering is not specified. Before acceptance it returns nil.

## Receiver behavior

A POST is read and decoded by the server, not passed by a direct callback.
Success is HTTP 200 and one JSON acknowledgement. Its correlation echoes the
received host message/intent identifier. Its nonblank receiver reference is
`local-` plus unpadded URL-safe base64 of the correlation bytes: stable across
receiver instances, distinct for distinct correlations, and independent of any
request counter. Its lowercase hexadecimal SHA-256 is computed from the exact
decoded `Text` UTF-8 bytes, **not** raw JSON, a client-supplied digest, or model
output. Unknown fields, including extra receipt-shaped request fields, are
ignored. Unicode, whitespace, and newlines remain unchanged in recorded text.

Same-correlation/same-text replays return the same acknowledgement and are each
recorded as actual HTTP requests. There is no business validation or deduplication;
the reference identifies the correlation, not a unique network attempt.
Snapshot mutation cannot alter stored records. A mutex protects both append and
snapshot copying; message fields are value strings. The record is appended
before the response is written. A failed response write does not undo actual
receipt or establish that the client observed an ACK.

Invalid JSON returns HTTP 400 with `invalid JSON\n`; a non-POST method returns
HTTP 405 with `method not allowed\n` and `Allow: POST`. Rejected requests record
nothing and do not affect later references. Request bodies are limited to 64 KiB
of encoded JSON; read failures and oversized bodies also return the same 400
error without recording. This is a fixture resource bound, not an order-size
policy or a generic parser/security framework.

The unchanged external-package tests use real HTTP requests and decode actual
response bodies. They check status, JSON shape, correlation, text digest, stable
reference, exact records, replay counts, defensive readback, eight concurrent
requests with snapshot readers, and rejection effects. No existing command or
shared production source is changed.

## Verification

Parent-reported functional RED, from `backend`:

```sh
/usr/bin/go test -mod=readonly -count=1 -timeout=60s ./internal/testpickupreceiver
```

Job 3 exited 1 after about 10 seconds. Stdout reported 501 instead of 200 for
success cases; 501 instead of 400/405, error-body mismatches, and missing `Allow`
for rejection cases; and zero concurrent records instead of eight. The package
reported `FAIL` in 0.008s. Stderr was empty. This was a compiled functional
failure, not a missing API or compiler failure. Parent evidence is retained at
`/tmp/restaurant-pickup-receiver-qGrISV/receiver-red.{command,exit,stdout,stderr}`.

The parent ran these new-package-only checks from `backend`, using the existing
private temporary directory, owned compiler cache, public module cache in place,
and offline Go settings. All three exited 0 with empty stderr: normal tests in
0.010s, race tests in 1.030s, and vet with empty stdout. Job 4 completed in about
18 seconds. Raw command/exit/stdout/stderr files use the same directory and
`receiver-green`, `receiver-race`, and `receiver-vet` prefixes.

```sh
TMPDIR=/tmp/restaurant-pickup-receiver-qGrISV TMP=/tmp/restaurant-pickup-receiver-qGrISV TEMP=/tmp/restaurant-pickup-receiver-qGrISV /usr/bin/go test -mod=readonly -count=1 -timeout=60s ./internal/testpickupreceiver
TMPDIR=/tmp/restaurant-pickup-receiver-qGrISV TMP=/tmp/restaurant-pickup-receiver-qGrISV TEMP=/tmp/restaurant-pickup-receiver-qGrISV /usr/bin/go test -mod=readonly -race -count=1 -timeout=60s ./internal/testpickupreceiver
TMPDIR=/tmp/restaurant-pickup-receiver-qGrISV TMP=/tmp/restaurant-pickup-receiver-qGrISV TEMP=/tmp/restaurant-pickup-receiver-qGrISV /usr/bin/go vet -mod=readonly ./internal/testpickupreceiver
```

## Independent component verification

A separate verifier used `/tmp/restaurant-pickup-receiver-verify-myilUO`, an
isolated copy with its own HOME, temporary storage, and writable compiler cache.
The original suite and three independent real-HTTP probes passed: normal tests
0.513s, race tests 1.538s, and vet with empty stdout. Each command exited 0 with
empty stderr. Probes checked exact decoded-text binding, stable correlations,
the 65,536/65,537-byte request boundary, rejection preserving pre-existing
records, and concurrent snapshot ownership. No blockers or advisories were found.
The parent then reran the normal package check: exit 0, 0.008s, empty stderr.
Probe sources and raw evidence remain in that disposable scratch directory;
no duplicate probe suite was added to the repository.

## Scope and evidence limits

This component implements only the receiver seam for S1–S3 and S5–S7, with
parent normal/race/vet checks and independent component verification passed. It does not satisfy connected pickup acceptance. A future bridge must decode the actual HTTP response before
invoking cart presentation/reply methods; that bridge is absent. There are no
cart, stafforders, AI, provider, dispatcher, queue, or persistence imports.
Receipt/replay behavior here does not establish exactly-once external delivery,
order persistence, business/cart correctness, staff processing, natural-language
interpretation, or production outcome/pre-send protocols. The parent race check
passed for concurrent recording, and the required independent check passed for
this component. No broader item-4 verification gap is closed by these checks.
