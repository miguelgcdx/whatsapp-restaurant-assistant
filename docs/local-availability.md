# Local staff availability

## Use the controls

Complete [local staff setup](local-auth.md) and the [catalog setup](local-catalog.md),
then sign in and choose **View catalog**. The existing catalog still shows current-page
names, exact configured-currency prices, availability, open/closed state and plain-link
pagination. Only these two operational booleans are editable:

- An item's **Mark unavailable** / **Mark available** button.
- **Close restaurant** / **Open restaurant** above the current page.

There is no name, price, currency, fee, settings or order editor. No new configuration,
dependency, provisioning default, live service or publication is introduced. The browser
receives public DTOs, never a JWT, session object or caller authority.

## Contextual accessibility

Each item's control region is named by its visible item heading via `aria-labelledby`.
A persistent `output` (implicit status role), including when empty, uses the same heading
and has a unique ID referenced by the primary button's `aria-describedby`. Restaurant
controls retain their existing region label and have contextual feedback naming.
Existing pending, disabled and `aria-busy` behavior is unchanged.

The owned authenticated browser journey checks two item regions without positional
selectors, persistent associated feedback, and pending/conflict/uncertain/retry behavior.
These DOM assertions do not prove assistive-technology speech.

Observed contextual-accessibility verification: lint, nonincremental typecheck, all 43
component/contract tests and production build passed. The first browser run failed on
an incorrect test assumption that an action label stays unchanged after authoritative
refresh. After correcting that assertion, one explicitly authorized fresh browser run
passed the full owned journey. No preimplementation RED or manual AT validation is claimed.

## Pending, conflicts and uncertain outcomes

A deliberate click creates one random intent key with the displayed version and desired
boolean. The same control is disabled while pending and exposes accessible busy/status
feedback. A successful response says **Availability saved. Refreshing current availability.**
and refreshes the authoritative server catalog; replay acknowledgments are not fresh reads.
Even a fresh intent requesting the current boolean increments its version once.

A 409 says **Availability changed or this key was already used. Review the refreshed
availability before a new change.** It refreshes the server view. Choose **Review refreshed
availability**, inspect the new state, then deliberately choose a new change. A new key
and displayed version are used; no stale overwrite or automatic conflict retry occurs.

