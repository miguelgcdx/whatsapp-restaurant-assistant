// Package catalog owns explicitly configured local restaurant catalogs, not staff authorization.
package catalog

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/postgres/db"
)

var (
	ErrInput       = errors.New("invalid catalog input")
	ErrUnavailable = errors.New("catalog temporarily unavailable")
	ErrNotFound    = errors.New("restaurant is not configured")
	ErrConflict    = errors.New("catalog setup conflicts with stored data")
	ErrStale       = errors.New("menu item missing or version stale")
)

type Settings struct {
	Currency         string
	CurrencyExponent int16
	FlatFeeMinor     int64
	Open             bool
	Version          int64
}

type Item struct {
	ID         string
	Name       string
	PriceMinor int64
	Currency   string
	Available  bool
	Version    int64
}

type Store struct {
	pool       *pgxpool.Pool
	restaurant pgtype.UUID
}

func uuid(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !id.Valid {
		return pgtype.UUID{}, ErrInput
	}
	canonical, err := id.Value()
	if err != nil || canonical != raw {
		return pgtype.UUID{}, ErrInput
	}
	return id, nil
}

// OpenStore is lazy, scoped, and never migrates or seeds. DSNs must pass the shared local guard.
func OpenStore(ctx context.Context, dsn, restaurantID string) (*Store, error) {
	id, err := uuid(restaurantID)
	if err != nil {
		return nil, ErrInput
	}
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		return nil, ErrInput
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Store{pool: pool, restaurant: id}, nil
}

func (s *Store) Close() { s.pool.Close() }

func validate(settings Settings, items []Item) error {
	if len(settings.Currency) != 3 || settings.CurrencyExponent < 0 || settings.CurrencyExponent > 6 || settings.FlatFeeMinor < 0 || settings.Version < 0 {
		return ErrInput
	}
	for _, c := range settings.Currency {
		if c < 'A' || c > 'Z' {
			return ErrInput
		}
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if _, err := uuid(item.ID); err != nil {
			return ErrInput
		}
		if seen[item.ID] || !utf8.ValidString(item.Name) || strings.TrimSpace(item.Name) == "" || strings.ContainsRune(item.Name, 0) || utf8.RuneCountInString(item.Name) > 200 || item.PriceMinor < 0 || item.Version < 0 || item.Currency != settings.Currency {
			return ErrInput
		}
		seen[item.ID] = true
	}
	return nil
}

// Initialize is atomic and create-only. Versions are output state: nonnegative
// input versions are ignored and new rows always start at zero. Retry comparison
// excludes availability/version so staff changes can never be reset by setup.
func (s *Store) Initialize(ctx context.Context, settings Settings, items []Item) error {
	if err := validate(settings, items); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Reuse the existing app-owned identity creator, without memberships/auth reads.
	if err = db.New(tx).EnsureRestaurantIdentity(ctx, s.restaurant); err != nil {
		return safe(err)
	}
	var locked pgtype.UUID
	if err = tx.QueryRow(ctx, "SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE", s.restaurant).Scan(&locked); err != nil {
		return safe(err)
	}
	stored, err := readSettings(ctx, tx, s.restaurant)
	switch {
	case err == nil:
		fixed := settings
		fixed.Version = stored.Version
		if fixed != stored {
			return ErrConflict
		}
		menu, err := readMenu(ctx, tx, s.restaurant)
		if err != nil {
			return err
		}
		if len(menu) != len(items) {
			return ErrConflict
		}
		byID := make(map[string]Item, len(menu))
		for _, item := range menu {
			byID[item.ID] = item
		}
		for _, item := range items {
			old, ok := byID[item.ID]
			if !ok || old.Name != item.Name || old.PriceMinor != item.PriceMinor || old.Currency != item.Currency {
				return ErrConflict
			}
		}
	case errors.Is(err, ErrNotFound):
		_, err = tx.Exec(ctx, "INSERT INTO app.restaurant_settings(restaurant_id,currency,currency_exponent,flat_fee_minor,open,version) VALUES($1,$2,$3,$4,$5,0)", s.restaurant, settings.Currency, settings.CurrencyExponent, settings.FlatFeeMinor, settings.Open)
		if err != nil {
			return safe(err)
		}
		for _, item := range items {
			_, err = tx.Exec(ctx, "INSERT INTO app.menu_items(id,restaurant_id,name,price_minor,currency,available,version,created_at) VALUES($1,$2,$3,$4,$5,$6,0,transaction_timestamp())", item.ID, s.restaurant, item.Name, item.PriceMinor, item.Currency, item.Available)
			if err != nil {
				return safe(err)
			}
		}
	default:
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return safe(err)
	}
	return nil
}

// safe never wraps a driver error: it can contain connection details or input.
func safe(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return ErrConflict
	}
	return ErrUnavailable
}

type reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readSettings(ctx context.Context, q reader, id pgtype.UUID) (Settings, error) {
	var s Settings
	err := q.QueryRow(ctx, "SELECT currency,currency_exponent,flat_fee_minor,open,version FROM app.restaurant_settings WHERE restaurant_id=$1", id).Scan(&s.Currency, &s.CurrencyExponent, &s.FlatFeeMinor, &s.Open, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, safe(err)
	}
	return s, nil
}

func readMenu(ctx context.Context, q reader, id pgtype.UUID) ([]Item, error) {
	rows, err := q.Query(ctx, "SELECT id::text,name,price_minor,currency,available,version FROM app.menu_items WHERE restaurant_id=$1 ORDER BY id", id)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	items := make([]Item, 0)
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Name, &item.PriceMinor, &item.Currency, &item.Available, &item.Version); err != nil {
			return nil, safe(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, safe(err)
	}
	return items, nil
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	return readSettings(ctx, s.pool, s.restaurant)
}

// Menu returns all fixed menu entries, including unavailable entries, ordered by canonical ID.
// This internal list is not a public pagination/cursor contract.
func (s *Store) Menu(ctx context.Context) ([]Item, error) {
	return readMenu(ctx, s.pool, s.restaurant)
}

// SetAvailability changes only availability and increments version, even for the
// same boolean. Missing/cross-scope/stale/overflow updates all change zero rows.
func (s *Store) SetAvailability(ctx context.Context, itemID string, expectedVersion int64, available bool) (Item, error) {
	id, err := uuid(itemID)
	if err != nil || expectedVersion < 0 {
		return Item{}, ErrInput
	}
	if expectedVersion == math.MaxInt64 {
		return Item{}, ErrStale
	}
	var item Item
	err = s.pool.QueryRow(ctx, "UPDATE app.menu_items SET available=$1,version=version+1 WHERE restaurant_id=$2 AND id=$3 AND version=$4 RETURNING id::text,name,price_minor,currency,available,version", available, s.restaurant, id, expectedVersion).Scan(&item.ID, &item.Name, &item.PriceMinor, &item.Currency, &item.Available, &item.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrStale
	}
	if err != nil {
		return Item{}, safe(err)
	}
	return item, nil
}
