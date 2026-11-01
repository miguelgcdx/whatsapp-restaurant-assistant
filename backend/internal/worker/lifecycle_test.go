package worker_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
	"restaurant-assistant/backend/internal/testsocket"
	"restaurant-assistant/backend/internal/worker"
)

func TestCancellationDuringSend(t *testing.T) {
	s := newStore()
	s.pending = true
	entered := make(chan struct{})
	exited := make(chan struct{})
	_, cancel, _ := start(t, s, worker.Options{}, senderFunc(func(ctx context.Context, d foundation.Delivery) foundation.SendResult {
		close(entered)
		<-ctx.Done()
		close(exited)
		return foundation.SendResult{State: foundation.SendUnknown}
	}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("send did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("send context not cancelled")
	}
}

// A published socket is not proof that Run has finished socket initialization.
// Hold the first store call so the helper cannot observe startup progress yet.
func TestStartWaitsForRunStartup(t *testing.T) {
	s := newStore()
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	path := testsocket.RelativeSocketPath(t, filepath.Join(testsocket.Dir(t), "private", "worker.sock"))
	returned := make(chan struct{})
	go func() {
		start(t, s, worker.Options{SocketPath: path}, senderFunc(accepted))
		close(returned)
	}()
	waitSocket(t, path)
	premature := false
	select {
	case <-returned:
		premature = true
	case <-time.After(50 * time.Millisecond):
	}
	s.mu.Unlock()
	locked = false
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("start did not observe Run startup after releasing the store")
	}
	if premature {
		t.Fatal("start reported readiness from pathname visibility before Run startup was observed")
	}
}

func TestForeignReplacementSurvivesShutdown(t *testing.T) {
	s := newStore()
	path, cancel, done := start(t, s, worker.Options{}, senderFunc(accepted))
	// Rename preserves the old inode while simulating a same-user replacement.
	old := path + ".old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	foreign, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish shutdown")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("foreign socket removed", err)
	} else if !os.SameFile(foreign, info) || info.Mode() != foreign.Mode() {
		t.Fatal("foreign socket identity or permissions changed")
	}
}
func TestSocketDirectoryAndLockGuards(t *testing.T) {
	dir := filepath.Join(testsocket.Dir(t), "public")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Mkdir's requested mode is filtered by umask: establish the public fixture
	// explicitly, or umask 077 turns this negative case into a valid worker.
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("public directory fixture must have mode 0755: info=%v err=%v", info, err)
	}
	publicPath := testsocket.RelativeSocketPath(t, filepath.Join(dir, "w.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := worker.Run(ctx, newStore(), senderFunc(accepted), worker.Options{SocketPath: publicPath})
	contextErr := ctx.Err()
	cancel()
	if contextErr != nil || err == nil || err.Error() != "socket directory must be private and same-user" {
		t.Fatalf("expected public-directory guard denial: err=%v context=%v", err, contextErr)
	}
	for _, path := range []string{publicPath, publicPath + ".lock"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("public-directory rejection left %s: %v", path, err)
		}
	}
	private := filepath.Join(testsocket.Dir(t), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(private, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	path := testsocket.RelativeSocketPath(t, filepath.Join(private, "w.sock"))
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = worker.Run(ctx, newStore(), senderFunc(accepted), worker.Options{SocketPath: path})
	contextErr = ctx.Err()
	cancel()
	if contextErr != nil || err == nil || err.Error() != "cannot open socket lock" {
		t.Fatalf("expected symlink-lock guard denial: err=%v context=%v", err, contextErr)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("symlink-lock rejection left a socket: %v", err)
	}
	if link, err := os.Readlink(path + ".lock"); err != nil || link != target {
		t.Fatalf("lock symlink changed: target=%q err=%v", link, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatal("lock target changed")
	}
}

// This is a worker/store seam, not the parent's API-to-provider seam. All state
// is provisioned by the shared isolated-cluster fixture, never an ambient DSN.
func TestDurableTimerRestartAndOutcomes(t *testing.T) {
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
	// Simulate a crash after recording send intent but before recording outcome.
	if _, err := store.Accept(ctx, foundation.Event{ID: "abandoned", Sender: "abandoned", Text: "unchanged"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	abandoned, ok, err := store.ClaimJob(ctx, time.Now(), time.Second)
	if err != nil || !ok {
		t.Fatal("abandoned claim", err)
	}
	if err := store.CompleteJob(ctx, abandoned, "Local test acknowledgement.", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimDelivery(ctx, time.Now(), 60*time.Millisecond); err != nil || !ok {
		t.Fatal("abandoned send intent", err)
	}
	for _, id := range []string{"accepted", "failed", "unknown"} {
		if _, err := store.Accept(ctx, foundation.Event{ID: id, Sender: id, Text: "unchanged"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	job, ok, err := store.ClaimJob(ctx, time.Now(), time.Second)
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	if err := store.RetryJob(ctx, job, time.Now().Add(60*time.Millisecond), time.Now()); err != nil {
		t.Fatal(err)
	}
	sent := make(chan string, 10)
	sender := senderFunc(func(ctx context.Context, d foundation.Delivery) foundation.SendResult {
		sent <- d.EventID
		return foundation.SendResult{State: foundation.SendState(d.EventID), ProviderID: map[string]string{"accepted": "provider"}[d.EventID]}
	})
	path := testsocket.RelativeSocketPath(t, filepath.Join(testsocket.Dir(t), "private", "w.sock"))
	launch := func() (context.CancelFunc, <-chan error) {
		c, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- worker.Run(c, store, sender, worker.Options{SocketPath: path, RecoveryInterval: time.Hour})
		}()
		return cancel, done
	}
	cancel, done := launch()
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		complete := true
		for _, id := range []string{"accepted", "failed", "unknown"} {
			snap, e := store.Inspect(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			if snap.OutboxState != id {
				complete = false
			}
			if snap.JobCount != 1 || snap.OutboxCount > 1 {
				t.Fatal(snap)
			}
		}
		abandonedSnapshot, e := store.Inspect(ctx, "abandoned")
		if e != nil {
			t.Fatal(e)
		}
		if abandonedSnapshot.OutboxState != "unknown" {
			complete = false
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("persisted timer/outcomes not drained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-sent:
		default:
			t.Fatal("missing send")
		}
	}
	cancel, done = launch()
	defer cancel()
	waitSocket(t, path)
	time.Sleep(60 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-sent:
		t.Fatal("resent terminal outcome", id)
	default:
	}
	for _, id := range []string{"accepted", "failed", "unknown", "abandoned"} {
		snap, e := store.Inspect(ctx, id)
		want := id
		if id == "abandoned" {
			want = "unknown"
		}
		if e != nil || snap.OutboxState != want || snap.JobCount != 1 || snap.OutboxCount != 1 {
			t.Fatal("restart changed durable result", snap, e)
		}
	}
}
