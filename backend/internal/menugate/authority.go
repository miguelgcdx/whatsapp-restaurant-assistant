package menugate

import (
	"context"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
)

type sourceScope struct{ restaurant, conversation, event, provider, business, sender string }

func scopeOf(s cart.Source) sourceScope {
	return sourceScope{s.RestaurantID(), s.ConversationID().String(), s.EventUUID(), s.EventProviderID(), s.Business(), s.Sender()}
}

// lockSource is the single normal authority prefix. Never acquire it while
// holding outbox/attempt locks obtained in an earlier phase. It validates scope,
// not permission: historical reads may observe a later staff control version.
func lockSource(ctx context.Context, tx pgx.Tx, restaurant string, source sourceScope) (mode string, version int64, err error) {
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.restaurants WHERE id=$1 FOR SHARE`, restaurant).Scan(&locked); err != nil {
		return "", 0, databaseError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.cart_conversations WHERE restaurant_id=$1 AND id=$2 AND source='local_fixture' AND business=$3 AND sender=$4 FOR SHARE`, restaurant, source.conversation, source.business, source.sender).Scan(&locked); err != nil {
		return "", 0, databaseError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT event_id::text FROM app.cart_execution_sources WHERE event_id=$1 AND restaurant_id=$2 AND conversation_id=$3 FOR SHARE`, source.event, restaurant, source.conversation).Scan(&locked); err != nil {
		return "", 0, databaseError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR SHARE`, restaurant, source.conversation).Scan(&mode, &version); err != nil {
		return "", 0, databaseError(err)
	}
	if mode != "ai" && mode != "staff" || version < 0 {
		return "", 0, ErrStoredData
	}
	return mode, version, nil
}
