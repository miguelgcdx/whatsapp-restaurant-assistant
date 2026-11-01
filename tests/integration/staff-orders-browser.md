# One isolated, real-auth Orders journey

This **test-only** entrypoint signs in through the real UI, opens the actual Orders
list, follows the seeded order detail and reads its exact immutable confirmed
summary units. It does not exercise order actions, loading-flash diagnostics,
outages, replay, performance, real providers or the current working preview.
Static checks are not runtime acceptance; independent-verification holds remain.

## Parent execution contract

The final backend readset window and seed source audit are granted. Parent T3
source/static checks and inspection of the complete diff still precede execution;
there is no remaining backend approval gate. Use only the final 254-file manifest
including the seed, never the superseded 253-file core/separate-seed interface.
Missing inputs, a collision, drift, an unexpected status or a timeout stop this
one run; there is no install, fallback, rebuild, automatic retry or repair.

Use the installed Node directly, not a package manager:

```text
/home/miguelgcd/.local/share/mise/installs/node/26.11.1/bin/node --conditions=react-server tests/integration/staff-orders-browser.mts --help
/home/miguelgcd/.local/share/mise/installs/node/26.11.1/bin/node --conditions=react-server tests/integration/staff-orders-browser.mts --execute --manifest ABSOLUTE_PUBLIC_JSON --manifest-sha256 SHA256
```

The parent supplies the real absolute public manifest path and its SHA-256, never
credentials or a prior private evidence path. For that invocation use a fresh
same-user 0700 `/tmp` directory with absolute `TMPDIR`, `TMP`, `TEMP`, unchanged
`HOME` and `NODE_OPTIONS=''`. Invoke with a positive environment (`env -i`),
including only unchanged `HOME`, installed Node plus `/usr/bin:/bin` in `PATH`,
`LANG=C`, those three absolute temporary paths and empty `NODE_OPTIONS`; do not
inherit debug, proxy, auth or PG variables into the orchestrator itself.
The program creates its own distinct 0700 run root,
uses that root for all subsequent temporary work and retains it. No entrypoint
has been executed by the implementation writer, including help.

## Public manifest (strict JSON)

| Field | Required value |
| --- | --- |
| `frontend_root` | Approved absolute frontend source root |
| `backend_root` | Approved absolute backend source root; not inferred from old evidence |
| `module_cache` | Exactly `<backend_root>/.cache/modules`; explicit installed reuse, not copied |
| `ports` | Distinct explicit numeric `postgres`, `next`, `staff` ports, each 1024–65535 |
| `files` | Exhaustive frontend-only list of `{root: "frontend", path: relative public path, sha256: lowercase SHA-256, bytes: integer byte length}` |
| `backend_manifest` | `{path: absolute public full-manifest JSON, sha256: "3c24c98316caf3b91db72c94e05ca56e0a891cfd88721d3af0135d4f1c0c270c"}` |

The parent must include **all** transitive public build inputs, not merely the
minimum required entries. Frontend categories are `src` TS/TSX/CSS,
`scripts/local-auth` MTS, these test-owned MTS files, public image/font assets,
`package.json`, `pnpm-lock.yaml`, `tsconfig.json`, `next-env.d.ts`,
`next.config.ts` and `postcss.config.mjs` when used. Backend categories are
`go.mod`, `go.sum`, root Markdown, Go/SQL/Markdown under `cmd`, `internal`,
`docs`, `migrations`, including
**all package test files, test helpers and embedded SQL** needed by `go test -c`.
In particular, `cmd/local-staff-api/orders_connected_test.go` supplies
`connectedTransport` to the new seed test; include it and every other host
`*_test.go` and imported internal test helper. Compiling them is not permission to
run `connectedSetup`, existing tests or the old dashboard diagnostic.
The final owner receipt is
`/tmp/restaurant-browser-public-window-qA9rCB/public-full-manifest.json`, with the
pinned SHA-256 above. It must have schema `restaurant-public-source-manifest/v1`,
`count:254`, `total_bytes:2096701`, `status:FULL_PUBLIC_READSET_PINNED`, no mutable
exceptions, `source_root` matching the supplied backend root's parent, and 254
`{path,bytes,sha256}` entries. Their byte sum must match. The included seed must
be 19355 bytes with SHA-256
`f8d5f1af9ac55b8e6537adf74cfb8798096dd84f9d13586a831e112d85e44e3c`.
Repo-relative `backend/` prefixes are stripped only for placement under the
snapshot's `backend`. Original public metadata is retained unchanged in
`backend-full-manifest.json`; no obsolete core metadata gate is interpreted.
No glob expansion or source walk discovers extra inputs. An unsupported public
input stops preparation; never broaden to a whole directory or omit a file.

