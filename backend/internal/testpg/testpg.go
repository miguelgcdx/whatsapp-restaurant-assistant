// Package testpg starts an isolated throwaway PostgreSQL cluster, never an existing DSN.
package testpg

import (
	"context"
	"fmt"
	"net"
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "pg_ctl", "-D", data, "-m", "immediate", "-w", "stop").CombinedOutput(); err != nil {
			t.Errorf("stop owned PostgreSQL cluster: %v: %s", err, out)
		}
	})
	run(t, "pg_ctl", "-D", data, "-l", filepath.Join(root, "postgres.log"), "-o", options, "-w", "start")
	run(t, "createdb", "-h", "127.0.0.1", "-p", fmt.Sprint(port), "-U", "localtest", "restaurant_foundation_test")
	return fmt.Sprintf("postgres://localtest@127.0.0.1:%d/restaurant_foundation_test?sslmode=disable", port)
}

func run(t testing.TB, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s isolated fixture: %v: %s", name, err, out)
	}
}
