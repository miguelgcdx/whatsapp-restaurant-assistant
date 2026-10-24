package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/testpg"
)

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := testpg.Start(t)
	if err := Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dsn
}

func accept(t *testing.T, s *Store, id string, now time.Time) foundation.Event {
	t.Helper()
	e := foundation.Event{ID: id, Sender: "test-customer", Text: "one without onions\n"}
	created, err := s.Accept(t.Context(), e, now)
	if err != nil || !created {
		t.Fatalf("Accept = %v, %v", created, err)
	}
	return e
}

func TestStoreRoundTripAndIdempotency(t *testing.T) {
	s, dsn := fixture(t)
	if err := Migrate(t.Context(), dsn); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := accept(t, s, "round-trip", now)
	if created, err := s.Accept(t.Context(), e, now); err != nil || created {
		t.Fatalf("duplicate = %v, %v", created, err)
	}
	changed := e
	changed.Text = "different"
	if _, err := s.Accept(t.Context(), changed, now); !errors.Is(err, foundation.ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	snap, err := s.Inspect(t.Context(), e.ID)
	if err != nil || snap.Event != e || snap.JobCount != 1 || snap.OutboxCount != 0 {
		t.Fatalf("inbound snapshot = %+v, %v", snap, err)
	}
	job, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok || job.Event != e || job.Attempt != 1 {
		t.Fatalf("claim = %+v, %v, %v", job, ok, err)
	}
	if err := s.CompleteJob(t.Context(), job, "local test acknowledgement", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(t.Context(), job, "duplicate", now); !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatalf("second completion = %v", err)
	}
	delivery, ok, err := s.ClaimDelivery(t.Context(), now, time.Minute)
	if err != nil || !ok || delivery.Text != "local test acknowledgement" || delivery.EventID != e.ID {
		t.Fatalf("delivery = %+v, %v, %v", delivery, ok, err)
	}
	if err := s.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "fake-message-1"}, now); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Inspect(t.Context(), e.ID)
	if err != nil || snap.JobState != "completed" || snap.OutboxState != "accepted" || snap.ProviderID != "fake-message-1" || snap.OutboxCount != 1 {
		t.Fatalf("completed = %+v, %v", snap, err)
	}
	if _, ok, err := s.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("idle next = %v, %v", ok, err)
	}
	s.Close()
	reopened, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snap, err = reopened.Inspect(t.Context(), e.ID)
	if err != nil || snap.OutboxState != "accepted" || snap.Event != e {
		t.Fatalf("reopened = %+v, %v", snap, err)
	}
}

func TestExpiredLeasesCannotCompleteOrBlindlyResend(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := accept(t, s, "leases", now)
	old, ok, err := s.ClaimJob(t.Context(), now, time.Second)
	if err != nil || !ok {
		t.Fatalf("initial claim = %v, %v", ok, err)
	}
	later := now.Add(2 * time.Second)
	current, ok, err := s.ClaimJob(t.Context(), later, time.Minute)
	if err != nil || !ok || current.Generation <= old.Generation {
		t.Fatalf("reclaim = %+v, %v, %v", current, ok, err)
	}
	if err := s.CompleteJob(t.Context(), old, "stale", later); !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatalf("stale completion = %v", err)
	}
	if err := s.CompleteJob(t.Context(), current, "current", later); err != nil {
		t.Fatal(err)
	}
	delivery, ok, err := s.ClaimDelivery(t.Context(), later, time.Second)
	if err != nil || !ok {
		t.Fatalf("send claim = %v, %v", ok, err)
	}
	afterCrash := later.Add(2 * time.Second)
	if _, ok, err := s.ClaimDelivery(t.Context(), afterCrash, time.Minute); err != nil || ok {
		t.Fatalf("unknown must not resend = %v, %v", ok, err)
	}
	if err := s.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "late-provider-id"}, afterCrash); !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatalf("stale send = %v", err)
	}
	snap, err := s.Inspect(t.Context(), e.ID)
	if err != nil || snap.OutboxState != "unknown" || snap.OutboxCount != 1 {
		t.Fatalf("unknown = %+v, %v", snap, err)
	}
}

func TestRetryPersistsDueTimeAndFailure(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	accept(t, s, "retry", now)
	job, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	due := now.Add(10 * time.Second)
	if err := s.RetryJob(t.Context(), job, due, now); err != nil {
		t.Fatal(err)
	}
	if next, ok, err := s.NextDue(t.Context()); err != nil || !ok || !next.Equal(due) {
		t.Fatalf("next = %v, %v, %v", next, ok, err)
	}
	if _, ok, err := s.ClaimJob(t.Context(), now, time.Minute); err != nil || ok {
		t.Fatalf("early claim = %v, %v", ok, err)
	}
	job, ok, err = s.ClaimJob(t.Context(), due, time.Minute)
	if err != nil || !ok || job.Attempt != 2 {
		t.Fatalf("retry claim = %+v, %v, %v", job, ok, err)
	}
	if err := s.FailJob(t.Context(), job, due); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("failed work requeued = %v, %v", ok, err)
	}
	snap, err := s.Inspect(t.Context(), "retry")
	if err != nil || snap.JobState != "failed" || snap.OutboxCount != 0 {
		t.Fatalf("failed = %+v, %v", snap, err)
	}
}

func TestConcurrentClaimsHaveOneOwner(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	accept(t, s, "concurrent", now)
	var owners atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				owners.Add(1)
			}
		})
	}
	wg.Wait()
	if owners.Load() != 1 {
		t.Fatalf("owners = %d", owners.Load())
	}
}

func TestRejectedOrUnavailableWriteLeavesNoWork(t *testing.T) {
	s, dsn := fixture(t)
	now := time.Now()
	if _, err := s.Accept(t.Context(), foundation.Event{ID: "bad"}, now); err == nil {
		t.Fatal("invalid accepted")
	}
	if _, err := s.Inspect(t.Context(), "bad"); !errors.Is(err, foundation.ErrNotFound) {
		t.Fatalf("invalid persisted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Accept(ctx, foundation.Event{ID: "cancelled", Sender: "test", Text: "hello"}, now); err == nil {
		t.Fatal("cancelled accepted")
	}
	s.Close()
	if _, err := s.Accept(t.Context(), foundation.Event{ID: "closed", Sender: "test", Text: "hello"}, now); err == nil {
		t.Fatal("closed pool accepted")
	}
	reopened, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, id := range []string{"bad", "cancelled", "closed"} {
		if _, err := reopened.Inspect(t.Context(), id); !errors.Is(err, foundation.ErrNotFound) {
			t.Fatalf("%s persisted: %v", id, err)
		}
	}
}

func TestDatabaseGuardRejectsNonIsolatedTargets(t *testing.T) {
	for _, dsn := range []string{
		"postgres://test@192.0.2.1/restaurant_foundation_test?sslmode=disable",
		"postgres://test@127.0.0.1/postgres?sslmode=disable",
		"postgres://test@example.invalid/restaurant_foundation_test?sslmode=disable",
	} {
		if _, err := Open(t.Context(), dsn); !errors.Is(err, ErrUnsafeDSN) {
			t.Fatalf("Open unsafe = %v", err)
		}
		if err := Migrate(t.Context(), dsn); !errors.Is(err, ErrUnsafeDSN) {
			t.Fatalf("Migrate unsafe = %v", err)
		}
	}
}
