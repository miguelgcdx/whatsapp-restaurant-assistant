package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/testsocket"
	"restaurant-assistant/backend/internal/worker"
)

type memoryStore struct {
	foundation.Store
	mu        sync.Mutex
	due       time.Time
	pending   bool
	delivery  bool
	calls     int
	completed chan string
	finished  chan foundation.SendResult
	fail      bool
}

func (s *memoryStore) ClaimJob(ctx context.Context, now time.Time, lease time.Duration) (foundation.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return foundation.Job{}, false, errors.New("unavailable")
	}
	if s.pending && !now.Before(s.due) {
		s.pending = false
		return foundation.Job{ID: "job"}, true, nil
	}
	return foundation.Job{}, false, nil
}
func (s *memoryStore) CompleteJob(ctx context.Context, j foundation.Job, text string, now time.Time) error {
	s.mu.Lock()
	s.delivery = true
	s.mu.Unlock()
	s.completed <- text
	return nil
}
func (s *memoryStore) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (foundation.Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.delivery {
		s.delivery = false
		return foundation.Delivery{ID: "effect", EventID: "event", Text: "Local test acknowledgement."}, true, nil
	}
	return foundation.Delivery{}, false, nil
}
func (s *memoryStore) FinishDelivery(ctx context.Context, d foundation.Delivery, r foundation.SendResult, now time.Time) error {
	s.finished <- r
	return nil
}
func (s *memoryStore) NextDue(context.Context) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.due, s.pending, nil
}
func (s *memoryStore) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

type senderFunc func(context.Context, foundation.Delivery) foundation.SendResult

