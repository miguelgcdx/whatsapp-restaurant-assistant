package menugate

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/worker"
)

// Dispatcher is explicitly configured and LOCAL only. Its trusted Sender is
// injected by the host, never selected by model output or a Receipt.
type Dispatcher struct {
	pool        *pgxpool.Pool
	restaurant  string
	sender      worker.Sender
	sendTimeout time.Duration
}

func OpenDispatcher(ctx context.Context, dsn, restaurantID string, sender worker.Sender, sendTimeout time.Duration) (*Dispatcher, error) {
	if ctx == nil || !canonicalID(restaurantID) || sender == nil || sendTimeout <= 0 {
		return nil, ErrInput
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		return nil, ErrInput
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Dispatcher{pool, restaurantID, sender, sendTimeout}, nil
}
func (d *Dispatcher) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
	}
}

// DispatchOne handles at most one candidate (or an expiry batch). true means
// handled, NOT delivered. Unknown attempts are never automatically revived.
// There is an unavoidable last-check/egress gap; in-flight I/O cannot be recalled.
func (d *Dispatcher) DispatchOne(parent context.Context, lease time.Duration) (handled bool, err error) {
	if parent == nil || d == nil || d.pool == nil || d.sender == nil || lease <= 0 {
		return false, ErrInput
	}
	if err = contextError(parent); err != nil {
		return false, err
	}
	r, found, expired, err := d.claim(parent, lease)
	if err != nil {
		return false, err
	}
	if !found {
		return expired, nil
	}
	delivery, suppressed, err := d.preflight(parent, r)
	if err != nil {
		return true, err
	}
	if suppressed {
		return true, nil
	}
	// Every TX has committed/released before Send; no locks over external I/O.
	sendctx, cancel := context.WithTimeout(parent, d.sendTimeout)
	result := d.sender.Send(sendctx, delivery)
	cancel()
	if !validResult(result) {
		return true, ErrStoredData
	}
	// Do NOT reapply AI mode/version here. Once I/O may have happened, preserve
	// its real outcome, fenced only by the exact current claim/finite lease.
	return true, d.complete(parent, r, result)
}
func validResult(r foundation.SendResult) bool {
	if !utf8.ValidString(r.ProviderID) || utf8.RuneCountInString(r.ProviderID) > 128 {
		return false
	}
	switch r.State {
	case foundation.SendAccepted:
		return strings.TrimSpace(r.ProviderID) != ""
	case foundation.SendFailed, foundation.SendUnknown:
		return r.ProviderID == ""
	default:
		return false
	}
}

func (d *Dispatcher) claim(parent context.Context, lease time.Duration) (r aiRow, found, expired bool, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if e := contextError(ctx); e != nil {
			err = e
		}
	}()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return r, false, false, ErrUnavailable
	}
	defer cleanup(tx)
	// This phase holds only outbox/attempt locks and commits before acquiring
	// ANY earlier source/control/job/event prefix. Avoid outbox->control cycles.
	rows, err := tx.Query(ctx, `UPDATE app.outbox SET state='unknown',lease_until=NULL WHERE kind='ai_reply' AND restaurant_id=$1 AND state='sending' AND lease_until<=clock_timestamp() RETURNING id::text,attempt,generation`, d.restaurant)
	if err != nil {
		return r, false, false, ErrUnavailable
	}
	type old struct {
		id         string
		attempt    int32
		generation int64
	}
	var olds []old
	for rows.Next() {
		var o old
		if err = rows.Scan(&o.id, &o.attempt, &o.generation); err != nil {
			rows.Close()
			return r, false, false, ErrUnavailable
		}
		olds = append(olds, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, false, false, ErrUnavailable
	}
	for _, o := range olds {
		tag, e := tx.Exec(ctx, `UPDATE app.send_attempts SET outcome='unknown',provider_id='',finished_at=clock_timestamp() WHERE outbox_id=$1 AND attempt=$2 AND generation=$3 AND outcome='sending'`, o.id, o.attempt, o.generation)
		if e != nil {
			return r, false, false, ErrUnavailable
		}
		if tag.RowsAffected() != 1 {
			return r, false, false, ErrStoredData
		}
	}
	expired = len(olds) > 0
	var id string
	err = tx.QueryRow(ctx, `SELECT q.id::text FROM app.outbox q JOIN app.inbound_events i ON i.id=q.event_id WHERE q.kind='ai_reply' AND q.restaurant_id=$1 AND q.state='pending' AND q.due_at<=clock_timestamp() AND NOT EXISTS (SELECT 1 FROM app.outbox previous JOIN app.inbound_events pe ON pe.id=previous.event_id WHERE pe.sender=i.sender AND pe.sequence<i.sequence AND previous.state IN ('pending','sending','unknown')) ORDER BY q.due_at,q.id FOR UPDATE OF q SKIP LOCKED LIMIT 1`, d.restaurant).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return r, false, false, ErrUnavailable
		}
		return r, false, expired, nil
	}
	if err != nil {
		return r, false, false, ErrUnavailable
	}
	r, err = readAI(ctx, tx, `id=$1`, id, true)
	if err != nil {
		return r, false, false, err
	}
	if r.restaurant != d.restaurant {
		return r, false, false, ErrStoredData
	}
	// Allocation is serialized by the selected root row. A second dispatcher
	// cannot create another attempt, even before this one's preflight starts.
	if _, err = tx.Exec(ctx, `UPDATE app.outbox SET state='sending',generation=generation+1,attempt=attempt+1,lease_until=clock_timestamp()+$2::bigint * interval '1 microsecond' WHERE id=$1`, id, lease.Microseconds()); err != nil {
		return r, false, false, ErrUnavailable
	}
	r.generation++
	r.attempt++
	if _, err = tx.Exec(ctx, `INSERT INTO app.send_attempts(outbox_id,attempt,generation,started_at,outcome) VALUES($1,$2,$3,clock_timestamp(),'sending')`, id, r.attempt, r.generation); err != nil {
		return r, false, false, ErrUnavailable
	}
	// Recompute lease AFTER any late blocking work, with the database clock.
	if err = tx.QueryRow(ctx, `UPDATE app.outbox SET lease_until=clock_timestamp()+$2::bigint * interval '1 microsecond' WHERE id=$1 RETURNING lease_until`, id, lease.Microseconds()).Scan(&r.lease); err != nil {
		return r, false, false, ErrUnavailable
	}
	r.state = "sending"
	if err = tx.Commit(ctx); err != nil {
		return r, false, false, ErrUnavailable
	}
	return r, true, expired, nil
}

