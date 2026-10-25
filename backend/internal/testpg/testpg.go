// Package testpg starts an isolated throwaway PostgreSQL cluster, never an existing DSN.
package testpg

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Start requires installed PostgreSQL binaries. Missing integration prerequisites
// are failures, not skipped tests that could be mistaken for database proof.
func Start(t testing.TB) string {
	t.Helper()
	for _, name := range []string{"initdb", "pg_ctl", "createdb"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("isolated PostgreSQL prerequisite %s: %v", name, err)
		}
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	run(t, "initdb", "-D", data, "-U", "localtest", "-A", "trust", "--no-locale", "-E", "UTF8", "--no-sync")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	// All fixture clients use TCP. Disable the unused Unix listener so long
	// runtime temporary paths cannot prevent an isolated PostgreSQL startup.
	options := fmt.Sprintf("-F -p %d -h 127.0.0.1 -k '' -c max_connections=20", port)
	started := false
	t.Cleanup(func() {
		// A failed pg_ctl start can leave a live postmaster behind. Still try to
		// stop it when its PID file exists (or cannot be checked), but don't
		// manufacture a second error when startup never created one.
		if !started {
			if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); os.IsNotExist(err) {
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "pg_ctl", "-D", data, "-m", "immediate", "-w", "stop").CombinedOutput(); err != nil {
			t.Errorf("stop owned PostgreSQL cluster: %v: %s", err, out)
		}
	})
	log := filepath.Join(root, "postgres.log")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pg_ctl", "-D", data, "-l", log, "-o", options, "-w", "start").CombinedOutput()
	if err != nil {
		// Report before testing removes the owned temporary directory. pg_ctl's
		// own output generally only says to examine this otherwise lost log.
		reportStartupLog(t, log)
		t.Fatalf("pg_ctl isolated fixture: %v: %s", err, out)
	}
	started = true
	run(t, "createdb", "-h", "127.0.0.1", "-p", fmt.Sprint(port), "-U", "localtest", "restaurant_foundation_test")
	return fmt.Sprintf("postgres://localtest@127.0.0.1:%d/restaurant_foundation_test?sslmode=disable", port)
}

// Keep the useful end of this fixture's server log, never an unbounded file.
func reportStartupLog(t testing.TB, path string) {
	t.Helper()
	const limit = 8 * 1024
	f, err := os.Open(path)
	if err != nil {
		t.Logf("read owned PostgreSQL startup log: %v", err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err == nil && info.Size() > limit {
		_, err = f.Seek(-limit, io.SeekEnd)
		if err == nil {
			t.Logf("owned PostgreSQL startup log truncated to last %d bytes", limit)
		}
	}
	if err != nil {
		t.Logf("read owned PostgreSQL startup log: %v", err)
		return
	}
	out, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		t.Logf("read owned PostgreSQL startup log: %v", err)
		return
	}
	t.Logf("owned PostgreSQL startup log:\n%s", out)
}

func run(t testing.TB, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s isolated fixture: %v: %s", name, err, out)
	}
}
