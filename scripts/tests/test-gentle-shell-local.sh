#!/bin/bash
set -eu

# The caller supplies a private retained evidence directory; never use /tmp.
: "${TMPDIR:?Set TMPDIR to a private project-local evidence directory}"
[[ -d $TMPDIR && ! -L $TMPDIR && -O $TMPDIR ]] || exit 1
SOURCE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)/gentle-shell-local"
TEST_ROOT="$(mktemp -d "$TMPDIR/tests-XXXXXX")"
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_CEILING_DIRECTORIES="$TMPDIR"
mkdir "$TEST_ROOT/bin" "$TEST_ROOT/caller"
for tool in git dirname stat mktemp; do
  ln -s "$(type -P "$tool")" "$TEST_ROOT/bin/$tool"
done
cat > "$TEST_ROOT/bin/gentle-shell" <<'MOCK'
#!/bin/bash
printf '%s\n' "$TMPDIR" > "$CAPTURE/temp"
printf '%s\n' "$PWD" > "$CAPTURE/cwd"
printf '%s\0' "$@" > "$CAPTURE/args"
: > "$TMPDIR/mock-created"
printf 'mock stdout\n'
printf 'mock stderr\n' >&2
exit 37
MOCK
chmod 700 "$TEST_ROOT/bin/gentle-shell"
fixture() {
  CASE="$TEST_ROOT/$1"
  mkdir -p "$CASE/repo/scripts" "$CASE/repo/backend/.cache/tmp" "$CASE/capture"
  chmod 700 "$CASE/repo/backend/.cache/tmp"
  git -c init.defaultBranch=main init -q --template= "$CASE/repo"
  cp "$SOURCE" "$CASE/repo/scripts/gentle-shell-local"
  chmod 700 "$CASE/repo/scripts/gentle-shell-local"
  export CAPTURE="$CASE/capture"
}
invoke() {
  set +e
  (cd "$TEST_ROOT/caller" && PATH="$TEST_ROOT/bin" "$CASE/repo/scripts/gentle-shell-local" "$@") > "$CASE/stdout" 2> "$CASE/stderr"
  STATUS=$?
  set -e
}
fail() { printf 'FAIL: %s (evidence: %s)\n' "$1" "$TEST_ROOT" >&2; exit 1; }
reject() {
  invoke
  [[ $STATUS == 1 ]] || fail "rejection status: $STATUS"
  [[ ! -s $CASE/stdout && ! -e $CAPTURE/temp ]] || fail 'rejection invoked launcher'
  printf 'gentle-shell-local: unsafe temporary-storage path\n' > "$CASE/expected-error"
  cmp "$CASE/expected-error" "$CASE/stderr" || fail 'rejection stderr'
}

# An inherited shell function must never replace the PATH executable.
gentle-shell() { printf 'unexpected shell function\n' >&2; return 99; }
export -f gentle-shell
fixture 'forwarding with spaces'
CALLER_TMP=$TMPDIR
invoke 'two words' '' '--literal=*' $'line\nbreak'
[[ $STATUS == 37 ]] || fail "forwarded status: $STATUS"
printf 'mock stdout\n' > "$CASE/expected-stdout"
printf 'mock stderr\n' > "$CASE/expected-stderr"
printf '%s\0' 'two words' '' '--literal=*' $'line\nbreak' > "$CASE/expected-args"
printf '%s\n' "$TEST_ROOT/caller" > "$CASE/expected-cwd"
cmp "$CASE/expected-stdout" "$CASE/stdout" || fail stdout
cmp "$CASE/expected-stderr" "$CASE/stderr" || fail stderr
cmp "$CASE/expected-args" "$CAPTURE/args" || fail args
cmp "$CASE/expected-cwd" "$CAPTURE/cwd" || fail cwd
FIRST="$(< "$CAPTURE/temp")"
[[ $FIRST == "$CASE/repo/backend/.cache/tmp/"* && $FIRST == /* ]] || fail 'project-local absolute TMPDIR'
[[ -d $FIRST && ! -L $FIRST && -O $FIRST && -f $FIRST/mock-created ]] || fail 'owned temp routing'
[[ $(stat -c %a "$FIRST") == 700 ]] || fail 'private launch permissions'
[[ $TMPDIR == "$CALLER_TMP" ]] || fail 'caller environment changed'
invoke
[[ $STATUS == 37 ]] || fail 'second launch status'
SECOND="$(< "$CAPTURE/temp")"
[[ $FIRST != "$SECOND" && -f $FIRST/mock-created && -f $SECOND/mock-created ]] || fail 'unique dirs / retained evidence'
stat -c '%a %U %n' "$FIRST" "$SECOND"
printf 'PASS: forwarding, cwd, environment, private unique project temp, retained evidence\n'

for component in backend cache tmp; do
  fixture "symlink-$component"
  mkdir -p "$CASE/target/.cache/tmp" "$CASE/target/tmp"
  case $component in
    backend) mv "$CASE/repo/backend" "$CASE/original"; ln -s "$CASE/target" "$CASE/repo/backend" ;;
    cache) mv "$CASE/repo/backend/.cache" "$CASE/original"; ln -s "$CASE/target" "$CASE/repo/backend/.cache" ;;
    tmp) mv "$CASE/repo/backend/.cache/tmp" "$CASE/original"; ln -s "$CASE/target" "$CASE/repo/backend/.cache/tmp" ;;
  esac
  reject
  [[ -z $(find "$CASE/target" -type f -print) ]] || fail 'symlink target modified'
  printf 'PASS: reject symlink %s without fallback\n' "$component"
done
fixture missing-base
mv "$CASE/repo/backend/.cache/tmp" "$CASE/original"
reject
[[ ! -e $CASE/repo/backend/.cache/tmp ]] || fail 'missing base created'
printf 'PASS: reject missing base without fallback\n'
fixture unsafe-permissions
chmod 777 "$CASE/repo/backend/.cache/tmp"
reject
printf 'PASS: reject unsafe base permissions\n'
fixture missing-root
mv "$CASE/repo/.git" "$CASE/git-retained"
invoke
[[ $STATUS == 1 && ! -s $CASE/stdout && ! -e $CAPTURE/temp ]] || fail 'missing repository status/effects'
printf 'gentle-shell-local: cannot resolve repository root\n' > "$CASE/expected-error"
cmp "$CASE/expected-error" "$CASE/stderr" || fail 'missing repository stderr'
[[ -z $(find "$CASE/repo/backend/.cache/tmp" -mindepth 1 -print) ]] || fail 'missing root created temp data'
printf 'PASS: missing repository fails without fallback\n'
fixture missing-executable
# Mask only the owned mock, never the real installed executable.
mv "$TEST_ROOT/bin/gentle-shell" "$TEST_ROOT/mock-disabled"
invoke
[[ $STATUS == 1 && ! -s $CASE/stdout && ! -e $CAPTURE/temp ]] || fail 'missing executable status/effects'
printf 'gentle-shell-local: gentle-shell executable not found in PATH\n' > "$CASE/expected-error"
cmp "$CASE/expected-error" "$CASE/stderr" || fail 'missing executable stderr'
printf 'PASS: missing executable does not use real launcher or fallback\n'
printf 'Evidence: %s\n' "$TEST_ROOT"