func (d *Dispatcher) preflight(parent context.Context, claim aiRow) (delivery foundation.Delivery, suppressed bool, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if e := contextError(ctx); e != nil {
			err = e
		}
	}()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return delivery, false, ErrUnavailable
	}
	defer cleanup(tx)
	// Unlocked capture is not a send permit. The complete normal prefix follows;
	// reload and compare the whole persisted delivery/attempt after blocking.
	initial, err := readAI(ctx, tx, `id=$1`, claim.id, false)
	if err != nil {
		return delivery, false, err
	}
	if initial != claim {
		return delivery, false, foundation.ErrStaleLease
	}
	scope, err := loadScope(ctx, tx, initial)
	if err != nil {
		return delivery, false, err
	}
	mode, version, err := lockSource(ctx, tx, d.restaurant, scope)
	if err != nil {
		return delivery, false, err
	}
	completed, err := lockCompleted(ctx, tx, scope)
	if err != nil {
		return delivery, false, err
	}
	r, err := readAI(ctx, tx, `id=$1`, claim.id, true)
	if err != nil {
		return delivery, false, err
	}
	if r != claim {
		return delivery, false, foundation.ErrStaleLease
	}
	// Re-read the persisted source under the retained authority/event locks.
	current, err := loadScope(ctx, tx, r)
	if err != nil {
		return delivery, false, err
	}
	if current != scope || r.restaurant != d.restaurant || completed.generation != r.creationGeneration || completed.attempt != r.creationAttempt {
		return delivery, false, ErrStoredData
	}
	if err = lockAttempt(ctx, tx, r); err != nil {
		return delivery, false, err
	}
	if err = freshLease(ctx, tx, r); err != nil {
		return delivery, false, err
	}
	if mode != "ai" || version != r.version {
		if err = finishAI(ctx, tx, r, "suppressed", ""); err != nil {
			return delivery, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return delivery, false, ErrUnavailable
		}
		return delivery, true, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return delivery, false, ErrUnavailable
	}
	return r.delivery(scope.provider), false, nil
}
func (d *Dispatcher) complete(parent context.Context, claim aiRow, result foundation.SendResult) (err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if e := contextError(ctx); e != nil {
			err = e
		}
	}()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer cleanup(tx)
	r, err := readAI(ctx, tx, `id=$1`, claim.id, true)
	if err != nil {
		return err
	}
	if r != claim {
		return foundation.ErrStaleLease
	}
	if err = lockAttempt(ctx, tx, r); err != nil {
		return err
	}
	if err = freshLease(ctx, tx, r); err != nil {
		return err
	}
	if err = finishAI(ctx, tx, r, string(result.State), result.ProviderID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}
