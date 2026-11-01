# Offline AI pickup scenario fixture

> **Expanded fixture: compiled and parent-checked; independent acceptance unresolved.**
> T5 was delivered source-only. The human subsequently authorized ordinary parent
> compilation/tests/race/vet (S9). All 12 cases passed against the pinned isolated
> snapshot. The terminal unavailable independent-verifier outcome is unchanged;
> parent checks are not independent acceptance.

`internal/testpickupeval.Run(ctx, testing.TB, Scenario) (Observation, error)` is a
reusable testing-owned LOCAL journey. It is not a production API, CLI, queue,
mounted JWT endpoint, arbitrary callback framework or live provider integration.
Scripted model replies exercise integration, **not natural-language quality**.

## Current expanded-revision parent checks

| Check | Native exit | Observed result |
|---|---:|---|
| Package tests | 0 | All 12 leaf cases passed, 14.873s |
| Package tests with `-race` | 0 | All 12 leaf cases passed, 15.609s |
| `go vet` | 0 | Quiet success |

No cases were skipped; stdout/stderr/exit/command records were read, and stderr
was empty for all three commands. These are ordinary **parent** checks, not a
replacement independent-verifier run, full backend suite, language-quality
assessment or clearance of any core bridge/staff/domain proof gate.

The current working-tree build-file hashes matched the isolated snapshot before
execution. Commands ran from the snapshot's `source/backend` directory:

```text
/usr/bin/go test -v -mod=readonly -count=1 -timeout=180s ./internal/testpickupeval
/usr/bin/go test -race -v -mod=readonly -count=1 -timeout=180s ./internal/testpickupeval
/usr/bin/go vet -mod=readonly ./internal/testpickupeval
```

All subprocesses used explicit `TMPDIR`, `TMP` and `TEMP` set to private
`/tmp/restaurant-ai-pickup-expanded-check-VpQnY4`, an owned build cache beneath it,
`GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`, and the existing
public module cache under the original workspace's `backend/.cache/modules`.
Retained raw artifacts are `expanded-{normal,race,vet}.{command,exit,stdout,stderr}`
in that run directory, while it exists. No code correction was needed.

Source/test comments saying source-only or unexecuted describe the T5 authoring
phase; this section records the later S9 execution of those same pinned sources.

## Evidence boundary — earlier historical revision

The smaller pre-T5 fixture had parent normal **6.220s**, race **9.030s** and quiet
vet checks, all exits 0, six leaf cases passed without skips and empty stderr.
Those results apply **only to that historical revision**, not the expanded code,
refactored setup, HTTP barrier, native edits or new tests now present. Earlier
explicit stub RED and the corrected staff-intent assertion retain their historical
attribution; they are not RED/GREEN evidence for T5.

Independent NEW-fixture verifier `mvwwymdw-3-9v81` ended **FINAL UNAVAILABLE** before
Go/PG execution: required scratch source copies conflicted with the no-copy task
restriction. Its static reads/hash checks did not establish runtime capture or
lifecycle safety. The older core bridge verifier and staff/domain/R7 proof stops
also remain unchanged. No retry, replacement, closing probe or new verifier was
launched. Later S9 authorization permitted ordinary expanded-revision parent
checks only. Neither the current nor prior parent checks replace a terminal
independent proof.

## Authorized isolation snapshot — not execution proof

After the source-only handoff, the human authorized a bounded source snapshot.
`/tmp/restaurant-ai-pickup-snapshot-zjoe4eja/source` contains the then-current
fixture dependency closure: 18 packages, 80 files, 515,114 bytes. The adjacent
`manifest.json` records per-file SHA-256 values. Source/copy hashes matched and
source hashes were unchanged when copying finished. Temporary paths may later
expire; this is not a committed artifact or persistent fixture resource.

The snapshot excludes credentials, `.env`, caches, logs, unrelated source and
dependency tests. It includes fixture tests, required production Go source,
embedded SQL, module files and fixture/spec documentation at the snapshot cutoff.
No source was imported back into the working tree. No runtime command ran during
snapshot preparation. Later ordinary parent checks used this snapshot as recorded
above. No independent verifier ran. The copy prerequisite is resolved, but the terminal unavailable
verification outcome remains: the current rules expose no supported restart or
replacement route. Snapshot permission alone does not grant one.

## API and supported scenarios

Import `restaurant-assistant/backend/internal/testpickupeval` from tests inside the
backend module. The default fixture remains two fictional USD 6.50 sandwiches,
pickup/pay_at_pickup, with exact first-unit note `"  no salt 🥪\n"`. No endpoint,
DSN, credentials, scope, policy version or assessor callback is accepted as input.

```go
inbound := "yes"
scenario := testpickupeval.Scenario{
    Inbound: &inbound,
    PostSummaryQuestion: "Would you like utensils?",
    Model: testpickupeval.ModelResponse{
        ToolArguments: `{"decision":"not_complete"}`,
    },
}
// Illustrative fixture use; any execution must respect the caller's scope.
// observed, err := testpickupeval.Run(t.Context(), t, scenario)
_ = scenario
```

