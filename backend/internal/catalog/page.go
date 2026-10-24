package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ErrStoredData identifies persisted values that cannot form a valid catalog page.
// It never carries SQL, driver errors, or connection details.
var ErrStoredData = errors.New("invalid stored catalog data")

// Position is a stable PostgreSQL keyset position, never an authorization grant.
type Position struct {
	CreatedAt time.Time
	ID        string
}

type Page struct {
	Items    []Item
	Settings Settings
	Next     *Position
}

// Page reads at most limit+1 scoped rows. Next always names the last emitted row.
// Settings are current at this read; paging does not promise a frozen snapshot.
func (s *Store) Page(ctx context.Context, limit int, after *Position) (Page, error) {
	if limit < 1 || limit > 100 {
		return Page{}, ErrInput
	}
	var stamp any
	var id any
	if after != nil {
		parsed, err := uuid(after.ID)
		if err != nil || after.CreatedAt.IsZero() || after.CreatedAt.Year() < 1 || after.CreatedAt.Year() > 9999 || after.CreatedAt.Nanosecond()%1000 != 0 {
			return Page{}, ErrInput
		}
		stamp = after.CreatedAt
		id = parsed
	}
	settings, err := readSettings(ctx, s.pool, s.restaurant)
	if err != nil {
		return Page{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text,name,price_minor,currency,available,version,created_at
 FROM app.menu_items WHERE restaurant_id=$1
 AND ($2::timestamptz IS NULL OR (created_at,id)>($2::timestamptz,$3::uuid))
 ORDER BY created_at,id LIMIT $4`, s.restaurant, stamp, id, limit+1)
	if err != nil {
		return Page{}, safe(err)
	}
	defer rows.Close()
	p := Page{Items: make([]Item, 0, limit), Settings: settings}
	var last Position
	for rows.Next() {
		var item Item
		var created pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.Name, &item.PriceMinor, &item.Currency, &item.Available, &item.Version, &created); err != nil {
			return Page{}, safe(err)
		}
		// PostgreSQL admits infinities and years outside RFC3339. Decode without
		// losing that distinction, and validate every fetched row (even lookahead)
		// before emitting any part of the page or constructing a continuation.
		if !created.Valid || created.InfinityModifier != pgtype.Finite || created.Time.IsZero() || created.Time.UTC().Year() < 1 || created.Time.UTC().Year() > 9999 || created.Time.Nanosecond()%1000 != 0 {
			return Page{}, ErrStoredData
		}
		if len(p.Items) == limit {
			copy := last
			p.Next = &copy
			continue
		}
		p.Items = append(p.Items, item)
		last = Position{CreatedAt: created.Time.UTC(), ID: item.ID}
	}
	if err := rows.Err(); err != nil {
		return Page{}, safe(err)
	}
	return p, nil
}
