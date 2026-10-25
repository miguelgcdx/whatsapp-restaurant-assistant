# Worker socket guard fixtures

`TestSocketDirectoryAndLockGuards` exercises the public `worker.Run` boundary
with the existing in-memory store and sender; this test needs no database.
Production socket guards are unchanged.

## Establish permissions, then assert rejection

`os.Mkdir(dir, 0755)` requests permissions; the process umask can remove them.
Under umask `077`, that request creates a private `0700` directory, which the
worker correctly accepts. A background-context rejection test then waits for a
worker that is legitimately running. This is a fixture defect, not a permission
bypass.

The fixture now explicitly chmods its owned public directory to `0755` and checks
that mode before calling Run. Both expected-rejection calls have one-second
contexts and require the exact intended error: public-directory privacy denial
or inability to open the symlink lock. Nil, unrelated errors, and expired
contexts cannot count as successful rejection. Public rejection must leave no
socket or lock; symlink-lock rejection must leave no socket and preserve both
the symlink and its target contents.

## Controlled subprocess regression

`TestSocketDirectoryAndLockGuardsUnderUmask` runs the existing named test in the
current test binary, separately under child-shell umasks `022` and `077`. It
never changes the parent process's global umask or recursively selects itself.
Each child has a three-second context, an explicit PATH, and separate compact
owned HOME/TMPDIR directories; no ambient DSN or credentials are inherited.
Normal owned-subtree cleanup runs after the bounded child exits. Compact
`testsocket.Dir` basenames avoid long Go test names in Unix socket paths; the
runner must still supply a short TMPDIR.

A child deadline, nonzero exit, or missing named-test PASS output fails the
regression. Before the fixture correction, `077` exceeded the child deadline;
that bounded runtime failure exposed the original hang without a long suite
timeout. The one-second worker bounds are failure containment, not evidence
that cancellation is an acceptable security denial.

## Targeted checks

From `backend`, with the approved isolated environment, short owned TMPDIR,
offline module caches, and local toolchain:

```sh
go test -count=1 -timeout=20s -run '^TestSocketDirectoryAndLockGuardsUnderUmask$' ./internal/worker
go test -count=1 -timeout=20s ./internal/worker
go test -race -count=1 -timeout=20s ./internal/worker
go vet ./internal/worker
```

Both package test commands include both controlled masks; separate mask runs
are unnecessary. Other worker tests retain their disposable PostgreSQL fixture
requirements and failures, rather than becoming generic short-mode skips.
These checks cover the affected worker package, not the full backend or cart
suite. Full-backend race verification remains a separate parent-owned check.

## Observed local verification

The controlled regression first failed under `077` at its three-second child
bound; `022` did not fail. After the test-only correction, the worker package's
normal tests, race tests and vet passed; the parent normal spot-check also passed.
Production `worker.go`, `socket.go`, `sender.go` and module manifests were unchanged.

The final full-backend race command, `go test -race -count=1 -timeout=180s ./...`,
passed under process umask `077` in 94.558 seconds wall time. Worker package time
was 5.023 seconds. All 104 selected maintained Go/SQL/module inputs matched before
and after the run; stderr was empty. The earlier complete normal-suite and vet
passes are historical results; the changed worker package was rechecked, not a
newly rerun full normal suite. Earlier launch and timeout failures remain recorded
in `../../odd/tasks/local-worker-guard-fixture.md`.
