// Package integration_test checks the real local processes through public interfaces.
package integration_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
	"restaurant-assistant/backend/internal/testsocket"
)

type process struct {
	cmd            *exec.Cmd
	done           chan struct{}
	err            error
	stdout, stderr bytes.Buffer
}

func start(t *testing.T, ctx context.Context, bin string, env []string, args ...string) *process {
	t.Helper()
	p := &process{cmd: exec.CommandContext(ctx, bin, args...), done: make(chan struct{})}
	p.cmd.Env = append(os.Environ(), env...)
	p.cmd.Stdout = &p.stdout
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *process) stop(t *testing.T, signal os.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("shutdown timeout; stdout=%q stderr=%q", p.stdout.String(), p.stderr.String())
	}
	if p.err != nil || p.stdout.Len() != 0 || p.stderr.Len() != 0 {
		t.Fatalf("process exit=%v stdout=%q stderr=%q", p.err, p.stdout.String(), p.stderr.String())
	}
}

func build(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	bin := filepath.Join(testsocket.Dir(t), name)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/"+name)
	cmd.Dir = filepath.Join("..", "..")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s=%v stdout=%q stderr=%q", name, err, stdout.String(), stderr.String())
	}
	return bin
}

// These are bounded active test observations, not application idle polling.
func eventually(t *testing.T, ctx context.Context, what string, check func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-tick.C:
		}
	}
}

func TestSeparateProcessesDurabilityWakeAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	dsn := testpg.Start(t)
	apiBin := build(t, ctx, "local-api")
	workerBin := build(t, ctx, "local-worker")
	migrateBin := build(t, ctx, "local-migrate")
	env := []string{"LOCAL_DATABASE_URL=" + dsn}
	setup := exec.CommandContext(ctx, migrateBin)
	setup.Env = append(os.Environ(), env...)
	var setupOut, setupErr bytes.Buffer
	setup.Stdout = &setupOut
	setup.Stderr = &setupErr
	if err := setup.Run(); err != nil || setupOut.String() != "local foundation migrations applied\n" || setupErr.Len() != 0 {
		t.Fatalf("setup=%v stdout=%q stderr=%q", err, setupOut.String(), setupErr.String())
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	socket := testsocket.RelativeSocketPath(t, filepath.Join(testsocket.Dir(t), "worker.sock"))
	type wire struct {
		EventID  string `json:"event_id"`
		EffectID string `json:"effect_id"`
		Text     string `json:"text"`
	}
	received := make(chan wire, 8)
	var sends atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body wire
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		decoder.DisallowUnknownFields()
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.EventID == "" || body.EffectID == "" || body.Text != "Local test acknowledgement." {
			t.Error("invalid fake-provider request")
			http.Error(w, "invalid", 422)
			return
		}
		sends.Add(1)
		select {
		case received <- body:
		default:
			t.Error("unexpected extra dispatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message_id": "fake-" + body.EventID})
	}))
	defer fake.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	key := []byte("synthetic-local-test-secret-32-bytes")
	apiEnv := append(append([]string{}, env...), "SYNTHETIC_WEBHOOK_SECRET="+string(key))
	apiProcess := start(t, ctx, apiBin, apiEnv, "-listen", address, "-wake-socket", socket)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	base := "http://" + address
	eventually(t, ctx, "API listener", func() bool {
		res, err := client.Get(base + "/missing")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode == 404
	})
	event := foundation.Event{ID: "before-worker", Sender: "test-customer", Text: "  exact text 🥘\n"}
	post := func(event foundation.Event) (int, string, error) {
		raw, err := json.Marshal(event)
		if err != nil {
			return 0, "", err
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(raw)
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/synthetic/events", bytes.NewReader(raw))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("X-Synthetic-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		res, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
		return res.StatusCode, string(body), err
	}
	// Concurrent replay while the worker is absent proves durable ingress is
	// independent of wake delivery and allocates one logical job, not twelve.
	type response struct {
		status int
		body   string
		err    error
	}
	responses := make(chan response, 12)
	var requests sync.WaitGroup
	for range 12 {
		requests.Go(func() { status, body, err := post(event); responses <- response{status, body, err} })
	}
	requests.Wait()
	close(responses)
	for r := range responses {
		if r.err != nil || r.status != 200 || r.body != "{\"ok\":true}\n" {
			t.Errorf("callback=%d %q %v", r.status, r.body, r.err)
		}
	}
	if t.Failed() {
		return
	}
	before, err := store.Inspect(ctx, event.ID)
	if err != nil || before.Event != event || before.JobCount != 1 || before.JobAttempt != 0 || before.OutboxCount != 0 || sends.Load() != 0 {
		t.Fatalf("durable ingress before worker: %+v %v sends=%d", before, err, sends.Load())
	}
	conflict := event
	conflict.Text = "different"
	if status, body, err := post(conflict); err != nil || status != 409 || body != "{\"error\":\"event_conflict\"}\n" {
		t.Fatalf("conflict=%d %q %v", status, body, err)
	}
	unchanged, err := store.Inspect(ctx, event.ID)
	if err != nil || unchanged != before {
		t.Fatalf("conflict changed state=%+v %v", unchanged, err)
	}
	workerEnv := append(append([]string{}, env...), "FAKE_PROVIDER_URL="+fake.URL+"/send")
	workerProcess := start(t, ctx, workerBin, workerEnv, "-wake-socket", socket, "-recovery-interval", "1h")
	accepted := func(event foundation.Event) {
		t.Helper()
		eventually(t, ctx, "accepted "+event.ID, func() bool {
			snap, err := store.Inspect(ctx, event.ID)
			return err == nil && snap.JobState == "completed" && snap.OutboxState == "accepted"
		})
		snap, err := store.Inspect(ctx, event.ID)
		if err != nil || snap.Event != event || snap.JobCount != 1 || snap.JobAttempt != 1 || snap.OutboxCount != 1 || snap.ProviderID != "fake-"+event.ID {
			t.Fatalf("accepted snapshot=%+v %v", snap, err)
		}
	}
	accepted(event)
	event2 := foundation.Event{ID: "wake-running-worker", Sender: event.Sender, Text: "second message"}
	if status, body, err := post(event2); err != nil || status != 200 || body != "{\"ok\":true}\n" {
		t.Fatalf("wake callback=%d %q %v", status, body, err)
	}
	accepted(event2) // A one-hour recovery interval makes this a real wake seam.
	workerProcess.stop(t, syscall.SIGTERM)
	event3 := foundation.Event{ID: "restart-backlog", Sender: event.Sender, Text: "persist while worker is stopped"}
	if status, body, err := post(event3); err != nil || status != 200 || body != "{\"ok\":true}\n" {
		t.Fatalf("offline callback=%d %q %v", status, body, err)
	}
	queued, err := store.Inspect(ctx, event3.ID)
	if err != nil || queued.JobCount != 1 || queued.JobAttempt != 0 || queued.OutboxCount != 0 {
		t.Fatalf("offline snapshot=%+v %v", queued, err)
	}
	restarted := start(t, ctx, workerBin, workerEnv, "-wake-socket", socket, "-recovery-interval", "1h")
	accepted(event3)
	restarted.stop(t, syscall.SIGINT)
	apiProcess.stop(t, syscall.SIGTERM)
	if sends.Load() != 3 {
		t.Fatalf("dispatch count=%d", sends.Load())
	}
	effects := map[string]bool{}
	for range 3 {
		select {
		case body := <-received:
			if effects[body.EffectID] {
				t.Fatal("effect ID reused")
			}
			effects[body.EffectID] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if due, ok, err := store.NextDue(ctx); err != nil || ok {
		t.Fatalf("terminal next due=%s %v %v", fmt.Sprint(due), ok, err)
	}
}
