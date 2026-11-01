# Socket test fixtures

Use `testsocket.Dir(t)` for owned, private fixture storage under the absolute
project-local `TMPDIR`. It returns an absolute directory with compact naming,
0700 permissions, and test cleanup; it does not relocate physical storage.

Wrap only the socket pathname with `testsocket.RelativeSocketPath(t, absolutePath)`.
The helper uses `filepath.Rel` from the current working directory without changing
it. It fails the test if the result exceeds Linux's 107-byte Unix socket pathname
limit (excluding the terminating NUL). The adjacent `.lock` is a regular file:
it does not require reserving four bytes from the socket limit.

Use that same relative pathname for bind, dial, stat, lock, replacement, and
shutdown checks. Keep the working directory unchanged while the fixture is in
use. Worker/API subprocesses and the umask guard child inherit their parent's
CWD, so they resolve the same socket address. The integration build command's
separate `../..` working directory is not the runtime child CWD.

Keep `Dir` results, `HOME`, `TMPDIR`/`TMP`/`TEMP`, binary output paths, and symlink
targets absolute. Relative socket addresses are not aliases, symlinks, abstract
sockets, or permission to escape project-local disk storage. If a relative
address is still too long, fail closed rather than choosing another location.

From `backend`, using the feature's isolated source copy and private offline
environment, the focused check is:

```sh
go test -count=1 -timeout=20s -v -run '^TestRelativeSocketPath$' ./internal/testsocket
```

The separately authorized broad checks are sequential, not extra fixture probes:

```sh
go test -count=1 -timeout=120s ./...
go vet ./...
go test -race -count=1 -timeout=180s ./...
```
