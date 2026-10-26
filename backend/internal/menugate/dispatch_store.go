package menugate

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

type aiRow struct {
	id, event, content, state, provider, restaurant, conversation, sourceProvider, sourceSender, sourceBusiness string
	generation, version, creationGeneration                                                                     int64
	attempt, creationAttempt                                                                                    int32
	lease                                                                                                       pgtype.Timestamptz
}

func (r aiRow) receipt() Receipt {
	return Receipt{r.id, r.content, r.version, r.state, r.attempt, r.provider}
}
func (r aiRow) delivery(provider string) foundation.Delivery {
	return foundation.Delivery{ID: r.id, EventID: provider, Text: r.content, Generation: r.generation, Attempt: r.attempt}
}

// readAI always starts with a root outbox row, never an authority JOIN that can
// silently hide malformed metadata. Predicate is a private fixed SQL fragment.
func readAI(ctx context.Context, tx pgx.Tx, predicate string, arg any, lock bool) (r aiRow, err error) {
	var kind string
	var restaurant, conversation pgtype.UUID
	var version, generation pgtype.Int8
	var attempt pgtype.Int4
	var sourceProvider, sourceSender, sourceBusiness pgtype.Text
	sql := `SELECT id::text,event_id::text,content,state,provider_id,generation,attempt,lease_until,kind,restaurant_id,conversation_id,control_version,creation_generation,creation_attempt,(SELECT e.provider_event_id FROM app.inbound_events e WHERE e.id=o.event_id),(SELECT e.sender FROM app.inbound_events e WHERE e.id=o.event_id),(SELECT c.business FROM app.cart_conversations c WHERE c.id=o.conversation_id AND c.restaurant_id=o.restaurant_id) FROM app.outbox o WHERE ` + predicate
	if lock {
		sql += ` FOR UPDATE`
	}
	err = tx.QueryRow(ctx, sql, arg).Scan(&r.id, &r.event, &r.content, &r.state, &r.provider, &r.generation, &r.attempt, &r.lease, &kind, &restaurant, &conversation, &version, &generation, &attempt, &sourceProvider, &sourceSender, &sourceBusiness)
	if err != nil {
		return r, databaseError(err)
	}
	if kind != "ai_reply" || !restaurant.Valid || !conversation.Valid || !version.Valid || version.Int64 < 0 || !generation.Valid || generation.Int64 <= 0 || !attempt.Valid || attempt.Int32 <= 0 || r.generation < 0 || r.attempt < 0 {
		return r, ErrStoredData
	}
	if !sourceProvider.Valid || !sourceSender.Valid || !sourceBusiness.Valid {
		return r, ErrStoredData
	}
	r.sourceProvider = sourceProvider.String
	r.sourceSender = sourceSender.String
	r.sourceBusiness = sourceBusiness.String
	r.restaurant = restaurant.String()
	r.conversation = conversation.String()
	r.version = version.Int64
	r.creationGeneration = generation.Int64
	r.creationAttempt = attempt.Int32
	if (foundation.Event{ID: "outbox", Sender: "fake", Text: r.content}).Validate() != nil {
		return r, ErrStoredData
	}
	switch r.state {
	case "pending":
		// This coordinator never revives attempts. A rewritten pending row is
		// corruption, not evidence that an earlier possible send stopped.
		if r.lease.Valid || r.generation != 0 || r.attempt != 0 || r.provider != "" {
			return r, ErrStoredData
		}
	case "accepted", "unknown", "failed":
		if r.lease.Valid || r.generation <= 0 || r.attempt <= 0 || !validResult(foundation.SendResult{State: foundation.SendState(r.state), ProviderID: r.provider}) {
			return r, ErrStoredData
		}
	case "suppressed":
		if r.lease.Valid || r.generation <= 0 || r.attempt <= 0 || r.provider != "" {
			return r, ErrStoredData
		}
	case "sending":
		if !r.lease.Valid || r.lease.InfinityModifier != pgtype.Finite || r.generation <= 0 || r.attempt <= 0 || r.provider != "" {
			return r, ErrStoredData
		}
	default:
		return r, ErrStoredData
	}
	return r, nil
}

