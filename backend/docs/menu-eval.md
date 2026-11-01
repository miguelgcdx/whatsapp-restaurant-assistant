# Distinguish reply proposals from menu authority

The menu engine returns model prose as an **unverified proposal**. Only the
structured `Result.Menu.Open` field, when present, reports stored open status.
The two offline characterization cases deliberately supply misleading prose;
passing them does not mean that prose is safe or correct to send to customers.

## What the cases establish

`TestMenuTurnProposalBoundary` uses the public `Engine.Run` interface and existing
fabricated-key, in-memory transport helpers. It adds no model, network listener,
database fixture, production behavior, or semantic refusal policy.

| Case | Scripted prose | Deterministic assertions |
|---|---|---|
| Direct answer | `We are open.` | Exact reply; `Menu=nil`; one transport attempt and zero menu reads. |
| Closed snapshot | `We are open. Water costs USD 9.99.` | Exact contradictory reply; nonnil `Menu.Open=false`; two attempts and one read. |

The second case verifies the complete authoritative tool payload, including
closed status, unavailable items and exact minor-unit prices. Stored Water costs
zero, not the scripted USD 9.99. Both cases compare an independently copied source
snapshot after the run and assert exact effect counts. Extra attempts or reads
fail the test; the injected transport never forwards requests to a network.

These are expected-GREEN characterizations of the existing contract, not a bug
fix requiring a manufactured RED. Existing wire, error, budget, cancellation,
isolation and database-scope tests retain their separate coverage.

## Run when prerequisites are available

From `backend`, use the repository's approved offline Go environment with an
explicit absolute, owned worktree-local private `TMPDIR`, `TMP` and `TEMP` per
`AGENTS.md`, plus the prepared local module/build caches. No dependency download
or live account is needed or authorized by this guide.

```sh
go test -count=1 -timeout=20s -run '^TestMenuTurnProposalBoundary$' ./internal/menuengine
```

Even this filtered test requires the package's compile-time dependencies. Missing
cached modules are unavailable proof, not a passing or silently skipped test.
The command is an instruction, not a claim that it has run successfully.

## What remains unmeasured

Scripted transcripts cannot show whether a real model selects `get_menu` for a
customer phrase, respects prices or closure in prose, handles languages well, or
resists prompt injection. No semantic score or pass threshold is reported.
A real-model evaluation would need separately authorized data sharing, a dataset,
a rubric and repeated-sampling policy; it is not part of this increment.

See [menu-turn.md](menu-turn.md) for the engine contract and
[commandcode.md](commandcode.md) for the configured
`deepseek/deepseek-v4.1-flash` client's protocol and evidence limits. This increment
adds no runtime host, ordering capability, delivery or production approval.
