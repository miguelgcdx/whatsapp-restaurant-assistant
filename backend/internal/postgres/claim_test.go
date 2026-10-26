package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

type claimFixture struct {
	dsn      string
	store    *postgres.Store
	job      foundation.Job
	event    string
	observer *pgx.Conn
}

func claimSetup(t *testing.T) claimFixture {
	t.Helper()
	ctx := t.Context()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := time.Now()
	if _, err = s.Accept(ctx, foundation.Event{ID: "claim-fixture", Sender: "fixture-sender", Text: "exact stored content"}, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := s.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	c := claimConn(t, dsn)
	var event string
	if err = c.QueryRow(ctx, `SELECT id::text FROM app.inbound_events WHERE provider_event_id=$1`, job.Event.ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	return claimFixture{dsn, s, job, event, c}
}
func claimConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, e := pgx.Connect(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}
func claimTx(t *testing.T, dsn string) pgx.Tx {
	t.Helper()
	tx, e := claimConn(t, dsn).Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}
func claimCheck(t *testing.T, f claimFixture, job foundation.Job, event, sender string, want error) {
	t.Helper()
	tx := claimTx(t, f.dsn)
	guard, err := postgres.LockExecutionJob(t.Context(), tx, event, sender, job)
	if err == nil {
		if guard == nil {
			t.Fatal("missing closure")
		}
		err = guard()
	}
	if !errors.Is(err, want) || (want != nil && guard != nil && err == nil) {
		t.Fatalf("guard error %v want %v", err, want)
	}
	if e := tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
}
func TestClaimAuthorityTupleCanonicalityAndLifecycle(t *testing.T) {
	f := claimSetup(t)
	ctx := t.Context()
	claimCheck(t, f, f.job, f.event, f.job.Event.Sender, nil)
	// Canonical all-zero is syntactically valid, unlike leaseID's policy.
	zero := f.job
	zero.ID = "00000000-0000-0000-0000-000000000000"
	if _, e := f.observer.Exec(ctx, `UPDATE app.jobs SET id=$1 WHERE id=$2`, zero.ID, f.job.ID); e != nil {
		t.Fatal(e)
	}
	claimCheck(t, f, zero, f.event, zero.Event.Sender, nil)
	if _, e := f.observer.Exec(ctx, `UPDATE app.jobs SET id=$1 WHERE id=$2`, f.job.ID, zero.ID); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"missing", "upper", "unhyphenated", "zero", "generation", "zero-generation", "attempt", "zero-attempt", "provider", "sender", "text", "event", "expected-sender"} {
		t.Run(name, func(t *testing.T) {
			job := f.job
			event, sender := f.event, job.Event.Sender
			switch name {
			case "missing":
				job.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "upper":
				job.ID = strings.ToUpper(job.ID)
			case "unhyphenated":
				job.ID = strings.ReplaceAll(job.ID, "-", "")
			case "zero":
				job.ID = "00000000-0000-0000-0000-000000000000"
			case "generation":
				job.Generation++
			case "zero-generation":
				job.Generation = 0
			case "attempt":
				job.Attempt++
			case "zero-attempt":
				job.Attempt = 0
			case "provider":
				job.Event.ID = "other"
			case "sender":
				job.Event.Sender = "other"
			case "text":
				job.Event.Text += "changed"
			case "event":
				event = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "expected-sender":
				sender = "other"
			}
			claimCheck(t, f, job, event, sender, foundation.ErrStaleLease)
		})
	}
	for _, value := range []string{"NULL", "'infinity'", "'-infinity'", "clock_timestamp()-interval '1 second'"} {
		state := "leased"
		if value == "NULL" {
			state = "queued"
		}
		if _, e := f.observer.Exec(ctx, `UPDATE app.jobs SET state=$2,lease_until=`+value+` WHERE id=$1`, f.job.ID, state); e != nil {
			t.Fatal(e)
		}
		claimCheck(t, f, f.job, f.event, f.job.Event.Sender, foundation.ErrStaleLease)
	}
	reclaimed, ok, err := f.store.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim %v %v", ok, err)
	}
	claimCheck(t, f, f.job, f.event, f.job.Event.Sender, foundation.ErrStaleLease)
	claimCheck(t, f, reclaimed, f.event, reclaimed.Event.Sender, nil)
	if err = f.store.CompleteJob(ctx, reclaimed, "fixture response", time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimCheck(t, f, reclaimed, f.event, reclaimed.Event.Sender, foundation.ErrStaleLease)
	diag, err := f.store.Inspect(ctx, f.job.Event.ID)
	if err != nil || diag.JobCount != 1 || diag.OutboxCount != 1 || diag.JobState != "completed" {
		t.Fatalf("inspect %+v %v", diag, err)
	}
}
func TestClaimAuthoritySafeDependencyAndClosure(t *testing.T) {
	f := claimSetup(t)
	ctx := t.Context()
	tx := claimTx(t, f.dsn)
	guard, err := postgres.LockExecutionJob(ctx, tx, f.event, f.job.Event.Sender, f.job)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if e := guard(); e != postgres.ErrClaimUnavailable || e.Error() != "claim verification temporarily unavailable" {
		t.Fatalf("closed closure: %v", e)
	}
	if g, e := postgres.LockExecutionJob(ctx, tx, f.event, f.job.Event.Sender, f.job); g != nil || e != postgres.ErrClaimUnavailable {
		t.Fatalf("closed tx has closure=%t: %v", g != nil, e)
	}
	if g, e := postgres.LockExecutionJob(ctx, nil, f.event, f.job.Event.Sender, f.job); g != nil || e != postgres.ErrClaimUnavailable {
		t.Fatalf("nil tx has closure=%t: %v", g != nil, e)
	}
	live := claimTx(t, f.dsn)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if g, e := postgres.LockExecutionJob(cancelled, live, f.event, f.job.Event.Sender, f.job); g != nil || e != postgres.ErrClaimUnavailable {
		t.Fatalf("cancel has closure=%t: %v", g != nil, e)
	}
}
func claimWait(t *testing.T, c *pgx.Conn, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var waiting bool
		if e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1)`, prefix+"%").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
	}
}
func claimExpired(t *testing.T, f claimFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var expired bool
		if e := f.observer.QueryRow(ctx, `SELECT clock_timestamp()>=lease_until FROM app.jobs WHERE id=$1`, f.job.ID).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if expired {
			return
		}
	}
}
func TestClaimAuthorityFreshClockAfterBlocking(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial-job-lock", true: "later-statement"}[later], func(t *testing.T) {
			f := claimSetup(t)
			ctx := t.Context()
			gate := claimTx(t, f.dsn)
			reader := claimTx(t, f.dsn)
			if _, e := f.observer.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '800 milliseconds' WHERE id=$1`, f.job.ID); e != nil {
				t.Fatal(e)
			}
			var guard func() error
			var err error
			if later {
				guard, err = postgres.LockExecutionJob(ctx, reader, f.event, f.job.Event.Sender, f.job)
				if err != nil {
					t.Fatal(err)
				}
				if err = guard(); err != nil {
					t.Fatal(err)
				}
				_, err = gate.Exec(ctx, `LOCK TABLE app.outbox IN ACCESS EXCLUSIVE MODE`)
			} else {
				_, err = gate.Exec(ctx, `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`, f.job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if later {
					_, e := reader.Exec(ctx, `SELECT count(*) FROM app.outbox`)
					if e != nil {
						done <- e
						return
					}
					done <- guard()
					return
				}
				g, e := postgres.LockExecutionJob(ctx, reader, f.event, f.job.Event.Sender, f.job)
				if e == nil {
					e = g()
				}
				done <- e
			}()
			prefix := "SELECT j.id"
			if later {
				prefix = "SELECT count(*) FROM app.outbox"
			}
			claimWait(t, f.observer, prefix)
			claimExpired(t, f)
			if err = gate.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, foundation.ErrStaleLease) {
				t.Fatalf("fresh clock: %v", err)
			}
			if err = reader.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			diag, e := f.store.Inspect(ctx, f.job.Event.ID)
			if e != nil || diag.JobCount != 1 || diag.OutboxCount != 0 || diag.JobState != "leased" || diag.JobAttempt != 1 {
				t.Fatalf("no writes: %+v %v", diag, e)
			}
		})
	}
}