type completedClaim struct {
	generation int64
	attempt    int32
}

func lockCompleted(ctx context.Context, tx pgx.Tx, s sourceScope) (c completedClaim, err error) {
	var state, provider, sender, content string
	err = tx.QueryRow(ctx, `SELECT j.state,j.generation,j.attempt,e.provider_event_id,e.sender,e.content FROM app.jobs j JOIN app.inbound_events e ON e.id=j.event_id WHERE j.event_id=$1 FOR UPDATE OF j FOR SHARE OF e`, s.event).Scan(&state, &c.generation, &c.attempt, &provider, &sender, &content)
	if err != nil {
		return c, databaseError(err)
	}
	if state != "completed" || c.generation <= 0 || c.attempt <= 0 || provider != s.provider || sender != s.sender || (foundation.Event{ID: provider, Sender: sender, Text: content}).Validate() != nil {
		return c, ErrStoredData
	}
	return c, nil
}
func loadScope(ctx context.Context, tx pgx.Tx, r aiRow) (s sourceScope, err error) {
	s.restaurant = r.restaurant
	s.conversation = r.conversation
	s.event = r.event
	err = tx.QueryRow(ctx, `SELECT e.provider_event_id,c.business,c.sender FROM app.inbound_events e JOIN app.cart_execution_sources x ON x.event_id=e.id JOIN app.cart_conversations c ON c.restaurant_id=x.restaurant_id AND c.id=x.conversation_id WHERE e.id=$1 AND x.restaurant_id=$2 AND x.conversation_id=$3 AND c.source='local_fixture' AND c.sender=e.sender`, r.event, r.restaurant, r.conversation).Scan(&s.provider, &s.business, &s.sender)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrStoredData
	}
	if err != nil {
		return s, ErrUnavailable
	}
	if s.provider != r.sourceProvider || s.sender != r.sourceSender || s.business != r.sourceBusiness {
		return s, foundation.ErrStaleLease
	}
	if !canonicalID(s.restaurant) || !canonicalID(s.conversation) || !canonicalID(s.event) || !text(s.provider, 128) || !text(s.business, 256) || !text(s.sender, 256) {
		return s, ErrStoredData
	}
	return s, nil
}
func lockAttempt(ctx context.Context, tx pgx.Tx, r aiRow) error {
	var generation int64
	var outcome string
	err := tx.QueryRow(ctx, `SELECT generation,outcome FROM app.send_attempts WHERE outbox_id=$1 AND attempt=$2 FOR UPDATE`, r.id, r.attempt).Scan(&generation, &outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStoredData
	}
	if err != nil {
		return ErrUnavailable
	}
	if generation != r.generation || outcome != "sending" {
		return foundation.ErrStaleLease
	}
	return nil
}
func freshLease(ctx context.Context, tx pgx.Tx, r aiRow) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return ErrUnavailable
	}
	if r.state != "sending" || r.generation <= 0 || r.attempt <= 0 || !r.lease.Valid || r.lease.InfinityModifier != pgtype.Finite || !r.lease.Time.After(now) {
		return foundation.ErrStaleLease
	}
	return contextError(ctx)
}
func finishAI(ctx context.Context, tx pgx.Tx, r aiRow, state, provider string) error {
	// Caller holds both rows and has performed freshLease after all blocking.
	if _, err := tx.Exec(ctx, `UPDATE app.outbox SET state=$2,provider_id=$3,lease_until=NULL WHERE id=$1`, r.id, state, provider); err != nil {
		return ErrUnavailable
	}
	if _, err := tx.Exec(ctx, `UPDATE app.send_attempts SET outcome=$3,provider_id=$4,finished_at=clock_timestamp() WHERE outbox_id=$1 AND attempt=$2 AND generation=$5`, r.id, r.attempt, state, provider, r.generation); err != nil {
		return ErrUnavailable
	}
	return nil
}
