package menugate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

// QueueClarification derives a question from the ACTIVE marker at this actual
// claim's canonical slot, inside the same guarded transaction as outbound commit.
// It accepts no caller prose/kind and never invokes menuengine. Mark and queue
// are separate commits: any queue error leaves the marker alone. Reconcile an
// uncertain commit with ReadQueued(actualSource), never another model invocation.
func (g *Gate) QueueClarification(parent context.Context, claim cart.MenuClaim, hostSlot int64) (out Receipt, err error) {
	if parent == nil || g == nil || g.pool == nil || !canonicalID(g.restaurant) || hostSlot < 0 || claim == (cart.MenuClaim{}) {
		return out, ErrInput
	}
	source, job := claim.Source(), claim.Job()
	if !validSource(source) {
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
	return g.commitReply(ctx, source, job, version, "", &clarificationBinding{slot: hostSlot})
}

type clarificationBinding struct{ slot int64 }

// reply reads only, after the existing restaurant/conversation/source/control/
// job lock prefix. Native menu ownership is already UPDATE-locked, serializing
// marker application. The immutable canonical row and scoped FKs bind one exact
// marker operation, never a same-kind/newest guess or a reconstructed opaque ref.
func (b clarificationBinding) reply(ctx context.Context, tx pgx.Tx, source cart.Source, job foundation.Job, version int64) (string, error) {
	var kind cart.UnresolvedEditKind
	var body string
	var fingerprint []byte
	err := tx.QueryRow(ctx, `SELECT m.kind,c.canonical_intent,c.fingerprint
 FROM app.unresolved_cart_edits m
 JOIN app.cart_execution_calls c ON c.event_id=m.event_id AND c.runtime_slot=m.runtime_slot AND c.operation_id=m.operation_id AND c.restaurant_id=m.restaurant_id AND c.conversation_id=m.conversation_id
 JOIN app.cart_execution_sources s ON s.event_id=m.event_id AND s.restaurant_id=m.restaurant_id AND s.conversation_id=m.conversation_id
 JOIN app.inbound_events e ON e.id=m.event_id
 JOIN app.carts draft ON draft.id=m.cart_id AND draft.restaurant_id=m.restaurant_id AND draft.conversation_id=m.conversation_id
 WHERE m.event_id=$1 AND m.runtime_slot=$2 AND m.restaurant_id=$3 AND m.conversation_id=$4
 AND m.admission_job_id=$5 AND m.control_version=$6 AND m.admission_generation<=$7 AND m.admission_attempt<=$8
 AND e.provider_event_id=$9 AND e.sender=$10 AND m.original_request=e.content
 AND m.resolved_operation IS NULL AND c.action='mark_unresolved_edit'
 AND c.canonical_intent::jsonb=jsonb_build_object('action','mark_unresolved_edit','cart',m.cart_id::text,'expected',m.expected_version,'kind',m.kind)
 FOR UPDATE OF m`, source.EventUUID(), b.slot, source.RestaurantID(), source.ConversationID().String(), job.ID, version, job.Generation, job.Attempt, source.EventProviderID(), source.Sender()).Scan(&kind, &body, &fingerprint)
	if err != nil {
		return "", databaseError(err)
	}
	digest := sha256.Sum256([]byte(body))
	if !bytes.Equal(fingerprint, digest[:]) {
		return "", ErrStoredData
	}
	var question string
	switch kind {
	case cart.UnresolvedAdd:
		question = "Which item would you like to add?"
	case cart.UnresolvedRemove:
		question = "Which item would you like to remove?"
	case cart.UnresolvedSetNote:
		question = "Which item should I change the note for?"
	default:
		return "", ErrStoredData
	}
	if (foundation.Event{ID: "outbox", Sender: "fake", Text: question}).Validate() != nil {
		return "", ErrReply
	}
	return question, nil
}
