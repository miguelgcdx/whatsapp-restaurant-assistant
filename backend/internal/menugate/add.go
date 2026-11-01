package menugate

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/menuengine"
)

var ErrClarificationRequired = errors.New("cart edit requires clarification")

// AddCaller owns its gate and cart store. Close only after active calls finish.
type AddCaller struct {
	gate  *Gate
	store *cart.Store
}

func OpenAddCaller(ctx context.Context, dsn, restaurant string, client *commandcode.Client, maxOutputTokens int) (*AddCaller, error) {
	g, err := Open(ctx, dsn, restaurant, client, maxOutputTokens)
	if err != nil {
		return nil, err
	}
	store, err := cart.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		g.Close()
		return nil, err
	}
	return &AddCaller{gate: g, store: store}, nil
}

func (c *AddCaller) Close() {
	if c != nil {
		if c.store != nil {
			c.store.Close()
		}
		if c.gate != nil {
			c.gate.Close()
		}
	}
}

// AdmitAdd returns a pending reference, never a completed cart update.
func (c *AddCaller) AdmitAdd(parent context.Context, claim cart.MenuClaim) (cart.NativeActionRef, error) {
	if parent == nil || c == nil || c.gate == nil || c.store == nil || claim == (cart.MenuClaim{}) {
		return cart.NativeActionRef{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if err := contextError(ctx); err != nil {
		return cart.NativeActionRef{}, err
	}
	source, job := claim.Source(), claim.Job()
	if !validSource(source) {
		return cart.NativeActionRef{}, ErrInput
	}
	if source.RestaurantID() != c.gate.restaurant {
		return cart.NativeActionRef{}, ErrNotFound
	}
	if _, err := c.gate.check(ctx, source, job, nil); err != nil {
		return cart.NativeActionRef{}, err
	}
	local, err := c.store.LoadLocal(ctx, source.ConversationID(), source.Business(), source.Sender())
	if err != nil {
		return cart.NativeActionRef{}, err
	}
	var raw string
	err = c.gate.pool.QueryRow(ctx, `SELECT id::text FROM app.carts WHERE restaurant_id=$1 AND conversation_id=$2`, c.gate.restaurant, source.ConversationID().String()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return cart.NativeActionRef{}, ErrClarificationRequired
	}
	if err != nil {
		if e := contextError(ctx); e != nil {
			return cart.NativeActionRef{}, e
		}
		return cart.NativeActionRef{}, ErrUnavailable
	}
	id, err := cart.ParseCartID(raw)
	if err != nil {
		return cart.NativeActionRef{}, ErrStoredData
	}
	snapshot, err := c.store.Read(ctx, local, id)
	if err != nil {
		return cart.NativeActionRef{}, err
	}
	call, err := c.gate.engine.ProposeAdd(ctx, job.Event.Text, id, snapshot.Version)
	if errors.Is(err, menuengine.ErrClarificationRequired) {
		return cart.NativeActionRef{}, ErrClarificationRequired
	}
	if err != nil {
		return cart.NativeActionRef{}, err
	}
	if err := contextError(ctx); err != nil {
		return cart.NativeActionRef{}, err
	}
	return c.store.AdmitNativeEdit(ctx, local, claim, 0, call)
}
