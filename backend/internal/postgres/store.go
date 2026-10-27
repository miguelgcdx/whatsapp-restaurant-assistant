package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres/db"
)

type Store struct{ pool *pgxpool.Pool }

var _ foundation.Store = (*Store)(nil)

// Replay rolls back the whole attempt, including sequence allocation.
var errReplay = errors.New("identical event replay")

func (s *Store) Close()                     { s.pool.Close() }
func stamp(at time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: at, Valid: true} }
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 5*time.Second)
}

func newID() (pgtype.UUID, error) {
	id := pgtype.UUID{Valid: true}
	if _, err := rand.Read(id.Bytes[:]); err != nil {
		return pgtype.UUID{}, err
	}
	id.Bytes[6] = id.Bytes[6]&0x0f | 0x40
	id.Bytes[8] = id.Bytes[8]&0x3f | 0x80
	return id, nil
}

func leaseID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if value == "" {
		return id, foundation.ErrStaleLease
	}
	if err := id.Scan(value); err != nil {
		return id, foundation.ErrStaleLease
	}
	return id, nil
}

func (s *Store) transact(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Accept(ctx context.Context, e foundation.Event, now time.Time) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	allocation, err := PrepareEventIntake(e)
	if err != nil {
		return false, err
	}
	created := false
	err = s.transact(ctx, func(q *db.Queries) error {
		inserted, err := allocation.Allocate(ctx, q, now)
		if err != nil {
			return err
		}
		if !inserted {
			stored, err := q.GetEventDigest(ctx, e.ID)
			// An existing non-legacy identity is a route conflict, not replay.
			if errors.Is(err, pgx.ErrNoRows) {
				return foundation.ErrConflict
			}
			if err != nil {
				return err
			}
			if !bytes.Equal(stored, allocation.Digest[:]) {
				return foundation.ErrConflict
			}
			return errReplay
		}
		if err := q.InsertJob(ctx, db.InsertJobParams{ID: allocation.JobID, EventID: allocation.EventID, DueAt: stamp(now)}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if errors.Is(err, errReplay) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return created, nil
}

func (s *Store) ClaimJob(ctx context.Context, now time.Time, lease time.Duration) (foundation.Job, bool, error) {
	if lease <= 0 {
		return foundation.Job{}, false, errors.New("lease duration must be positive")
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	row, err := db.New(s.pool).ClaimJob(ctx, db.ClaimJobParams{UntilAt: stamp(now.Add(lease)), NowAt: stamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.Job{}, false, nil
	}
	if err != nil {
		return foundation.Job{}, false, err
	}
	return foundation.Job{ID: row.ID.String(), Generation: row.Generation, Attempt: row.Attempt, Event: foundation.Event{ID: row.ProviderEventID, Sender: row.Sender, Text: row.Content}}, true, nil
}

func (s *Store) CompleteJob(ctx context.Context, job foundation.Job, text string, now time.Time) error {
	if err := (foundation.Event{ID: "outbox", Sender: "fake", Text: text}).Validate(); err != nil {
		return err
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	id, err := leaseID(job.ID)
	if err != nil {
		return err
	}
	outboxID, err := newID()
	if err != nil {
		return err
	}
	return s.transact(ctx, func(q *db.Queries) error {
		if err := setJob(ctx, q, id, job.Generation, "completed", now, now); err != nil {
			return err
		}
		return q.InsertOutbox(ctx, db.InsertOutboxParams{ID: outboxID, JobID: id, Content: text, DueAt: stamp(now)})
	})
}

func setJob(ctx context.Context, q *db.Queries, id pgtype.UUID, generation int64, state string, due, now time.Time) error {
	n, err := q.SetJobState(ctx, db.SetJobStateParams{State: state, DueAt: stamp(due), ID: id, Generation: generation, NowAt: stamp(now)})
	if err != nil {
		return err
	}
	if n != 1 {
		return foundation.ErrStaleLease
	}
	return nil
}

func (s *Store) RetryJob(ctx context.Context, job foundation.Job, due, now time.Time) error {
	if !due.After(now) {
		return errors.New("retry must be scheduled after current time")
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	id, err := leaseID(job.ID)
	if err != nil {
		return err
	}
	return setJob(ctx, db.New(s.pool), id, job.Generation, "queued", due, now)
}

func (s *Store) FailJob(ctx context.Context, job foundation.Job, now time.Time) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	id, err := leaseID(job.ID)
	if err != nil {
		return err
	}
	return setJob(ctx, db.New(s.pool), id, job.Generation, "failed", now, now)
}

// ClaimDelivery durably records intent before returning control to the sender.
// Expired sending leases become unknown; they never become pending again.
func (s *Store) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (foundation.Delivery, bool, error) {
	if lease <= 0 {
		return foundation.Delivery{}, false, errors.New("lease duration must be positive")
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	var delivery foundation.Delivery
	found := false
	err := s.transact(ctx, func(q *db.Queries) error {
		expired, err := q.ExpireSends(ctx, stamp(now))
		if err != nil {
			return err
		}
		for _, row := range expired {
			if err := q.FinishSendAttempt(ctx, db.FinishSendAttemptParams{OutboxID: row.ID, Attempt: row.Attempt, Outcome: "unknown", FinishedAt: stamp(now)}); err != nil {
				return err
			}
		}
		row, err := q.ClaimDelivery(ctx, db.ClaimDeliveryParams{NowAt: stamp(now), UntilAt: stamp(now.Add(lease))})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.InsertSendAttempt(ctx, db.InsertSendAttemptParams{OutboxID: row.ID, Attempt: row.Attempt, Generation: row.Generation, StartedAt: stamp(now)}); err != nil {
			return err
		}
		delivery = foundation.Delivery{ID: row.ID.String(), EventID: row.ProviderEventID, Text: row.Content, Generation: row.Generation, Attempt: row.Attempt}
		found = true
		return nil
	})
	if err != nil {
		return foundation.Delivery{}, false, err
	}
	return delivery, found, nil
}

func (s *Store) FinishDelivery(ctx context.Context, delivery foundation.Delivery, result foundation.SendResult, now time.Time) error {
	switch result.State {
	case foundation.SendAccepted:
		if result.ProviderID == "" {
			return errors.New("accepted delivery requires provider ID")
		}
	case foundation.SendUnknown, foundation.SendFailed:
	default:
		return errors.New("invalid send outcome")
	}
	if !utf8.ValidString(result.ProviderID) || utf8.RuneCountInString(result.ProviderID) > 128 {
		return errors.New("invalid provider ID")
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	id, err := leaseID(delivery.ID)
	if err != nil {
		return err
	}
	return s.transact(ctx, func(q *db.Queries) error {
		n, err := q.SetDeliveryState(ctx, db.SetDeliveryStateParams{ID: id, Generation: delivery.Generation, State: string(result.State), ProviderID: result.ProviderID, NowAt: stamp(now)})
		if err != nil {
			return err
		}
		if n != 1 {
			return foundation.ErrStaleLease
		}
		return q.FinishSendAttempt(ctx, db.FinishSendAttemptParams{OutboxID: id, Attempt: delivery.Attempt, Outcome: string(result.State), ProviderID: result.ProviderID, FinishedAt: stamp(now)})
	})
}

func (s *Store) NextDue(ctx context.Context) (time.Time, bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	row, err := db.New(s.pool).NextDue(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	return row.Time, row.Valid, nil
}

func (s *Store) Inspect(ctx context.Context, eventID string) (foundation.Snapshot, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	row, err := db.New(s.pool).InspectEvent(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.Snapshot{}, foundation.ErrNotFound
	}
	if err != nil {
		return foundation.Snapshot{}, fmt.Errorf("inspect local event: %w", err)
	}
	return foundation.Snapshot{Event: foundation.Event{ID: row.ProviderEventID, Sender: row.Sender, Text: row.Content}, JobState: row.JobState, JobAttempt: row.Attempt, OutboxState: row.OutboxState, ProviderID: row.ProviderID, JobCount: row.JobCount, OutboxCount: row.OutboxCount}, nil
}