### G1: one anchored restaurant-only question

`PostSummaryQuestion` finishes the **existing summary-request source** with one
real `menugate.RunAndQueue` reply, after the authoritative summary was received
and ACKed. That reply then traverses actual LOCAL receiver HTTP before
`RecordLocalReply`. The resulting history can therefore begin with a restaurant
utensils question without inventing a preceding customer turn.

This is one source-bound reply, **not** an independently originated restaurant
message API or permission for multiple replies per source. `History []Exchange`
still adds ordered customer/restaurant pairs after that optional question. All
turns are stored and projected by the domain; no semantic filtering, truncation
or nil-to-empty repair occurs. Missing presentation cannot combine with question,
history or edit options. At most 62 planned intervening turns are accepted.

### G2: actual resolved native edits, pending valid work, new summary

```go
scenario.Edits = []testpickupeval.Edit{
    {Kind: testpickupeval.AddSandwich}, // exactly one additional sandwich
    {Kind: testpickupeval.NoteFirstUnit, Note: "no onions"},
}
scenario.RefreshSummary = true
```

Each edit uses a fresh admitted menu source and its actual opaque `MenuClaim`,
`cart.EditCall`, `AdmitNativeEdit`, `RecoverNativeEdit` and `ExecuteNativeEdit`.
The edit source is finished through the existing menu gate, not fabricated or
retired with SQL. A menu claim is **not** passed to legacy `Store.Execute`.
Public readback establishes the stored cart after each real business operation.

- At most two edits are supported. `NoteFirstUnit` selects the first actual stored
  unit, not an ambiguous target. Its note is validated by public `cart.SetNote`;
  invalid note/domain setup fails the owning test, never becomes fake completion
  evidence. `AddSandwich` does not accept a note or arbitrary item/count dialect.
- `Pending: true` admits a **valid** native action but does not acquire/execute its
  business lease. Pending work must be last and cannot combine with refresh.
  Its observation has an actual admission operation ID and no execution result.
  This is unresolved **work**, not persisted ambiguous/unresolved **target** state.
- Without refresh, actual version change or pending native work makes completion
  reject the old summary with `cart.ErrPickupSummaryRequired`, before model HTTP.
  The completion attempt baseline is captured **after** these legitimate setup
  effects; refusal must not undo them or introduce additional order/cart effects.
- `RefreshSummary` requires resolved edits. It registers a fresh prepare source,
  prepares the current stored cart, reads the actual queued summary, sends exact
  bytes through the receiver and records its validated ACK. `Summary` and
  `Presentation` then refer to this current anchor; `OriginalSummary` retains the
  original one. A fresh completion uses the new snapshot and its complete scoped
  history, not stale summary strings or caller-built evidence.

### G3: sequential distinct source and controlled stale control

`SecondInbound` runs only after a successful primary completion and any requested
same-source replay/staff processing. The first actual source is finished normally
through the menu gate before admitting/claiming the distinct second source. It
uses the same scripted HTTP response, its own actual model capture and public
before/after effects. `Second.RetiredFirstProcessing` reads the actual first job
completed with its queued reply before second intake; that unpresented reply is
not invented receiver history. Existing domain conflict enforcement must leave at most one
order. This is **not** same-source replay renamed, and it is **not** simultaneous
valid distinct-claim concurrency proof: `ClaimMenu` serializes earliest active work,
and newer inbound allocation can fence an earlier decision window.

`Interleave: TakeStaffControl` is the sole supported controlled interleaving:

1. The actual `AssessAndSubmit` runs in one fixture-owned goroutine.
2. The actual assessor's loopback HTTP handler captures its decoded request and
   signals a barrier **after** trusted context acquisition, before responding.
3. The caller goroutine reads public effects/control, calls public
   `conversation.SetMode(..., ModeStaff, ...)` with privately retained fixture
   auth, then reads the after-mutation baseline.
4. It releases the scripted model response. The original execution retains its
   original control version; current domain fences must return `cart.ErrStale`.
5. Attempt readback must equal the legitimate after-mutation business baseline;
   control change itself is asserted separately, never erased or faked.

No arbitrary callback, injected Go decision, production hook or raw SQL is exposed.
This enum cannot combine with edits, refresh, second inbound, staff processing,
replay, missing presentation or a provider-failure HTTP status. Use a scripted
`complete` result to exercise the post-assessment fence. Controlled cart/claim/scope
races are **not** implemented by this option; stale old-cart setup is G2, not a race.

## Observations and errors

- `Context`/`ContextError` are the actual public preflight read, including stale/
  pending refusal. The actual bridge is still called independently; Run returns
  the primary bridge error with `errors.Is`/`errors.As` identity preserved. Silence
  (`Inbound == nil`) admits no current event and performs no completion model or
  submission call. A nonnil blank inbound is invalid, not silence.
