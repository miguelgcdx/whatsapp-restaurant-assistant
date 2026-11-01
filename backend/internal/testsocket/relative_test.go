package testsocket_test

import (
	"os"
	"path/filepath"
	"testing"

	"restaurant-assistant/backend/internal/testsocket"
)

func TestRelativeSocketPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []struct {
		name string
		dir  string
	}{
		{name: "ordinary", dir: "private"},
		{name: "nested_umask_child_layout", dir: "ws-child/private"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			root := testsocket.Dir(t)
			if !filepath.IsAbs(root) {
				t.Fatalf("Dir must remain absolute: %q", root)
			}
			info, err := os.Stat(root)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0700 {
				t.Errorf("Dir permissions = %o, want 700", info.Mode().Perm())
			}
			target := filepath.Join(root, layout.dir, "worker.sock")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			// An ordinary sentinel proves path identity without binding a socket.
			if err := os.WriteFile(target, []byte("fixture identity"), 0600); err != nil {
				t.Fatal(err)
			}
			address := testsocket.RelativeSocketPath(t, target)
			if filepath.IsAbs(address) {
				t.Errorf("socket address must be relative, got %q", address)
			}
			if len(address) > 107 {
				t.Errorf("socket address is %d bytes, want at most 107", len(address))
			}
			resolved, err := filepath.Abs(address)
			if err != nil {
				t.Fatal(err)
			}
			if resolved != filepath.Clean(target) {
				t.Errorf("resolved address = %q, want %q", resolved, target)
			}
			current, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if current != cwd {
				t.Errorf("CWD changed from %q to %q", cwd, current)
			}
		})
	}
}
