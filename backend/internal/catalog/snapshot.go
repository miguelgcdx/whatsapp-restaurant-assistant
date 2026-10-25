package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSnapshotLimit is a local execution budget, not a restaurant quantity rule.
var ErrSnapshotLimit = errors.New("catalog snapshot exceeds local item budget")

const snapshotItemLimit = 1000
const snapshotTimeout = 5 * time.Second

// MenuSnapshot is a complete, UUID-ordered catalog projection. Open=false permits
// browsing only; neither this value nor its validation authorizes an order.
type MenuSnapshot struct {
	Settings Settings
	Items    []Item
}

// Validate checks projected data without normalizing, sorting or mutating it.
// Domain rules are shared unchanged with catalog setup; failures here describe
// invalid stored/projected data, not a new business policy.
func (s MenuSnapshot) Validate() error {
	if len(s.Items) > snapshotItemLimit {
		return ErrSnapshotLimit
	}
	if s.Items == nil || validate(s.Settings, s.Items) != nil {
		return ErrStoredData
	}
	for i := 1; i < len(s.Items); i++ {
		if s.Items[i-1].ID >= s.Items[i].ID {
			return ErrStoredData
		}
	}
	return nil
}

// Snapshot reads only this server-configured Store's restaurant, including closed
// settings and unavailable items. Settings and items share one database snapshot;
// Page's independently current settings and cursor semantics remain unchanged.
func (s *Store) Snapshot(ctx context.Context) (MenuSnapshot, error) {
	if ctx == nil || s == nil || s.pool == nil || !s.restaurant.Valid {
		return MenuSnapshot{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return MenuSnapshot{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return MenuSnapshot{}, snapshotError(ctx, err)
	}
	defer func() {
		// Caller cancellation must not leave cleanup using an already-dead context.
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	settings, err := readSettings(ctx, tx, s.restaurant)
	if err != nil {
		return MenuSnapshot{}, snapshotError(ctx, err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text,name,price_minor,currency,available,version
 FROM app.menu_items WHERE restaurant_id=$1 ORDER BY id LIMIT $2`, s.restaurant, snapshotItemLimit+1)
	if err != nil {
		return MenuSnapshot{}, snapshotError(ctx, err)
	}
	defer rows.Close()
	out := MenuSnapshot{Settings: settings, Items: make([]Item, 0)}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Name, &item.PriceMinor, &item.Currency, &item.Available, &item.Version); err != nil {
			return MenuSnapshot{}, snapshotError(ctx, err)
		}
		if len(out.Items) == snapshotItemLimit {
			return MenuSnapshot{}, ErrSnapshotLimit
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return MenuSnapshot{}, snapshotError(ctx, err)
	}
	rows.Close()
	if err := out.Validate(); err != nil {
		return MenuSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MenuSnapshot{}, snapshotError(ctx, err)
	}
	return out, nil
}

// readSettings already sanitizes database failures. Check the snapshot context
// first to restore cancellation identity without wrapping any raw driver text.
func snapshotError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return ErrUnavailable
}
