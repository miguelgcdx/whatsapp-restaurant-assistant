# Isolated PostgreSQL test fixtures

`internal/testpg.Start(testing.TB)` creates a disposable local cluster and returns
its TCP DSN. It never accepts an existing database URL. This is test infrastructure,
not an application database launcher or evidence of production readiness.

## Prerequisites and ownership

Install the project's required Go toolchain (currently Go 1.27.3) and make local
`initdb`, `pg_ctl`, and `createdb` binaries available on `PATH`. Run as a user that
can initialize PostgreSQL, not via sudo. Missing binaries or failed commands
**fail** tests; there is no short-mode skip, fallback database, or silent retry.

Each call owns a temporary directory, data directory, and `postgres.log`.
Initialization uses the synthetic `localtest` role and trust authentication only
for this throwaway cluster. The server listens on `127.0.0.1`, disables Unix
sockets, and creates only `restaurant_foundation_test`. Never point these tests at
valuable data, hosted databases, or real credentials.

The helper obtains a port by binding `127.0.0.1:0`, then closes that listener before
starting PostgreSQL. This is **not an atomic port reservation through server
startup**. Another process could acquire the released port. No observed evidence
has established that this caused the previously reported startup failure; the
port-selection behavior is unchanged. There are no retries, global locks, shared
servers, or serial-suite workarounds.

## Startup diagnostics and cleanup

On failed `pg_ctl start`, the helper reports the final **8 KiB** of its own
`postgres.log` before test cleanup removes the temporary directory. Larger logs
are explicitly marked truncated. An unavailable log is reported separately;
`pg_ctl`'s original exit error and output still fail the test. Diagnostics do not
search other directories, read configured database URLs, or turn failed startup
into a usable DSN. The bound applies to server-log content, not Go test output
formatting or the existing command-output capture.

Cleanup is registered before startup so that a failed start with a remaining
`postmaster.pid` still attempts immediate shutdown. If startup failed and that PID
file is absent, cleanup does not issue a pointless `stop` command. Other PID-file
inspection errors conservatively leave the stop attempt enabled. After successful
startup, shutdown is always attempted, including when `createdb` fails. Genuine
shutdown failures remain test errors. These rules avoid obscuring startup failure
with a second missing-PID error; they do not recover or retry startup.

Callers should register connection-pool cleanup after `Start` returns, so testing's
LIFO cleanup closes clients before stopping the owned server.

## Regression checks

From `backend`, using the prepared project-local Go caches and offline module
settings described by the project's verification environment:

```sh
go test -count=1 -timeout=20s ./internal/testpg
go test -race -count=1 -timeout=20s ./internal/testpg
go vet ./internal/testpg
go test -count=1 -timeout=20s -run '^TestExpiredLeasesCannotCompleteOrBlindlyResend$' ./internal/postgres
```

The testpg tests execute the public `Start` boundary in child test processes with
explicit clean environments and synthetic binaries. They assert failure exits,
command attempts, returned DSN/arguments, bounded diagnostics, legitimate cleanup
errors, and removal of disposable data. They cover missing prerequisites, failed
initialization, failed/partial startup, missing/large logs, failed database
creation, normal success, and failed shutdown. A supplemental public probe covers
an uninspectable PID path and a startup-log read error. These synthetic probes are
not real PostgreSQL proof; the separate expired-lease regression uses actual
installed PostgreSQL binaries.

For full-suite runs, use a short, private `TMPDIR`: API/worker/integration Unix
sockets have operating-system path-length limits. Deep project-local temporary
paths can cause bind errors even though this TCP-only PostgreSQL fixture works.
Correct that runner configuration rather than changing production socket behavior.

## Evidence and limits of this correction

The deterministic probes reproduced lost server-log evidence and unnecessary
missing-PID cleanup errors. They did **not** reproduce or identify the original
real PostgreSQL startup cause. A subsequent isolated real expired-lease regression
passed, but does not explain that earlier failure or prove concurrent-suite
reliability. Retained local run evidence is under
`backend/.cache/diagnostics/pg-reliability-5Jh3SF`; earlier successful startup
capture remains separately under `pg-startup-KQK5p7`.

Consumers are tests in `internal/{api,cart,catalog,integration,postgres,staff,
staffavailability,staffcatalog,worker}` and
`cmd/{local-migrate,local-staff-api,local-staff-membership}`. No production CLI
imports this helper. The affected seam is shared test startup/cleanup, not domain,
HTTP, worker delivery, or production command behavior. Final full-suite checks and
independent lifecycle assessment belong to the parent verification step.

## Final observed verification

Independent fixture lifecycle verification passed, including the supplemental
public uninspectable-PID/log-read-error probe adopted into the regression suite.
The final full-backend race run passed under `077`, with the PostgreSQL package
at 15.245 seconds and fixture tests at 2.422 seconds. All 104 selected maintained
Go/SQL/module inputs matched before/after that run. Prior full normal/vet passes
and later refreshed worker checks retain their separate attribution; no new full
normal run is claimed. The original historical startup cause remains unknown;
the demonstrated lost-diagnostic and noisy-cleanup defects were corrected, not
relabelled as a proven fix for that unavailable original server failure.
