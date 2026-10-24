// Package staff implements local staff authorization without reading auth-private data.
package staff

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/postgres/db"
)

var (
	ErrInput           = errors.New("invalid staff configuration or input")
	ErrUnavailable     = errors.New("staff access temporarily unavailable")
	ErrUnauthenticated = errors.New("authentication is required")
)

func parseRestaurant(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !id.Valid {
		return id, ErrInput
	}
	canonical, err := id.Value()
	if err != nil || canonical != raw {
		return pgtype.UUID{}, ErrInput
	}
	return id, nil
}
func validSubject(subject string) bool {
	return utf8.ValidString(subject) && len(subject) <= 256 && strings.TrimSpace(subject) != "" && !strings.ContainsRune(subject, 0)
}

type Store struct {
	pool       *pgxpool.Pool
	restaurant pgtype.UUID
}

// OpenStore is lazy. Only the explicit migrator applies the app schema.
func OpenStore(ctx context.Context, dsn, restaurantID string) (*Store, error) {
	id, err := parseRestaurant(restaurantID)
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
func (s *Store) Access(ctx context.Context, subject string) (bool, error) {
	if !validSubject(subject) {
		return false, ErrInput
	}
	active, err := db.New(s.pool).StaffMembershipActive(ctx, db.StaffMembershipActiveParams{RestaurantID: s.restaurant, AuthUserID: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	return active, nil
}

// EnsureMember preserves inactive membership and commits identity/member atomically.
func (s *Store) EnsureMember(ctx context.Context, subject string) (bool, error) {
	if !validSubject(subject) {
		return false, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err = q.EnsureRestaurantIdentity(ctx, s.restaurant); err != nil {
		return false, ErrUnavailable
	}
	if err = q.EnsureStaffMembership(ctx, db.EnsureStaffMembershipParams{RestaurantID: s.restaurant, AuthUserID: subject}); err != nil {
		return false, ErrUnavailable
	}
	active, err := q.StaffMembershipActive(ctx, db.StaffMembershipActiveParams{RestaurantID: s.restaurant, AuthUserID: subject})
	if err != nil {
		return false, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return false, ErrUnavailable
	}
	return active, nil
}
func (s *Store) RevokeMember(ctx context.Context, subject string) error {
	if !validSubject(subject) {
		return ErrInput
	}
	if err := db.New(s.pool).RevokeStaffMembership(ctx, db.RevokeStaffMembershipParams{RestaurantID: s.restaurant, AuthUserID: subject}); err != nil {
		return ErrUnavailable
	}
	return nil
}