func (f senderFunc) Send(c context.Context, d foundation.Delivery) foundation.SendResult {
	return f(c, d)
}
func newStore() *memoryStore {
	return &memoryStore{completed: make(chan string, 10), finished: make(chan foundation.SendResult, 10)}
}
func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Lstat(path); e == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("socket not ready")
}
func start(t *testing.T, s *memoryStore, o worker.Options, sender worker.Sender) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	if o.SocketPath == "" {
		o.SocketPath = filepath.Join(testsocket.Dir(t), "private", "worker.sock")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, s, sender, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(time.Second):
			t.Error("worker did not stop")
		}
	})
	waitSocket(t, o.SocketPath)
	return o.SocketPath, cancel, done
}
func accepted(context.Context, foundation.Delivery) foundation.SendResult {
	return foundation.SendResult{State: foundation.SendAccepted, ProviderID: "message"}
}
func TestScheduling(t *testing.T) {
	for _, mode := range []string{"startup", "hint", "timer", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore()
			o := worker.Options{RecoveryInterval: 50 * time.Millisecond}
			if mode == "startup" || mode == "timer" {
				s.pending = true
			}
			if mode == "timer" {
				s.due = time.Now().Add(70 * time.Millisecond)
				o.RecoveryInterval = time.Hour
			}
			path, _, _ := start(t, s, o, senderFunc(accepted))
			if mode == "hint" || mode == "recovery" {
				deadline := time.Now().Add(time.Second)
				for s.count() < 3 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if s.count() < 3 {
					t.Fatal("startup scan did not finish")
				}
				s.mu.Lock()
				s.pending = true
				s.mu.Unlock()
			}
			if mode == "hint" {
				c, e := net.Dial("unixgram", path)
				if e != nil {
					t.Fatal(e)
				}
				_, e = c.Write([]byte("ignored payload"))
				c.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			select {
			case text := <-s.completed:
				if text != "Local test acknowledgement." {
					t.Fatal(text)
				}
			case <-time.After(time.Second):
				t.Fatal("did not drain")
			}
			select {
			case r := <-s.finished:
				if r.State != foundation.SendAccepted {
					t.Fatal(r)
				}
			case <-time.After(time.Second):
				t.Fatal("did not finish")
			}
		})
	}
}
func TestIdleAndCancellation(t *testing.T) {
	s := newStore()
	path, cancel, _ := start(t, s, worker.Options{}, senderFunc(accepted))
	time.Sleep(20 * time.Millisecond)
	before := s.count()
	time.Sleep(70 * time.Millisecond)
	if s.count() != before {
		t.Fatal("idle queries")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("owned socket remains")
}
func TestStoreFailureReturnsWithoutSpin(t *testing.T) {
	s := newStore()
	s.fail = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := worker.Run(ctx, s, senderFunc(accepted), worker.Options{SocketPath: filepath.Join(testsocket.Dir(t), "private", "w.sock")})
	if err == nil || s.count() != 1 {
		t.Fatalf("err=%v calls=%d", err, s.count())
	}
}
func TestSocketOwnership(t *testing.T) {
	s := newStore()
	path, _, _ := start(t, s, worker.Options{}, senderFunc(accepted))
	if err := worker.Run(context.Background(), newStore(), senderFunc(accepted), worker.Options{SocketPath: path}); err == nil {
		t.Fatal("replaced active worker")
	}
	if _, e := os.Lstat(path); e != nil {
		t.Fatal(e)
	}
	other := filepath.Join(testsocket.Dir(t), "private")
	if e := os.Mkdir(other, 0700); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(other, "target")
	if e := os.WriteFile(target, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(other, "w.sock")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	if e := worker.Run(context.Background(), newStore(), senderFunc(accepted), worker.Options{SocketPath: link}); e == nil {
		t.Fatal("accepted symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatal("modified target")
	}
}
func TestStaleSocketRestart(t *testing.T) {
	dir := filepath.Join(testsocket.Dir(t), "private")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "w.sock")
	c, e := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	start(t, newStore(), worker.Options{SocketPath: path}, senderFunc(accepted))
}
func TestInvalidOptions(t *testing.T) {
	for _, o := range []worker.Options{{RecoveryInterval: -1}, {Lease: -1}, {SendTimeout: -1}, {Lease: time.Second, SendTimeout: time.Second}} {
		o.SocketPath = filepath.Join(testsocket.Dir(t), "private", "w.sock")
		if e := worker.Run(context.Background(), newStore(), senderFunc(accepted), o); e == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
func TestSenderURLGuards(t *testing.T) {
	for _, u := range []string{"", "https://example.com", "http://localhost:80", "http://192.0.2.1", "http://user:pass@127.0.0.1", "ftp://127.0.0.1", "http://127.0.0.1/#fragment"} {
		if _, e := worker.NewFakeSender(u); e == nil {
			t.Fatalf("accepted %q", u)
		}
	}
}
func TestSenderOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		state  foundation.SendState
	}{{"accepted", 200, `{"message_id":"id"}`, foundation.SendAccepted}, {"rejected", 422, `{}`, foundation.SendFailed}, {"badrequest", 400, `{}`, foundation.SendFailed}, {"ambiguous", 503, `{}`, foundation.SendUnknown}, {"blank", 200, `{"message_id":" "}`, foundation.SendUnknown}, {"long", 200, `{"message_id":"` + strings.Repeat("x", 129) + `"}`, foundation.SendUnknown}, {"large", 200, strings.Repeat("x", 65537), foundation.SendUnknown}, {"trailing", 200, `{"message_id":"id"} {}`, foundation.SendUnknown}, {"utf8", 200, "{\"message_id\":\"\xff\"}", foundation.SendUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("bad request")
				}
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload) != 3 || payload["event_id"] != "event" || payload["effect_id"] != "effect" || payload["text"] != "text" {
					t.Error("incorrect fake payload", payload, err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s, e := worker.NewFakeSender(server.URL)
			if e != nil {
				t.Fatal(e)
			}
			r := s.Send(context.Background(), foundation.Delivery{ID: "effect", EventID: "event", Text: "text"})
			if r.State != tc.state {
				t.Fatal(r)
			}
			if tc.state == foundation.SendAccepted && r.ProviderID != "id" {
				t.Fatal(r)
			}
		})
	}
}
func TestSenderDeadlineAndRedirect(t *testing.T) {
	var hit atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer redirect.Close()
	s, _ := worker.NewFakeSender(redirect.URL)
	if s.Send(context.Background(), foundation.Delivery{}).State != foundation.SendUnknown || hit.Load() {
		t.Fatal("followed redirect")
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	s, _ = worker.NewFakeSender(slow.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.Send(ctx, foundation.Delivery{}).State != foundation.SendUnknown {
		t.Fatal("timeout not unknown")
	}
}
func TestSendCancellationAndOutcome(t *testing.T) {
	for _, state := range []foundation.SendState{foundation.SendAccepted, foundation.SendFailed, foundation.SendUnknown} {
		t.Run(string(state), func(t *testing.T) {
			s := newStore()
			s.pending = true
			start(t, s, worker.Options{}, senderFunc(func(ctx context.Context, d foundation.Delivery) foundation.SendResult {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Error("missing send bound")
				}
				return foundation.SendResult{State: state, ProviderID: "id"}
			}))
			select {
			case r := <-s.finished:
				if r.State != state {
					t.Fatal(r)
				}
			case <-time.After(time.Second):
				t.Fatal("not finished")
			}
		})
	}
}
