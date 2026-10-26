package cart

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Source is immutable local fixture metadata, NOT a claim, authentication
// credential, or permission to invoke a model. Its zero value is invalid.
// BindSource is the only public way to obtain a populated value.
type Source struct {
	restaurant      string
	conversation    ConversationID
	eventUUID       string
	eventProviderID string
	business        string
	sender          string
}

func (s Source) RestaurantID() string           { return s.restaurant }
func (s Source) ConversationID() ConversationID { return s.conversation }
func (s Source) EventUUID() string              { return s.eventUUID }
func (s Source) EventProviderID() string        { return s.eventProviderID }
func (s Source) Business() string               { return s.business }
func (s Source) Sender() string                 { return s.sender }

// BindSource explicitly binds an already persisted event to current local
// fixture ownership. It allocates no cart, call, slot, or operation identity.
// The whole event namespace is shared with RegisterExecution.
func (s *Store) BindSource(ctx context.Context, local LocalContext, eventProviderID string) (Source, error) {
	if ctx == nil || s == nil || s.pool == nil || !local.valid() || local.restaurant != s.restaurant || !text(eventProviderID, 128, true) {
		return Source{}, ErrInput
	}
	if err := sourceContextError(ctx); err != nil {
		return Source{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fail := func(err error) (Source, error) {
		if contextErr := sourceContextError(bounded); contextErr != nil {
			return Source{}, contextErr
		}
		return Source{}, err
	}
	tx, err := s.begin(bounded, local)
	if err != nil {
		return fail(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	source, err := s.bindSourceTx(bounded, tx, local, eventProviderID)
	if err != nil {
		return fail(err)
	}
	if err = sourceContextError(bounded); err != nil {
		return Source{}, err
	}
	if err = tx.Commit(bounded); err != nil {
		return fail(ErrUnavailable)
	}
	return source, nil
}

// sourceContextError also handles contexts whose deadline has elapsed before
// their Err method publishes it. Only the new API has this error/cap policy.
func sourceContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// bindSourceTx is setup within the caller's existing registration transaction:
// a subsequent call-registration failure must roll back this source as well.
func (s *Store) bindSourceTx(ctx context.Context, tx pgx.Tx, local LocalContext, event string) (Source, error) {
	var eid, sender string
	if err := tx.QueryRow(ctx, `SELECT id::text,sender FROM app.inbound_events WHERE provider_event_id=$1 FOR SHARE`, event).Scan(&eid, &sender); err != nil {
		return Source{}, unavailable(err)
	}
	var restaurant, conversation string
	loadSource := func() error {
		return tx.QueryRow(ctx, `SELECT restaurant_id::text,conversation_id::text FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`, eid).Scan(&restaurant, &conversation)
	}
	err := loadSource()
	if errors.Is(err, pgx.ErrNoRows) {
		// Validate sender before creating ANY setup state. Existing bindings
		// are checked first so every attempted context rebind is a conflict.
		if sender != local.sender {
			return Source{}, ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO app.cart_execution_sources(event_id,restaurant_id,conversation_id,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(event_id) DO NOTHING`, eid, s.restaurant, local.conversation.value, time.Now().UTC())
		if err != nil {
			return Source{}, ErrUnavailable
		}
		// Another context may have registered the event while INSERT waited.
		err = loadSource()
	}
	if err != nil {
		return Source{}, unavailable(err)
	}
	if restaurant != s.restaurant || conversation != local.conversation.value {
		return Source{}, ErrConflict
	}
	if sender != local.sender {
		return Source{}, ErrNotFound
	}
	return Source{
		restaurant: s.restaurant, conversation: local.conversation,
		eventUUID: eid, eventProviderID: event,
		business: local.business, sender: local.sender,
	}, nil
}