- `ModelRequests` comes from actual decoded primary/replay model HTTP bodies.
  `Second.ModelRequests` belongs to the distinct second attempt. Only model,
  messages and tools are exposed; authorization headers are stripped on loopback
  redirection and never observed. `Model.ToolArguments` scripts protocol bytes,
  not business authority. HTTPStatus accepts zero/200 or 400–599; 503 retains the
  real commandcode public error identity. Strings are UTF-8 and at most 64 KiB.
- `Deliveries` pairs actual receiver observations with decoded HTTP 200 ACKs.
  Correlation, nonempty receiver reference and exact decoded SHA-256 bytes are
  validated before either presentation-recording call. Enqueue/send acceptance
  alone is not a receipt. Refreshed summaries remain separately visible.
- `Edits` contains actual source/admission IDs, public execution results/errors
  and before/after cart reads. No opaque business lease/ref escapes. A pending
  edit has zero execution result because execution was deliberately not invoked.
- `Before`, `After`, `BeforeReplay`, `Final` are fresh public cart/order/intent/job
  reads around primary assessment, optional staff actions and same-source replay.
  Staff accept/start-preparing legitimately append durable status intents; they
  stay visible. Replay must preserve the entire fresh BeforeReplay snapshot.
  `Final` precedes optional distinct-source work; `Second.Before/After` separately
  expose that attempt. Its primary outcome is `Second.Error`, not Run's error.
  These public effects do not expose every internal registration row: a distinct
  second complete proposal may register its immutable descriptor before Submit
  returns conflict. No claim of zero internal registration writes is made.
- `Interleaving` exposes actual before/after mutation business readbacks and public
  control states. Credentials, raw auth tokens, DSNs and SQL rows never escape.
  Setup/readback errors fail the owning test; missing authority is not repaired.

## Ownership, bounds and cleanup — parent checks, not independent acceptance

The caller owns the testing lifecycle and must not concurrently mutate input.
Resources register with `t.Cleanup`. Existing public testpg migration/catalog/cart/
job/staff/conversation APIs create isolated disposable state; no existing DSN or
application-effect SQL is used. Native admission/registration, server digest,
fixed `pickup-completion-v1`, immutable snapshot, claims/control and Submit's
atomic persistence remain core-owned. No queue, migration or production adapter
was added. Staff reads/process remain **domain APIs**, not mounted JWT HTTP proof.

The two-minute scenario context bounds domain/HTTP work; existing testpg has its
own bounded startup/shutdown. Assessor/client deadlines remain 20/10 seconds and
fixture HTTP I/O timeouts five seconds. Model capture is bounded at 1 MiB, ACK
read at 4 KiB, and request snapshots are synchronized. The takeover branch owns
one result-buffered goroutine, context-bound handler barrier and idempotent release.
On normal return, cancellation or owning-test Fatal, it releases the handler,
cancels and waits up to 16 seconds for concrete client/domain work to finish before
server cleanup; failure to join is reported, not silently treated as success.
There are no sleeps, background retries or caller callbacks. The parent race run observed no races in the covered scenarios; this is not
exhaustive lifecycle coverage or the missing independent acceptance.

Nil/expired contexts and invalid supported option shapes fail before setup. A
one-minute synthetic JWT is retained privately for the bounded takeover scenario;
auth expiration is a setup failure, not bypassed by control mutation. Cancellation
after a primary bridge/replay may return partial observations instead of reading
through a canceled context. Cancellation during setup/readback fails the test.

## Parent-executed contracts and exact gaps

The new public-interface tests included in both passing parent runs:

- `TestRunAnchoredUtensilsQuestionDoesNotInventCustomerTurn`
- `TestRunRealAddAndNoteInvalidateOldSummaryWithoutAttemptEffects`
- `TestRunRefreshedSummarySubmitsCurrentNativeEditSnapshot`
- `TestRunValidPendingNativeWorkRefusesWithoutAttemptEffects`
- `TestRunDistinctSecondCompletionDoesNotCreateAnotherOrder`
- `TestRunActualModelHTTPBarrierFencesPublicControlTakeover`

The original cases were also rerun and passed on this expansion. T5 was authored
under a source-only restriction, so no test-first RED is claimed for its new
contracts. S9 later authorized the ordinary parent checks above. Go 1.27.3 and
local PostgreSQL remain prerequisites. These results grant no authority to retry
or substitute an unavailable independent verifier or execute stopped core proofs.

S3/S4/S7 covered scenarios now have ordinary parent execution evidence for the
anchored utensils question, actual add/note, pending valid work, refreshed summary,
sequential distinct completion, and one controlled public control takeover.
**Independent acceptance and full original-case coverage remain unresolved.** Independently originated arbitrary restaurant messages/multiple replies
per source, persisted ambiguous target state, simultaneous authorized distinct
claims, stale claim/wrong-scope/cart-race harnesses, uncertain delivery and broad
cancellation scenarios remain unsupported or unproved. Do not equate pending valid
work with N6's ambiguous target, or infer full S4 acceptance from scenario source.
S5 preserves all owner surfaces and terminal proof boundaries. S6 explicitly
requires separate language-quality evaluation; arbitrary scripted output capture
alone is not such proof. S1/S2 remain the disjoint AI-facing support ownership.
