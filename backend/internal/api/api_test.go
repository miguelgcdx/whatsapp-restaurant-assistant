package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

var secret = []byte(strings.Repeat("s", 32))

func signature(body string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}
func request(h http.Handler, method, path, body, sig string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Synthetic-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func assertResponse(t *testing.T, w *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if w.Code != status || w.Body.String() != body {
		t.Fatalf("response = %d %q; want %d %q", w.Code, w.Body.String(), status, body)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatal("missing JSON content type")
	}
}

type failingStore struct {
	foundation.Store
	calls int
}

func (s *failingStore) Accept(context.Context, foundation.Event, time.Time) (bool, error) {
	s.calls++
	return false, errors.New("private database failure")
}

func TestCallback(t *testing.T) {
	ctx := context.Background()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wakes := 0
	h, err := New(store, secret, func(context.Context) error { wakes++; return errors.New("missing worker") })
	if err != nil {
		t.Fatal(err)
	}
	good := `{"event_id":"one","sender":"local","text":" exact 🥘 "}`
	for _, tt := range []struct {
		name, method, path, body, sig string
		status                        int
		response                      string
	}{
		{"bad signature", "POST", "/synthetic/events", good, "sha256=00", 401, "unauthorized"},
		{"missing signature", "POST", "/synthetic/events", good, "", 401, "unauthorized"},
		{"oversized", "POST", "/synthetic/events", strings.Repeat("x", 65537), "signed", 413, "body_too_large"},
		{"unknown field", "POST", "/synthetic/events", `{"event_id":"one","sender":"local","text":"hello","extra":1}`, "signed", 400, "invalid_event"},
		{"trailing JSON", "POST", "/synthetic/events", good + ` {}`, "signed", 400, "invalid_event"},
		{"malformed JSON", "POST", "/synthetic/events", `{`, "signed", 400, "invalid_event"},
		{"blank text", "POST", "/synthetic/events", `{"event_id":"one","sender":"local","text":" "}`, "signed", 400, "invalid_event"},
		{"long text", "POST", "/synthetic/events", `{"event_id":"one","sender":"local","text":"` + strings.Repeat("界", 4001) + `"}`, "signed", 400, "invalid_event"},
		{"invalid UTF8", "POST", "/synthetic/events", `{"event_id":"one","sender":"local","text":"` + string([]byte{255}) + `"}`, "signed", 400, "invalid_event"},
		{"unknown route", "POST", "/other", good, "signed", 404, "not_found"},
		{"wrong method", "GET", "/synthetic/events", good, "signed", 405, "method_not_allowed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sig := tt.sig
			if sig == "signed" {
				sig = signature(tt.body)
			}
			assertResponse(t, request(h, tt.method, tt.path, tt.body, sig), tt.status, `{"error":"`+tt.response+`"}`+"\n")
			if _, err := store.Inspect(ctx, "one"); !errors.Is(err, foundation.ErrNotFound) {
				t.Fatalf("rejection changed stored state: %v", err)
			}
			if wakes != 0 {
				t.Fatal("rejection woke worker")
			}
			job, ok, err := store.ClaimJob(ctx, time.Now(), time.Minute)
			if err != nil || ok {
				t.Fatalf("rejection created job: %+v %v %v", job, ok, err)
			}
		})
	}
	for _, body := range []string{good, good, good + strings.Repeat(" ", 65536-len(good))} {
		assertResponse(t, request(h, "POST", "/synthetic/events", body, signature(body)), 200, "{\"ok\":true}\n")
	}
	snap, err := store.Inspect(ctx, "one")
	if err != nil || snap.Event.Text != " exact 🥘 " || snap.JobCount != 1 || snap.JobAttempt != 0 || snap.OutboxCount != 0 {
		t.Fatalf("accepted snapshot: %+v %v", snap, err)
	}
	conflict := strings.Replace(good, "exact", "different", 1)
	assertResponse(t, request(h, "POST", "/synthetic/events", conflict, signature(conflict)), 409, "{\"error\":\"event_conflict\"}\n")
	after, err := store.Inspect(ctx, "one")
	if err != nil || after != snap {
		t.Fatalf("conflict changed state: %+v %v", after, err)
	}
	if wakes != 3 {
		t.Fatalf("wake calls: %d", wakes)
	}
}

func TestUnavailableAndConfiguration(t *testing.T) {
	s := &failingStore{}
	if _, err := New(s, []byte("short"), nil); err == nil {
		t.Fatal("short secret accepted")
	}
	if _, err := New(nil, secret, nil); err == nil {
		t.Fatal("nil store accepted")
	}
	h, err := New(s, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"event_id":"one","sender":"local","text":"hello"}`
	assertResponse(t, request(h, "POST", "/synthetic/events", body, signature(body)), 503, "{\"error\":\"storage_unavailable\"}\n")
	if s.calls != 1 {
		t.Fatal("missing persistence call")
	}
	// A signature must authenticate the exact bytes, not a decoded/re-encoded event.
	assertResponse(t, request(h, "POST", "/synthetic/events", body+" ", signature(body)), 401, "{\"error\":\"unauthorized\"}\n")
	if s.calls != 1 {
		t.Fatal("unauthenticated storage call")
	}
	for _, tt := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/synthetic/events", 405, "method_not_allowed"},
		{"POST", "/other", 404, "not_found"},
	} {
		w := request(h, tt.method, tt.path, body, signature(body))
		assertResponse(t, w, tt.status, `{"error":"`+tt.code+`"}`+"\n")
		if tt.status == 405 && w.Header().Get("Allow") != "POST" {
			t.Fatal("missing Allow header")
		}
	}
	r := httptest.NewRequest("POST", "/synthetic/events", strings.NewReader(body))
	r.Header.Add("X-Synthetic-Signature", signature(body))
	r.Header.Add("X-Synthetic-Signature", signature(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertResponse(t, w, 401, "{\"error\":\"unauthorized\"}\n")
	if s.calls != 1 {
		t.Fatal("rejected route/method/signature reached storage")
	}
}

func TestNotify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wake.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := Notify(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 32)
	n, _, err := conn.ReadFromUnix(b)
	if err != nil || string(b[:n]) != "wake" {
		t.Fatalf("hint: %q %v", b[:n], err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Notify(ctx, path); err == nil {
		t.Fatal("canceled notification succeeded")
	}
	if err := Notify(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing socket succeeded")
	}
}
