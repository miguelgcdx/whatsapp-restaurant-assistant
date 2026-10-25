package worker_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/testsocket"
)

// Only the child shell changes umask; the concurrently running parent keeps its
// process-wide permissions policy. Select the real guard test, not this probe.
func TestSocketDirectoryAndLockGuardsUnderUmask(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []string{"022", "077"} {
		t.Run(mask, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `umask "$1"; exec "$2" -test.v -test.run '^TestSocketDirectoryAndLockGuards$'`, "worker-guard", mask, binary)
			// No ambient DSN, credentials, or arbitrary test flags reach the child.
			// Compact owned paths leave room for the child's ws-*/public/w.sock.
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + testsocket.Dir(t), "TMPDIR=" + testsocket.Dir(t)}
			output, err := cmd.CombinedOutput()
			exit := -1
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
			t.Logf("umask=%s exit=%d error=%v context=%v\n%s", mask, exit, err, ctx.Err(), output)
			if ctx.Err() != nil {
				t.Fatalf("umask %s: guard test exceeded child deadline (expected prompt rejection): %v", mask, ctx.Err())
			}
			if err != nil {
				t.Fatalf("umask %s: guard test failed: %v", mask, err)
			}
			if !strings.Contains(string(output), "--- PASS: TestSocketDirectoryAndLockGuards (") || !strings.HasSuffix(string(output), "PASS\n") {
				t.Fatalf("umask %s: missing successful named guard test output", mask)
			}
		})
	}
}
