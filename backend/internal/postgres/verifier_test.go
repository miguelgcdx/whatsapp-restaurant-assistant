package postgres_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

// S1: independent pools fence expired work, including failed operations.
func TestVerifierAtomicLeaseAndSendRecovery(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	open := func() *postgres.Store {
		s, err := postgres.Open(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	a, b := open(), open()
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := foundation.Event{ID: "verifier-first", Sender: "same", Text: "  exact 🥘\n"}
	if yes, err := a.Accept(t.Context(), e, now); err != nil || !yes {
		t.Fatalf("accept=%v %v", yes, err)
	}
	snap := func() foundation.Snapshot {
		v, err := b.Inspect(t.Context(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snap()
	changed := e
	changed.Sender = "different"
	if yes, err := b.Accept(t.Context(), changed, now); yes || !errors.Is(err, foundation.ErrConflict) {
		t.Fatalf("conflict=%v %v", yes, err)
	}
	if snap() != before {
		t.Fatal("conflict changed public state")
	}
	if yes, err := b.Accept(t.Context(), e, now); err != nil || yes {
		t.Fatalf("replay=%v %v", yes, err)
	}
	old, yes, err := a.ClaimJob(t.Context(), now, time.Second)
	if err != nil || !yes {
		t.Fatalf("claim=%v %v", yes, err)
	}
	later := now.Add(time.Second) // exact expiry, not merely after it
	before = snap()
	for _, err := range []error{
		a.CompleteJob(t.Context(), old, "stale", later),
		a.RetryJob(t.Context(), old, later.Add(time.Hour), later),
		a.FailJob(t.Context(), old, later),
	} {
		if !errors.Is(err, foundation.ErrStaleLease) {
			t.Fatalf("expired operation=%v", err)
		}
	}
	if snap() != before {
		t.Fatal("expired operation changed public state")
	}
	var wg sync.WaitGroup
	owners := make(chan foundation.Job, 2)
	for _, s := range []*postgres.Store{a, b} {
		wg.Go(func() {
			job, ok, err := s.ClaimJob(t.Context(), later, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				owners <- job
			}
		})
	}
	wg.Wait()
	close(owners)
	if len(owners) != 1 {
		t.Fatalf("reclaim owners=%d", len(owners))
	}
	current := <-owners
	if current.Generation != old.Generation+1 || current.Attempt != 2 {
		t.Fatalf("reclaim=%+v", current)
	}
	before = snap()
	// A valid UTF-8 NUL reaches PostgreSQL and fails; completion must roll back.
	if err := a.CompleteJob(t.Context(), current, "bad\x00text", later); err == nil {
		t.Fatal("NUL completion succeeded")
	}
	if snap() != before {
		t.Fatal("failed completion was not atomic")
	}
	if err := b.CompleteJob(t.Context(), current, "reply", later); err != nil {
		t.Fatal(err)
	}
	if err := a.CompleteJob(t.Context(), old, "stale", later); !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatal(err)
	}
	d, yes, err := a.ClaimDelivery(t.Context(), later, time.Second)
	if err != nil || !yes {
		t.Fatalf("send claim=%v %v", yes, err)
	}
	before = snap()
	if before.OutboxState != "sending" || before.OutboxCount != 1 {
		t.Fatalf("intent=%+v", before)
	}
	for _, result := range []foundation.SendResult{{State: "bogus"}, {State: foundation.SendAccepted}} {
		if err := b.FinishDelivery(t.Context(), d, result, later); err == nil {
			t.Fatal("invalid result accepted")
		}
		if snap() != before {
			t.Fatal("invalid result changed intent")
		}
	}
	end := later.Add(time.Second)
	if err := b.FinishDelivery(t.Context(), d, foundation.SendResult{State: foundation.SendFailed}, end); !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatalf("expired finish=%v", err)
	}
	reopened := open()
	if _, ok, err := reopened.ClaimDelivery(t.Context(), end, time.Minute); err != nil || ok {
		t.Fatalf("restart resent=%v %v", ok, err)
	}
	if got := snap(); got.OutboxState != "unknown" || got.JobCount != 1 || got.OutboxCount != 1 {
		t.Fatalf("unknown=%+v", got)
	}
	if _, ok, err := reopened.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("unknown deadline=%v %v", ok, err)
	}
}

// S1: failed predecessors release successors, persisted retries survive restart.
func TestVerifierRetryAndFailedSendOrdering(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, id := range []string{"first", "second"} {
		if ok, err := s.Accept(t.Context(), foundation.Event{ID: id, Sender: "same", Text: id}, now.Add(-time.Duration(i)*time.Hour)); err != nil || !ok {
			t.Fatalf("accept=%v %v", ok, err)
		}
	}
	job, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok || job.Event.ID != "first" {
		t.Fatalf("first=%+v %v %v", job, ok, err)
	}
	due := now.Add(time.Hour)
	if err := s.RetryJob(t.Context(), job, due, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if at, ok, err := reopened.NextDue(t.Context()); err != nil || !ok || !at.Equal(due) {
		t.Fatalf("due=%v %v %v", at, ok, err)
	}
	if _, ok, err := reopened.ClaimJob(t.Context(), due.Add(-time.Microsecond), time.Minute); err != nil || ok {
		t.Fatalf("early/blocked=%v %v", ok, err)
	}
	for _, id := range []string{"first", "second"} {
		job, ok, err := reopened.ClaimJob(t.Context(), due, time.Minute)
		if err != nil || !ok || job.Event.ID != id {
			t.Fatalf("job=%+v %v %v", job, ok, err)
		}
		if err := reopened.CompleteJob(t.Context(), job, "reply", due); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"first", "second"} {
		d, ok, err := reopened.ClaimDelivery(t.Context(), due, time.Minute)
		if err != nil || !ok || d.EventID != id {
			t.Fatalf("delivery=%+v %v %v", d, ok, err)
		}
		if err := reopened.FinishDelivery(t.Context(), d, foundation.SendResult{State: foundation.SendFailed}, due); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := reopened.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("terminal=%v %v", ok, err)
	}
}

// S1: setup guards reject all configured hosts before attempting a connection.
func TestVerifierSetupGuard(t *testing.T) {
	for _, dsn := range []string{"", " ", "postgres://u:private@127.0.0.1/restaurant_foundation_?sslmode=disable", "postgres://u:private@127.0.0.1,192.0.2.1/restaurant_foundation_test?sslmode=disable", "postgres://u:private@localhost/restaurant_foundation_test?sslmode=disable"} {
		if s, err := postgres.Open(t.Context(), dsn); !errors.Is(err, postgres.ErrUnsafeDSN) {
			if s != nil {
				s.Close()
			}
			t.Fatalf("Open=%v", err)
		}
		if err := postgres.Migrate(t.Context(), dsn); !errors.Is(err, postgres.ErrUnsafeDSN) {
			t.Fatalf("Migrate=%v", err)
		}
	}
}