An interrupted, malformed, 500 or 503 response can have an unknown commit outcome. The UI
says **The result is uncertain. Retry the same request; do not start a new change.**
**Retry same request** explicitly resends the exact frozen key and JSON body. No automatic
retry occurs, and an in-place server refresh (including a sibling control's refresh)
does not rewrite/discard this intent. Keep this page open until resolved: a full reload,
navigation or closing the tab loses the in-memory intent; no browser storage is used.
An access denial disables the control and refreshes protected routing. Rejected requests
show **Availability request was rejected. Review the refreshed availability.**

## Protected write boundary

The Next BFF exposes only:

- `PATCH /api/staff/catalog/items/{canonical-lowercase-UUIDv4}/availability`
  with `{ "expected_version": "0", "available": false }`.
- `PATCH /api/staff/restaurant/availability`
  with `{ "expected_version": "0", "open": false }`.

Other methods return 405 / `Allow: PATCH`. Every PATCH requires a matching configured
Origin, an authoritative uncached Better Auth session, `application/json` (optional UTF-8
charset) and `Idempotency-Key`. Keys are 1–128 printable ASCII bytes without whitespace.
Versions are canonical nonnegative signed-int64 decimal **strings**, never JSON numbers.
The flat two-field body rejects duplicate (including escaped duplicate), unknown, missing
or null fields, invalid UTF-8, trailing JSON and queries. The raw UTF-8 body limit is
64 KiB inclusive, checked before JSON normalization. Original validated JSON bytes are
forwarded so legitimate whitespace, key order and escaped-field semantics remain valid.

Only the public server-generated JWT, validated key and explicit JSON transport headers
reach the fixed Go loopback PATCH resource. No caller Authorization, cookies, roles,
restaurant selector, proxy, redirect or automatic retry is forwarded. Transport uses
Node HTTP with no shared agent, a two-second absolute Go deadline and a 512 KiB response
bound. Fatal UTF-8 decoding and strict schema-derived DTO checks include target ID,
desired boolean and version increment. Valid Go 200 bytes, including the original replay
request ID, are returned unchanged. Responses are `no-store`; recognized errors use only
canonical safe messages and matching status/code. Unknown or corrupt results fail closed
with safe 503. See [Go wire and messages](../backend/docs/staff-availability.md).
Origin is a browser boundary, not a replacement for Go JWT/current membership authority.

## Verification and evidence ownership

Focused deterministic checks:

```sh
pnpm exec vitest run tests/contracts/staff-availability.test.ts tests/auth/staff-availability.test.ts
pnpm exec tsc --noEmit --incremental false
```

The new tests first failed on missing implementation modules. Seven tests subsequently
passed (4.90 seconds) using real generated Better Auth sessions, owned temporary PostgreSQL
and a loopback provider seam. That provider is **not** Go authorization proof. They exercise
strict DTO/body/intent contracts, required Origin, public route exports, no caller authority
forwarding, exact replay bytes, safe errors, bounds, timeout, logout and explicit retry identity.
A further client response-classifier test first failed on missing `intentOutcome`, then
the expanded focused run passed all eight tests in 3.61 seconds. The unchanged
catalog auth/render suite also passed all six tests in 3.41 seconds. Nonincremental
typecheck passed after correcting ES2017-incompatible bigint literals and a harness header
type without changing compiler configuration. Browser authorship is not a passing execution.

The extended `tests/integration/dashboard.mts` retains all earlier read/navigation,
setup/revocation/isolation/outage assertions. Its owned real Go/Next/browser journey now
exercises both controls and authoritative refresh, a stale UI conflict, same-key duplicate
convergence, competing versions, revoked membership for new/replayed writes and write
outages. It advances its exported read snapshot only after legitimate writes. Its transport
fault injection uses `route.fetch()` through the real BFF and Go, commits, then aborts only
the browser response. Explicit retry must preserve key/body and increment the version only
once, including across a sibling refresh. No fake business-success replacement is used.
No screenshots, traces, HAR, storageState, JWTs or credentials are emitted.

Parent owns actual browser execution (select the prepared browser cache **before Node**):

```sh
PLAYWRIGHT_BROWSERS_PATH="$PWD/backend/.cache/playwright" pnpm test:dashboard
```

### Observed parent results

The parent observed Go normal/vet/race passing in 24 seconds, including the same-live-request
revocation proof. Nonincremental typecheck, the full JavaScript suite (15 files / 43 tests,
18.96 seconds) and the production Next build all exited 0 (32 seconds overall). Cache
Components and Partial Prefetching remained enabled; both PATCH routes were registered.

The actual owned browser command above exited 0 in 32 seconds. It covered both controls,
authoritative refresh, stale conflicts, real commit-then-response-loss same-intent retry,
duplicate convergence, competing versions, historical replay, revoked writes, the prior
catalog/auth/setup/isolation journeys and dependency outages. The three anonymous E2E
cases also passed. These are **parent executions**, not independent browser/build proof.
The existing Node module-type warning remains. Declared 60-second JWT lifetime is not
elapsed-expiry or immediate Go-token-revocation proof.

### Independent assessment: qualified, accepted locally

One independent Go race run passed all three focused packages, including the supplied
`TestVerifierConflictingSameKeyAndFailureReuse` public-HTTP regression. One independent
Node run passed two files / eight tests in 3.74 seconds. No candidate-caused security or
concurrency blocker was established. However, that exact Node command unexpectedly
downloaded/installed **209 packages** before testing, violating the no-network/no-install
verification constraint. **S4 / overall independent verification remains PARTIAL.**

Original 175 source/document/config files, Git index and Git status were unchanged at
independent readback. Installation artifacts were observed in a private scratch dependency
copy/HOME, but no pre-run dependency-byte/link manifest existed: original/shared dependency
write isolation remains **unverified**. Source integrity does not establish dependency
integrity. Recorded pnpm/Vitest versions do not prove the actual runtime Node version.

The human explicitly accepted this exception for this local increment only, including that
remaining integrity gap, and authorized local commits. This is not compliant offline Node
verification, a clean independent PASS, publication permission or a future waiver. No
verification rerun, install, cleanup or native RDD operation was used to resolve it. The
independent Go probe was retained as a regression test; the previously observed independent
execution includes it, while the earlier parent full Go run predates its transfer. See
`odd/tasks/staff-availability-controls.md` for complete evidence and disposition.