No private directory inventory is performed. Dot paths, credentials, `.env`, Git,
old `.next`, caches, PG data and browser state are excluded. Source symlinks and
symlinked ancestors are rejected. Only the explicitly installed frontend
`node_modules` directory is linked for reuse. Every manifest pin is validated
before and after copying, and every snapshot copy is checked; duplicate entries
and source drift fail closed. SHA-256 and byte length must both match every
original and copied entry. After the final copy check the runner emits exactly
`SNAPSHOT_VALIDATED` on stdout before building. Keep the runner's stdin pipe open:
the parent consumes that public event, releases the owner source window, then
writes exactly `SOURCE_WINDOW_RELEASED\n` to stdin within 30 seconds. No private-log
polling or prequeued acknowledgement: absent/malformed acknowledgement stops the
run before build. No original backend source is read again. Installed Next 16.4.2, Playwright 1.64.1 and Better
Auth 1.7.12 are checked before building. Runtime auth config is absent from the
positive build environment. No build/server runs in the original worktree.

## Sequencing and limits

| Stage | Single-attempt contract |
| --- | --- |
| Build | Installed Next production build, 300 seconds |
| Compile | Unmodified host `go build` plus seed `go test -c`, **one combined 120 seconds**, before JWT minting |
| Database | Owned `initdb`/`pg_ctl` and one fresh `restaurant_foundation_<run>` database on numeric loopback, no inherited PG defaults |
| Setup | Supported `scripts/local-auth/setup.mts`, `--conditions=react-server`, 120 seconds; existing Go and Better Auth migrations/provisioning |
| Listeners | Owned production Next and unmodified Go host; readiness at most 30 seconds each |
| Seed | Separate supported sign-in/session/getToken, then precompiled seed once; remaining 120-second seed deadline |
| Browser | One clean context, actual UI sign-in then GET/navigation only, 60 seconds including launch |

The Go environment uses new private `GOCACHE`/`GOPATH`, `GOENV=off`,
`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, readonly modules and no downloads.
Setup's supported worker still performs its existing offline `go run` migration
and membership commands inside the setup budget; it is not replaced or modified.

All runtime auth settings use the new DB/private setup state. **One** owned Next
origin derives `BETTER_AUTH_URL`, browser origin, `STAFF_JWT_ISSUER` and
`STAFF_JWKS_URL=<origin>/api/auth/jwks`; there are no independent issuer/JWKS
options. `STAFF_API_URL` targets the owned host; `LOCAL_RESTAURANT_ID` comes from
supported setup state. The existing host's `STAFF_CATALOG_CURSOR_KEY` is a fresh
run-generated random key, never printed or inherited. The supported server auth factory reads that state only
inside the run-owned program. It signs in with the generated fixture, calls real
`getSession`/`getToken` and signs out; it does not read auth tables, hand-sign a JWT,
serialize a browser session or add an endpoint. The separate browser starts empty
and must sign in again. Tokens have the existing factory's 60-second lifetime.

## Frozen seed interface

`STAFF_ORDERS_BROWSER_SEED_INPUT` points to a newly created strict JSON file,
0600, directly inside the exact new owned 0700 run root. Its **seven required
strings** are `database_url`, `restaurant_id`, `subject`, `issuer`, `jwks_url`,
`token`, `evidence_dir`. `evidence_dir` is that same root.
`STAFF_ORDERS_BROWSER_SEED_OUTPUT` names a nonexistent file directly in that root.
The backend creates it exclusively, 0600, with only `order_id`, `summary_id`,
`initial_version`, `unit_count`. The frontend expects UUID IDs, a positive decimal
string version and an integer unit count >=2; units pagination remains read-only.

The seed child gets the positive tooling environment, **not** runtime auth/DSN
variables. Its only `PG*` names are `PGPASSFILE=/dev/null` and
`PGSERVICEFILE=/dev/null`. The explicit DSN is supplied only in its private input.
It runs exactly once as the precompiled binary with
`-test.run '^TestOrdersBrowserSeed$' -test.count=1 -test.timeout=120s`.
The backend helper owns its pools/fake assessor only, verifies the real JWT/current
active membership, seeds through public domain/services and genuinely ACKs the
summary. It must never own PG, migrate, fabricate stored orders or reactivate a
membership. The frontend owns no backend source edits.

## Evidence and teardown

Only fixed stage/status receipts and the new evidence directory path go to stdout;
catch blocks never print errors, child output, DSNs, cookies, tokens or passwords.
Child stdout/stderr is written directly to protected 0600 files, never tailed.
`public-manifest.json` freezes public inputs; `outcome.json` contains sanitized
stage outcomes. Generated state, seed input/output, PG data and raw logs remain
private. Human inspection of retained raw evidence is separate from agent tools.
No screenshots, traces, video, storageState or browser token/storage inspection.

Browser close precedes stopping owned Next, host, auth pool and PG. Termination
uses only owned child handles/process groups and owned PG data; there is no global
kill, port scan or blanket deletion. Successful and failed roots are retained.
The parent reports actual execution/skips and handles retained private evidence;
this source does not establish runtime PASS or independent CLEAR.
