// Package menugate authorizes one ephemeral local menu proposal against current
// persisted fixture ownership, conversation control and an execution job claim.
// It does not authenticate a provider/customer or authorize durable delivery.
package menugate

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/postgres"
)

var (
	ErrInput          = errors.New("invalid authorized menu turn input")
	ErrNotFound       = errors.New("menu turn source not found")
	ErrControlChanged = errors.New("AI conversation is paused or changed")
	ErrStoredData     = errors.New("invalid stored menu turn authority")
	ErrUnavailable    = errors.New("menu turn authorization temporarily unavailable")
)

// Gate owns immutable configuration and both database pools. Its catalog reader
// is always bound to the SAME DSN/restaurant as its authority checks. The caller
// must close it only after outstanding Run calls have finished.
type Gate struct {
	pool       *pgxpool.Pool
	catalog    *catalog.Store
	engine     *menuengine.Engine
	restaurant string
}

// Open is lazy: no migration, binding, control initialization, key/environment
// lookup or model call. The concrete client is explicit and caller-configured.
func Open(ctx context.Context, dsn, restaurantID string, client *commandcode.Client, maxOutputTokens int) (*Gate, error) {
	if ctx == nil || client == nil || maxOutputTokens < 1 || maxOutputTokens > 8192 || !canonicalID(restaurantID) {
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
	store, err := catalog.OpenStore(ctx, dsn, restaurantID)
	if err != nil {
		pool.Close()
		return nil, ErrUnavailable
	}
	engine, err := menuengine.New(menuengine.Config{Client: client, ReadMenu: store.Snapshot, MaxOutputTokens: maxOutputTokens})
	if err != nil {
		store.Close()
		pool.Close()
		return nil, ErrInput
	}
	return &Gate{pool: pool, catalog: store, engine: engine, restaurant: restaurantID}, nil
}

func (g *Gate) Close() {
	if g != nil {
		if g.catalog != nil {
			g.catalog.Close()
		}
		if g.pool != nil {
			g.pool.Close()
		}
	}
}

func canonicalID(raw string) bool {
	_, err := cart.ParseConversationID(raw)
	return err == nil
}

func text(raw string, max int) bool {
	return utf8.ValidString(raw) && !strings.ContainsRune(raw, 0) && strings.TrimSpace(raw) != "" && utf8.RuneCountInString(raw) <= max
}

func validSource(source cart.Source) bool {
	return canonicalID(source.RestaurantID()) && canonicalID(source.ConversationID().String()) &&
		canonicalID(source.EventUUID()) && text(source.EventProviderID(), 128) &&
		text(source.Business(), 256) && text(source.Sender(), 256)
}

// contextError observes actual elapsed deadlines even when Err publication lags.
// Observable cancellation takes priority over clock-based deadline inference.
func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// Run accepts no text, history, ownership selector or mutable permission flag.
// Every error discards the entire proposal. A successful result is accepted only
// at its postcheck, not a capability for later send/completion/order execution.
func (g *Gate) Run(parent context.Context, source cart.Source, job foundation.Job) (out menuengine.Result, err error) {
	if parent == nil || g == nil || g.pool == nil || g.catalog == nil || g.engine == nil || !canonicalID(g.restaurant) || !validSource(source) {
		return menuengine.Result{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	defer func() {
		if expired := contextError(parent); expired != nil {
			err = expired
		} else if expired := contextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out = menuengine.Result{}
		}
	}()
	if err = contextError(ctx); err != nil {
		return menuengine.Result{}, err
	}
	if source.RestaurantID() != g.restaurant {
		return menuengine.Result{}, ErrNotFound
	}
	version, err := g.check(ctx, source, job, nil)
	if err != nil {
		return menuengine.Result{}, err
	}
	// check committed/released ALL authority locks. Snapshot uses the separately
	// owned, same-config catalog pool; no model I/O occurs inside an authority TX.
	out, err = g.engine.Run(ctx, job.Event.Text)
	if expired := contextError(ctx); expired != nil {
		return menuengine.Result{}, expired
	}
	if err != nil {
		// Only the concrete engine/client can produce this error. They already
		// sanitize dependency details and preserve their safe public identities.
		return menuengine.Result{}, err
	}
	if _, err = g.check(ctx, source, job, &version); err != nil {
		return menuengine.Result{}, err
	}
	return out, nil
}

func cleanup(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return ErrUnavailable
}

// check uses exactly the same normal-TX prefix in both phases. A nil expected
// version means precheck, not a permission mode. No authority survives release;
// the postcheck reloads every row and compares the captured version under locks.
func (g *Gate) check(parent context.Context, source cart.Source, job foundation.Job, expected *int64) (version int64, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := contextError(ctx); expired != nil {
			err = expired
		}
	}()
	if err = contextError(ctx); err != nil {
		return 0, err
	}
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer cleanup(tx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.restaurants WHERE id=$1 FOR SHARE`, g.restaurant).Scan(&locked); err != nil {
		return 0, databaseError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.cart_conversations WHERE restaurant_id=$1 AND id=$2 AND source='local_fixture' AND business=$3 AND sender=$4 FOR SHARE`, g.restaurant, source.ConversationID().String(), source.Business(), source.Sender()).Scan(&locked); err != nil {
		return 0, databaseError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT event_id::text FROM app.cart_execution_sources WHERE event_id=$1 AND restaurant_id=$2 AND conversation_id=$3 FOR SHARE`, source.EventUUID(), g.restaurant, source.ConversationID().String()).Scan(&locked); err != nil {
		return 0, databaseError(err)
	}
	var mode string
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR SHARE`, g.restaurant, source.ConversationID().String()).Scan(&mode, &version); err != nil {
		return 0, databaseError(err)
	}
	if mode != "ai" && mode != "staff" || version < 0 {
		return 0, ErrStoredData
	}
	if mode != "ai" || expected != nil && version != *expected {
		return 0, ErrControlChanged
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
	// This is a separate fresh database-clock statement AFTER every potentially
	// blocking ownership/control/job acquisition, immediately before release.
	if err = guard(); err != nil {
		if errors.Is(err, foundation.ErrStaleLease) {
			return 0, foundation.ErrStaleLease
		}
		return 0, ErrUnavailable
	}
	if err = contextError(ctx); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, ErrUnavailable
	}
	return version, nil
}
