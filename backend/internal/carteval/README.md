# Scripted offline cart evaluation fixture v1

Candidate **`carteval-fixture-v1`**, JSON schema version **1**. This is a test-only
entry point around unchanged MAIN public APIs, not a new caller or mapper.
Source alone is not functional proof: consult the run-specific raw receipts and
case records for execution status. No historical runtime/R5/R6 result or frozen hash is amended.
The parent fingerprints the final three source files separately for handoff.

## Run only this package

Working directory: repository `backend/`. Prerequisites: installed local Go
**1.27.3** (per `go.mod`), `initdb`, `pg_ctl`, and `createdb` on the selected PATH;
non-root user able to start disposable PostgreSQL and bind numeric loopback.
Race additionally requires a working local C compiler/cgo toolchain. No database
URL, provider key, real auth service, frontend, or live model is required or read
by this fixture. Do not use `-short` or filtered case leaves for acceptance: C01
and C02 are intentionally compared within the complete evaluation test.

These are proposed parent-run commands, **not execution receipts or permission
to rerun a stopped check**. The parent owns its at-most-four-Go-command ledger,
raw safe stdout/stderr/exit capture and process deadlines (test process 210s,
outer 240s; vet process 25s, outer 30s). Run commands separately in foreground;
stop on an unexpected setup/compile/test/timeout/integrity failure.

Use the already allocated private owned nonsymlink mode-0700 root for this run.
Set these literal values on every command/subprocess (including formatter):

```sh
export TMPDIR=/tmp/restaurant-carteval-sFXswG/tmp
export TMP=/tmp/restaurant-carteval-sFXswG/tmp
export TEMP=/tmp/restaurant-carteval-sFXswG/tmp
export HOME=/tmp/restaurant-carteval-sFXswG/home
export PATH=/usr/bin:/bin
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOENV=off
export GOCACHE=/tmp/restaurant-carteval-sFXswG/cache/build
export GOPATH=/tmp/restaurant-carteval-sFXswG/cache/gopath
export GOMODCACHE=/home/miguelgcd/Projects/ai/rags/rag1/whatsapp-restaurant-assistant/restaurant-assistant/backend/.cache/modules
export GOFLAGS='-mod=readonly -p=2' GOMAXPROCS=2 GOTELEMETRY=off

go test -json -count=1 -timeout=180s -run '^(TestCartEvaluationFixture|TestSemanticContentsComparison|TestSnapshotDelta|TestEvaluationRecordSchema)$' ./internal/carteval
go test -race -json -count=1 -timeout=180s -run '^(TestCartEvaluationFixture|TestSemanticContentsComparison|TestSnapshotDelta|TestEvaluationRecordSchema)$' ./internal/carteval
go vet ./internal/carteval
```

The module-cache path above names the existing public backend dependency cache,
not any stopped proof's private cache. Before execution the parent checks the
required module directories from `go.mod`; missing dependencies remain a blocker.
The fixture uses that populated source cache without downloads or source changes;
compiled objects and GOPATH are task-private. No blanket copy, installer, old proof
cache, toolchain download or environment repair is authorized. Missing
cache/toolchain/PostgreSQL prerequisites are concrete blockers, not skipped proof.
Do not relocate caches into the worktree. Reapply the environment in independent
shells; exports in this example do not configure a later tool invocation.

## Selected tests and scope

| Go test / case subtest | Level and actual script | Required observations |
|---|---|---|
| `TestCartEvaluationFixture/C01` | I/component: public catalog browse, then `add two burgers` | Browse read-only; stored B/W/S projection; PENDING unchanged; actual native execution and Read agree on two B at 700 each, no notes, subtotal 1400, version 1. |
| `TestCartEvaluationFixture/C02` | I/component: `add two burgers` without browse | Independent empty draft; semantic contents equal C01 ignoring generated IDs; discover and execute actual pending work. |
| `TestCartEvaluationFixture/C03` | I/component: `add two burgers` | Independent empty chain start, not another addition onto C01/C02. |
| `TestCartEvaluationFixture/C09` | I/component: `add one pizza` | Known supported-grammar unknown-menu-NAME clarification; exact `menugate.ErrClarificationRequired`, zero transport/reference, no discoverable work, unchanged public draft/version. Not the original `Add a pizza.` or unknown-ID proof. |
| `TestCartEvaluationFixture/C10` | I/lower-level-component: trusted native `Add(S,1)` | Exact `cart.ErrUnavailableItem`, zero native reference, unchanged draft/version, no discoverable work. Not AddCaller/NL or existing-order preservation. |
| `TestCartEvaluationFixture/C11/{price,authority,cart}` | D/component: structured Add with one extra field | Exact `cartintent.ErrInput` and zero `cart.Call`; valid companion equals public constructor-built call bound to trusted cart outside JSON. Neither proposal admitted/executed. Public draft/version and control/job provenance unchanged. |
| `TestCartEvaluationFixture/{C04,C05,C06,C07,C08,C12}` | Deferred | Explicit NOT_RUN records; no case fixture or operation invoked. |
| `TestSemanticContentsComparison` | Pure helper | Ignores unit IDs/order; detects note, price, menu, currency, exponent, quantity and subtotal differences. |
| `TestSnapshotDelta` | Pure helper | Version/quantity/subtotal differences and unchanged delta. |
| `TestEvaluationRecordSchema` | Pure helper | Safe envelope and public snapshot round-trip; explicit zero call/counters; pending metadata never fabricates ExecuteResult. Emits no case receipt. |

