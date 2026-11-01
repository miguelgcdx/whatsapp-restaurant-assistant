// Package testsocket keeps test-generated Unix socket paths compact without
// escaping the runtime-owned TMPDIR. Go's full test names can exceed OS limits.
package testsocket

import (
	"os"
	"path/filepath"
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

// RelativeSocketPath addresses an absolute fixture from the unchanged CWD.
// Linux permits 107 pathname bytes; the adjacent .lock is a regular file.
func RelativeSocketPath(t testing.TB, absoluteSocketPath string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Rel(cwd, absoluteSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) > 107 {
		t.Fatalf("relative socket address is %d bytes, want at most 107", len(path))
	}
	return path
}
