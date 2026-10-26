package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

// ErrClaimUnavailable deliberately contains no driver or connection details.
var ErrClaimUnavailable = errors.New("claim verification temporarily unavailable")

// LockExecutionJob verifies only legacy-route full execution claims and retains job
// and event locks in tx. The returned clock check is usable only while that
// transaction remains open and owns those locks. Call it after any blocking
// statements and immediately before releasing an outcome or committing.
// This neither completes a job nor creates a transferable authority token.
func LockExecutionJob(ctx context.Context, tx pgx.Tx, eventUUID, sender string, job foundation.Job) (func() error, error) {
	// Preserve cart's canonical UUID semantics, including syntactic all-zero
	// UUIDs. leaseID has a different contract and must not be used here.
	var uuid pgtype.UUID
	if uuid.Scan(job.ID) != nil || !uuid.Valid || job.Generation <= 0 || job.Attempt <= 0 {
		return nil, foundation.ErrStaleLease
	}
	canonical, err := uuid.Value()
	if err != nil || canonical != job.ID {
		return nil, foundation.ErrStaleLease
	}
	if ctx == nil || tx == nil {
		return nil, ErrClaimUnavailable
	}
	var id, eid, state, provider, storedSender, content string
	var generation int64
	var attempt int32
	var deadline pgtype.Timestamptz
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.event_id::text,j.state,j.generation,j.attempt,j.lease_until,e.provider_event_id,e.sender,e.content FROM app.jobs j JOIN app.inbound_events e ON e.id=j.event_id WHERE j.id=$1 AND j.route='legacy_ack' FOR UPDATE OF j FOR SHARE OF e`, job.ID).Scan(&id, &eid, &state, &generation, &attempt, &deadline, &provider, &storedSender, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, foundation.ErrStaleLease
	}
	if err != nil {
		return nil, ErrClaimUnavailable
	}
	if id != job.ID || eid != eventUUID || state != "leased" || generation != job.Generation || attempt != job.Attempt || provider != job.Event.ID || storedSender != sender || storedSender != job.Event.Sender || content != job.Event.Text || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite {
		return nil, foundation.ErrStaleLease
	}
	return func() error {
		// Separate statement AFTER blocking locks/statements. Transaction-start now()
		// and SELECT expressions evaluated before LockRows are not authority clocks.
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return ErrClaimUnavailable
		}
		if !deadline.Time.After(now) {
			return foundation.ErrStaleLease
		}
		return nil
	}, nil
}
