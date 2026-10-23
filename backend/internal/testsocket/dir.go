// Package testsocket keeps test-generated Unix socket paths compact without
// escaping the runtime-owned TMPDIR. Go's full test names can exceed OS limits.
package testsocket

import (
	"os"
	"testing"
)

// Dir has t.TempDir's ownership and cleanup semantics with a compact basename.
// Callers still must choose a TMPDIR short enough for their OS socket limit.
func Dir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove owned socket test directory: %v", err)
		}
	})
	return dir
}
