package menugate

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/postgres/db"
)

var ErrReply = errors.New("invalid durable menu reply")

// Receipt is historical data. It is neither a lease nor permission to send.
type Receipt struct {
	OutboxID       string
	Content        string
	ControlVersion int64
	State          string
	Attempt        int32
	ProviderID     string
}

// RunAndQueue never accepts caller text or a model-selected ownership scope.
// An uncertain commit returns an error/zero receipt; resolve by ReadQueued, not
// by replaying model work. No transaction survives across engine.Run.
func (g *Gate) RunAndQueue(parent context.Context, source cart.Source, job foundation.Job) (out Receipt, err error) {
	if parent == nil || g == nil || g.pool == nil || g.catalog == nil || g.engine == nil || !canonicalID(g.restaurant) || !validSource(source) {
		return out, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	defer func() {
		if e := contextError(parent); e != nil {
			err = e
		} else if e := contextError(ctx); e != nil {
			err = e
		}
		if err != nil {
			out = Receipt{}
		}
	}()
	if err = contextError(ctx); err != nil {
		return out, err
	}
	if source.RestaurantID() != g.restaurant {
		return out, ErrNotFound
	}
	version, err := g.queuePrecheck(ctx, source, job)
	if err != nil {
		return out, err
	}
	proposal, err := g.engine.Run(ctx, job.Event.Text)
	if e := contextError(ctx); e != nil {
		return out, e
	}
	if err != nil {
		return out, err
	}
	// Reuse the actual foundation validation boundary (4000 runes), preserving
	// bytes. Reasoning, menu snapshots and tool messages never enter the outbox.
	if (foundation.Event{ID: "outbox", Sender: "fake", Text: proposal.Reply}).Validate() != nil {
		return out, ErrReply
	}
	phase, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	defer func() {
		if expired := contextError(phase); expired != nil {
			err = expired
		}
	}()
	tx, err := g.pool.Begin(phase)
	if err != nil {
		return out, ErrUnavailable
	}
	defer cleanup(tx)
	mode, current, err := lockSource(phase, tx, g.restaurant, scopeOf(source))
	if err != nil {
		return out, err
	}
	if mode != "ai" || current != version {
		return out, ErrControlChanged
	}
	guard, err := postgres.LockExecutionJob(phase, tx, source.EventUUID(), source.Sender(), job)
	if err != nil {
		if errors.Is(err, foundation.ErrStaleLease) {
			return out, foundation.ErrStaleLease
		}
		return out, ErrUnavailable
	}
	if source.EventProviderID() != job.Event.ID {
		return out, foundation.ErrStaleLease
	}
	id := pgtype.UUID{Valid: true}
	if _, err = rand.Read(id.Bytes[:]); err != nil {
		return out, ErrUnavailable
	}
	id.Bytes[6] = id.Bytes[6]&15 | 64
	id.Bytes[8] = id.Bytes[8]&63 | 128
	// Reuse the established one-event outbox insertion. Its temporary ACK shape
	// is uncommitted and becomes AI before the transaction can be observed.
	q := db.New(tx)
	var now time.Time
	if err = tx.QueryRow(phase, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return out, ErrUnavailable
	}
	if err = q.InsertOutbox(phase, db.InsertOutboxParams{ID: id, JobID: mustUUID(job.ID), Content: proposal.Reply, DueAt: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
		return out, ErrUnavailable
	}
	if _, err = tx.Exec(phase, `UPDATE app.outbox SET kind='ai_reply',restaurant_id=$2,conversation_id=$3,control_version=$4,creation_generation=$5,creation_attempt=$6 WHERE id=$1`, id, g.restaurant, source.ConversationID().String(), version, job.Generation, job.Attempt); err != nil {
		return out, ErrUnavailable
	}
	// All blocking statements, including the unique effect insert, precede the
	// canonical fresh DB-clock guard. Guard before changing the job state.
	if err = guard(); err != nil {
		if errors.Is(err, foundation.ErrStaleLease) {
			return out, foundation.ErrStaleLease
		}
		return out, ErrUnavailable
	}
	if _, err = tx.Exec(phase, `UPDATE app.jobs SET state='completed',lease_until=NULL WHERE id=$1`, job.ID); err != nil {
		return out, ErrUnavailable
	}
	if err = contextError(phase); err != nil {
		return out, err
	}
	if err = tx.Commit(phase); err != nil {
		return out, ErrUnavailable
	}
	return Receipt{OutboxID: id.String(), Content: proposal.Reply, ControlVersion: version, State: "pending"}, nil
}

// queuePrecheck retains Run's normal lock hierarchy, but the new durable API
// rejects a completed/stale job before considering current AI permission. A
// historical receipt is the only supported way to inspect a completed turn.
func (g *Gate) queuePrecheck(parent context.Context, source cart.Source, job foundation.Job) (version int64, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := contextError(ctx); expired != nil {
			err = expired
		}
	}()
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer cleanup(tx)
	mode, version, err := lockSource(ctx, tx, g.restaurant, scopeOf(source))
	if err != nil {
		return 0, err
	}
	guard, err := postgres.LockExecutionJob(ctx, tx, source.EventUUID(), source.Sender(), job)
	if err != nil {
		if errors.Is(err, foundation.ErrStaleLease) {
			return 0, foundation.ErrStaleLease
		}
		return 0, ErrUnavailable
	}
	if source.EventProviderID() != job.Event.ID {
		return 0, foundation.ErrStaleLease
	}
	if err = guard(); err != nil {
		if errors.Is(err, foundation.ErrStaleLease) {
			return 0, foundation.ErrStaleLease
		}
		return 0, ErrUnavailable
	}
	if mode != "ai" {
		return 0, ErrControlChanged
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, ErrUnavailable
	}
	return version, nil
}

// mustUUID is used only after LockExecutionJob has checked canonical syntax.
func mustUUID(s string) pgtype.UUID { var id pgtype.UUID; _ = id.Scan(s); return id }

func (g *Gate) ReadQueued(parent context.Context, source cart.Source) (out Receipt, err error) {
	if parent == nil || g == nil || g.pool == nil || !canonicalID(g.restaurant) || !validSource(source) {
		return out, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if e := contextError(ctx); e != nil {
			err = e
		}
		if err != nil {
			out = Receipt{}
		}
	}()
	if err = contextError(ctx); err != nil {
		return out, err
	}
	if source.RestaurantID() != g.restaurant {
		return out, ErrNotFound
	}
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return out, ErrUnavailable
	}
	defer cleanup(tx)
	if _, _, err = lockSource(ctx, tx, g.restaurant, scopeOf(source)); err != nil {
		return out, err
	}
	// Probe without locks to distinguish an absent historical effect. The bound
	// row is reloaded under lock only AFTER the job/event prefix below.
	if _, err = readAI(ctx, tx, `event_id=$1`, source.EventUUID(), false); err != nil {
		return out, err
	}
	completed, err := lockCompleted(ctx, tx, scopeOf(source))
	if err != nil {
		return out, err
	}
	row, err := readAI(ctx, tx, `event_id=$1`, source.EventUUID(), true)
	if err != nil {
		return out, err
	}
	if row.restaurant != g.restaurant || row.conversation != source.ConversationID().String() || row.creationGeneration != completed.generation || row.creationAttempt != completed.attempt {
		return out, ErrStoredData
	}
	if err = tx.Commit(ctx); err != nil {
		return out, ErrUnavailable
	}
	return row.receipt(), nil
}