Synthetic stored menu: USD exponent 2; B Burger 700 and W Water 200 available;
S Soup 500 unavailable. Setup uses public catalog Initialize, EnsureMember,
ProvisionLocal, **conversation.Initialize**, Create, AcceptMenu and ClaimMenu.
Every independent component/subvariant has fresh conversation, sender, event,
claim and empty draft. One owned cluster is shared sequentially; there is no SQL
queue cleanup, private storage inspection or direct control SQL insertion.
C02 discovers via ClaimNextNativeEdit; C01/C03 recover their actual pending ref.
Execution uses the existing ExecuteNativeEdit consumer, not duplicated rules.

## Safe evidence envelope

`go test -json` carries `CART_EVAL_RECORD {JSON}` in output events; do not mistake
the surrounding Go event for the inner record. One record per case/subvariant is
logged at subtest cleanup, including failure paths reached after `begin`.
A setup failure before cases produces no case records: report missing mandatory
public evidence as BLOCKED/NOT_RUN, never infer PASS from missing output.

Schema fields:

- Identity/scope: `schema_version`, `candidate`, `case`, `variant`, `level`,
  `actual_script_not_original_NL`, optional `trusted_setup_scope` (safe synthetic
  restaurant/conversation/sender/event/job/cart/create-operation/staff IDs).
- Assertions/disposition: `expected`, `actual`, `error_identity`, optional
  `zero_call`, `component_outcome`, `outcome`, `gates`. PASS means only the named
  executed component's assertions succeeded; FAIL reflects a failed subtest;
  NOT_RUN means deferred/not attempted. BLOCKED is a handoff disposition for
  mandatory missing observations; it must not be silently promoted to PASS.
- Public observations: `before`, `pending_or_rejected`, `after` are actual public
  cart Snapshots (IDs, version, currency, exponent, units with actual unit/menu IDs,
  notes and stored minor prices, subtotal). `pending_delta` and `final_delta`
  carry version, quantity and subtotal_minor changes. `public_browse` contains
  actual catalog settings/items. `job_before`/`job_after` are public Inspect
  projections, not raw database dumps or hidden cart-mutation counters.
- Pending versus execution: optional `pending_operation_id`,
  `pending_reference_state`, `scripted_proposal_arguments`, and **separate**
  `execute_result` from the actual consumer. PENDING is not a cart mutation.
- Counts: `invocations_excluding_setup` records exact direct invocations for
  transport, AdmitAdd, direct AdmitNativeEdit/Decode/Read/catalog Snapshot,
  ClaimNextNativeEdit/RecoverNativeEdit/ExecuteNativeEdit, Inspect/control Read/
  ReadAdmission. `returned_outcomes_not_mutation_counts` separately records
  pending references, clarifications, native/decode rejections, valid unadmitted
  companions, acquired work, empty discoveries and executed results.
- Limitations: `unavailable_observations` explicitly labels private AddCaller
  ReadMenu/internal-admission/read counts and durable cart-mutation counters
  **UNAVAILABLE**. Outer calls and returned outcomes never substitute for them.

No key, token, DSN, request authorization header or private signing material is
logged, including fabricated values. Synthetic signing keys/tokens stay in memory;
model transport is injected in-process and never delegates network I/O. Only the
owned numeric-loopback httptest JWKS server is used for public staff bootstrap.
Migration/job-store driver errors are withheld from fixture failure messages.

## Ownership, cleanup and limits

The Go test owns its cluster/JWKS server and clients. `testpg.Start` uses TCP-only
PostgreSQL and `t.TempDir()` under the explicit TMPDIR, stopping its owned cluster
before testing removes its directory. LIFO cleanup closes callers/stores and the
owned JWKS server before cluster teardown; no existing DSN is accepted. The parent
owns retained run-root caches/results/control and their eventual cleanup; this
writer creates no scratch directories and deletes nothing. Abnormal termination
may require parent-owned cleanup; a timeout is not a cleanup or PASS receipt.

No original natural-language understanding, conversational context, orders,
Remove/SetNote/clearing execution, summary/dashboard, live quality, frontend
integration or full twelve-case GREEN is measured. R5/R6 evidence and runtime
task 4 remain unchanged. AI reviews actual final source plus parent receipts;
this fixture grants no peer source copy, provisioning or rerun authorization.
