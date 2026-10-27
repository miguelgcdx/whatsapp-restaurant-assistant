package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

// MenuExecutionBinding is expected immutable admission metadata, not current
// model permission or a constructible cart.MenuClaim. Verification must compare
// every field with the persisted source and menu-route job under transaction locks.
type MenuExecutionBinding struct {
	EventUUID        string
	RestaurantID     string
	ConversationID   string
	Business         string
	Sender           string
	AdmissionMode    string
	AdmissionVersion int64
}

// LockMenuExecutionJob retains ownership, event, source and job locks in tx.
// Its fresh-clock closure is valid only while that transaction owns the locks.
// Structural validity does not check current control or grant model permission.
func LockMenuExecutionJob(ctx context.Context, tx pgx.Tx, binding MenuExecutionBinding, job foundation.Job) (func() error, error) {
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
	lookupError := func(err error) error {
		if errors.Is(err, pgx.ErrNoRows) {
			return foundation.ErrStaleLease
		}
		return ErrClaimUnavailable
	}
	// Discovery is not authority; every discovered field is rechecked below.
	var event, restaurant, conversation string
	if err := tx.QueryRow(ctx, `SELECT event_id::text,restaurant_id::text,conversation_id::text FROM app.jobs WHERE id=$1 AND route='menu_message'`, job.ID).Scan(&event, &restaurant, &conversation); err != nil {
		return nil, lookupError(err)
	}
	if event != binding.EventUUID || restaurant != binding.RestaurantID || conversation != binding.ConversationID {
		return nil, foundation.ErrStaleLease
	}
	var business, sender string
	if err := tx.QueryRow(ctx, `SELECT c.business,c.sender FROM app.restaurants r JOIN app.cart_conversations c ON c.restaurant_id=r.id WHERE r.id=$1 AND c.id=$2 AND c.source='local_fixture' FOR UPDATE OF r,c`, restaurant, conversation).Scan(&business, &sender); err != nil {
		return nil, lookupError(err)
	}
	if business != binding.Business || sender != binding.Sender {
		return nil, foundation.ErrStaleLease
	}
	var provider, eventSender, content string
	if err := tx.QueryRow(ctx, `SELECT e.provider_event_id,e.sender,e.content FROM app.inbound_events e JOIN app.cart_execution_sources s ON s.event_id=e.id WHERE e.id=$1 AND s.restaurant_id=$2 AND s.conversation_id=$3 FOR SHARE OF e FOR UPDATE OF s`, event, restaurant, conversation).Scan(&provider, &eventSender, &content); err != nil {
		return nil, lookupError(err)
	}
	if provider != job.Event.ID || eventSender != sender || eventSender != job.Event.Sender || content != job.Event.Text {
		return nil, foundation.ErrStaleLease
	}
	var stored MenuExecutionBinding
	var state string
	var generation int64
	var attempt int32
	var deadline pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `SELECT event_id::text,restaurant_id::text,conversation_id::text,admission_mode,admission_version,state,generation,attempt,lease_until FROM app.jobs WHERE id=$1 AND route='menu_message' FOR UPDATE`, job.ID).Scan(&stored.EventUUID, &stored.RestaurantID, &stored.ConversationID, &stored.AdmissionMode, &stored.AdmissionVersion, &state, &generation, &attempt, &deadline); err != nil {
		return nil, lookupError(err)
	}
	stored.Business, stored.Sender = business, sender
	if stored != binding || stored.AdmissionMode != "ai" || stored.AdmissionVersion < 0 || state != "leased" || generation != job.Generation || attempt != job.Attempt || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite {
		return nil, foundation.ErrStaleLease
	}
	return func() error {
		// A separate statement AFTER locks and any later blocking statements.
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
