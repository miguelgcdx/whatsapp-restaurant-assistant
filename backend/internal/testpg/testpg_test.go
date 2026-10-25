package testpg_test

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/testpg"
)

// Probe in another test process: Fatal and Cleanup must have their real testing.T
// semantics, including failure exits and removal of the fixture's temporary tree.
func TestStartProbe(t *testing.T) {
	if os.Getenv("TESTPG_PROBE") != "1" {
		return
	}
	fmt.Println("RETURNED_DSN=" + testpg.Start(t))
}

func TestStartContracts(t *testing.T) {
	cases := []struct {
		name       string
		wantExit   bool
		wantEvents string
		wantOutput []string
		noOutput   []string
	}{
		{"success", false, "initdb\nstart\ncreatedb\nstop\n", nil, nil},
		{"missing", true, "", []string{"prerequisite createdb"}, nil},
		{"init-fail", true, "initdb\n", []string{"initdb isolated fixture", "synthetic init error"}, nil},
		{"start-fail", true, "initdb\nstart\n", []string{"pg_ctl isolated fixture", "synthetic start error", "synthetic server fatal"}, []string{"stop owned PostgreSQL", "RETURNED_DSN="}},
		{"partial-start", true, "initdb\nstart\nstop\n", []string{"synthetic start error", "synthetic server fatal"}, []string{"RETURNED_DSN="}},
		{"large-log", true, "initdb\nstart\n", []string{"synthetic log tail", "truncated"}, []string{"LOG_HEAD_MUST_NOT_APPEAR", "RETURNED_DSN="}},
		{"no-log", true, "initdb\nstart\n", []string{"synthetic start error", "read owned PostgreSQL startup log"}, []string{"RETURNED_DSN="}},
		{"createdb-fail", true, "initdb\nstart\ncreatedb\nstop\n", []string{"createdb isolated fixture", "synthetic createdb error"}, []string{"RETURNED_DSN="}},
		{"stop-fail", true, "initdb\nstart\ncreatedb\nstop\n", []string{"stop owned PostgreSQL cluster", "synthetic stop error"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			tmp := filepath.Join(root, "tmp")
			home := filepath.Join(root, "home")
			for _, dir := range []string{bin, tmp, home} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for name, script := range map[string]string{"initdb": fakeInit, "pg_ctl": fakeControl, "createdb": fakeCreate} {
				if name == "createdb" && tc.name == "missing" {
					continue
				}
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestStartProbe$", "-test.v")
			cmd.Env = []string{
				"PATH=" + bin, "HOME=" + home, "TMPDIR=" + tmp,
				"TESTPG_PROBE=1", "TESTPG_CASE=" + tc.name, "TESTPG_AUDIT=" + root,
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantExit {
				t.Fatalf("exit = %v, want failure %v:\n%s", err, tc.wantExit, out)
			}
			if err != nil {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("expected test failure exit 1, got %v:\n%s", err, out)
				}
			}
			events := readOptional(t, filepath.Join(root, "events"))
			if events != tc.wantEvents {
				t.Errorf("command attempts = %q, want %q; output:\n%s", events, tc.wantEvents, out)
			}
			for _, want := range tc.wantOutput {
				if !strings.Contains(string(out), want) {
					t.Errorf("missing %q in output:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.noOutput {
				if strings.Contains(string(out), unwanted) {
					t.Errorf("unexpected %q in output:\n%s", unwanted, out)
				}
			}
			if tc.name == "large-log" && len(out) > 10000 {
				t.Errorf("startup diagnostics not bounded: %d bytes", len(out))
			}
			if data := strings.TrimSpace(readOptional(t, filepath.Join(root, "data-path"))); data != "" {
				if !strings.HasPrefix(data, tmp+string(os.PathSeparator)) {
					t.Fatalf("fixture escaped owned temporary tree: %s", data)
				}
				if _, err := os.Stat(data); !os.IsNotExist(err) {
					t.Errorf("disposable fixture remains after probe: %v", err)
				}
			}
			if tc.name == "success" || tc.name == "stop-fail" {
				var dsn string
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(line, "RETURNED_DSN=") {
						dsn = strings.TrimPrefix(line, "RETURNED_DSN=")
					}
				}
				u, err := url.Parse(dsn)
				if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User.Username() != "localtest" || u.Path != "/restaurant_foundation_test" || u.RawQuery != "sslmode=disable" {
					t.Fatalf("successful DSN contract: %q (%v)", dsn, err)
				}
				if got := readOptional(t, filepath.Join(root, "create-args")); got != "-h 127.0.0.1 -p "+u.Port()+" -U localtest restaurant_foundation_test\n" {
					t.Errorf("createdb arguments = %q", got)
				}
				options := readOptional(t, filepath.Join(root, "options"))
				if options != "-F -p "+u.Port()+" -h 127.0.0.1 -k '' -c max_connections=20\n" {
					t.Errorf("server options = %q", options)
				}
			}
		})
	}
}

func readOptional(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const fakeInit = `printf 'initdb\n' >> "$TESTPG_AUDIT/events"
if [ "$TESTPG_CASE" = init-fail ]; then
  printf 'synthetic init error\n' >&2
  exit 1
fi
[ "$1" = -D ] || exit 2
/bin/mkdir -p "$2" || exit 2
printf '%s\n' "$2" > "$TESTPG_AUDIT/data-path"
`

const fakeControl = `while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) shift; data=$1 ;;
    -l) shift; log=$1 ;;
    -o) shift; printf '%s\n' "$1" > "$TESTPG_AUDIT/options" ;;
  esac
  phase=$1
  shift
done
printf '%s\n' "$phase" >> "$TESTPG_AUDIT/events"
if [ "$phase" = stop ]; then
  if [ ! -f "$data/postmaster.pid" ]; then
    printf 'postmaster.pid missing\n' >&2
    exit 1
  fi
  if [ "$TESTPG_CASE" = stop-fail ]; then
    printf 'synthetic stop error\n' >&2
    exit 1
  fi
  exit 0
fi
case "$TESTPG_CASE" in
  start-fail|partial-start|large-log|no-log)
    if [ "$TESTPG_CASE" = partial-start ]; then printf 'synthetic pid\n' > "$data/postmaster.pid"; fi
    if [ "$TESTPG_CASE" = large-log ]; then
      printf 'LOG_HEAD_MUST_NOT_APPEAR\n' > "$log"
      n=0
      while [ "$n" -lt 1200 ]; do printf 'synthetic padding ' >> "$log"; n=$((n+1)); done
      printf 'synthetic log tail\n' >> "$log"
    elif [ "$TESTPG_CASE" != no-log ]; then
      printf 'synthetic server fatal\n' > "$log"
    fi
    printf 'synthetic start error\n' >&2
    exit 1 ;;
esac
printf 'synthetic pid\n' > "$data/postmaster.pid"
`

const fakeCreate = `printf 'createdb\n' >> "$TESTPG_AUDIT/events"
printf '%s\n' "$*" > "$TESTPG_AUDIT/create-args"
if [ "$TESTPG_CASE" = createdb-fail ]; then
  printf 'synthetic createdb error\n' >&2
  exit 1
fi
`

// Exercise the public failure boundary with an uninspectable owned PID path.
func TestUninspectablePIDStillStops(t *testing.T) {
	root := t.TempDir()
	bin, tmp, home := filepath.Join(root, "bin"), filepath.Join(root, "tmp"), filepath.Join(root, "home")
	for _, dir := range []string{bin, tmp, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	control := `while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) shift; data=$1 ;;
    -l) shift; log=$1 ;;
  esac
  phase=$1
  shift
done
printf '%s\n' "$phase" >> "$TESTPG_AUDIT/events"
if [ "$phase" = stop ]; then exit 0; fi
/bin/ln -s postmaster.pid "$data/postmaster.pid" || exit 2
/bin/mkdir "$log" || exit 2
printf 'synthetic uninspectable start error\n' >&2
exit 1
`
	for name, script := range map[string]string{"initdb": fakeInit, "pg_ctl": control, "createdb": fakeCreate} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestStartProbe$", "-test.v")
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home, "TMPDIR=" + tmp,
		"TESTPG_PROBE=1", "TESTPG_CASE=uninspectable", "TESTPG_AUDIT=" + root}
	out, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("want failure exit 1, got %v:\n%s", err, out)
	}
	if got := readOptional(t, filepath.Join(root, "events")); got != "initdb\nstart\nstop\n" {
		t.Fatalf("command attempts = %q:\n%s", got, out)
	}
	for _, want := range []string{"read owned PostgreSQL startup log", "pg_ctl isolated fixture", "synthetic uninspectable start error"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "RETURNED_DSN=") {
		t.Errorf("failed setup returned DSN:\n%s", out)
	}
	data := strings.TrimSpace(readOptional(t, filepath.Join(root, "data-path")))
	if !strings.HasPrefix(data, tmp+string(os.PathSeparator)) {
		t.Fatalf("unowned data: %q", data)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("fixture was not removed: %v", err)
	}
}
