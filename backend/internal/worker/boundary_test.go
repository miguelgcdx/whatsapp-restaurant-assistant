package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/testsocket"
	"restaurant-assistant/backend/internal/worker"
)

func TestSymlinkAncestorDoesNotCreateState(t *testing.T) {
	root := testsocket.Dir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path := testsocket.RelativeSocketPath(t, filepath.Join(link, "new", "w.sock"))
	if err := worker.Run(context.Background(), newStore(), senderFunc(accepted), worker.Options{SocketPath: path}); err == nil {
		t.Fatal("accepted symlink ancestor")
	}
	if _, err := os.Lstat(filepath.Join(target, "new")); !os.IsNotExist(err) {
		t.Fatal("rejected path created state through symlink", err)
	}
}
func TestSenderResponseBoundariesAndNoProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("environment proxy used") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	for _, tc := range []struct{ name, body, id string }{{"128 Unicode codepoints", `{"message_id":"` + strings.Repeat("é", 128) + `"}`, strings.Repeat("é", 128)}, {"exact response limit", `{"message_id":"id"}` + strings.Repeat(" ", 65536-len(`{"message_id":"id"}`)), "id"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			s, err := worker.NewFakeSender(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			result := s.Send(context.Background(), foundation.Delivery{})
			if result.State != foundation.SendAccepted || result.ProviderID != tc.id {
				t.Fatal(result)
			}
		})
	}
}
