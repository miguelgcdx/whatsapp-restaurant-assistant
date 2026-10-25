package conversation

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
)

type Store struct {
	pool       *pgxpool.Pool
	restaurant string
	verifier   *staff.Verifier
}

// OpenStore is lazy and performs no migration, provisioning, or environment lookup.
func OpenStore(ctx context.Context, dsn, restaurantID string, verifier *staff.Verifier) (*Store, error) {
	if ctx == nil || verifier == nil {
		return nil, ErrInput
	}
	if _, err := cart.ParseConversationID(restaurantID); err != nil {
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
	return &Store{pool: pool, restaurant: restaurantID, verifier: verifier}, nil
}
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

const (
	initialize = "initialize"
	setMode    = "set_mode"
)

type intent struct {
	action   string
	mode     Mode
	expected pgtype.Int8
	key      string
}

func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// Initialize explicitly creates version zero; it can never reset current control.
func (s *Store) Initialize(ctx context.Context, accessToken string, id cart.ConversationID, mode Mode, key string) (State, error) {
	if !mode.valid() || !validKey(key) {
		return State{}, ErrInput
	}
	return s.run(ctx, accessToken, id, intent{action: initialize, mode: mode, key: key})
}

// SetMode increments even for same-mode assignments. A complete intent's replay
// returns its original result, not the current state and not another mutation.
func (s *Store) SetMode(ctx context.Context, accessToken string, id cart.ConversationID, mode Mode, expectedVersion int64, key string) (State, error) {
	if !mode.valid() || expectedVersion < 0 || !validKey(key) {
		return State{}, ErrInput
	}
	return s.run(ctx, accessToken, id, intent{action: setMode, mode: mode, expected: pgtype.Int8{Int64: expectedVersion, Valid: true}, key: key})
}

// Read does not create missing control and confers no downstream capability.
func (s *Store) Read(ctx context.Context, accessToken string, id cart.ConversationID) (State, error) {
	return s.run(ctx, accessToken, id, intent{})
}

func cleanup(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Cancellation publication can lag a deadline (including an I/O/server error
// arriving first). Observe explicit cancellation first, then the actual deadline;
// a dependency timeout alone is never evidence that the caller has expired.
func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func safe(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if e := contextError(ctx); e != nil {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrUnavailable
}
func missing(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return safe(ctx, err)
}

func (s *Store) run(parent context.Context, token string, id cart.ConversationID, req intent) (out State, err error) {
	if s == nil || s.pool == nil || s.verifier == nil || parent == nil {
		return State{}, ErrInput
	}
	if _, e := cart.ParseConversationID(id.String()); e != nil {
		return State{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			out = State{}
			if expired := contextError(ctx); expired != nil {
				err = expired
			}
		}
	}()
	if err = contextError(ctx); err != nil {
		return State{}, err
	}
	// Verify may fetch JWKS. Never call it again while holding transaction locks.
	actor, err := s.verifier.Verify(ctx, token)
	if expired := contextError(ctx); expired != nil {
		return State{}, expired
	}
	if err != nil {
		if errors.Is(err, staff.ErrUnauthenticated) {
			return State{}, staff.ErrUnauthenticated
		}
		if errors.Is(err, staff.ErrUnavailable) {
			return State{}, staff.ErrUnavailable
		}
		return State{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return State{}, safe(ctx, err)
	}
	defer cleanup(tx)
	lock := " FOR UPDATE"
	if req.action == "" {
		lock = " FOR SHARE"
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.restaurants WHERE id=$1`+lock, s.restaurant).Scan(&locked); err != nil {
		return State{}, missing(ctx, err)
	}
	// This statement runs AFTER any blocking restaurant lock. Holding SHARE until
	// commit makes concurrent revocation serialize, not race a pre-TX boolean.
	var active bool
	err = tx.QueryRow(ctx, `SELECT active FROM app.staff_memberships WHERE restaurant_id=$1 AND auth_user_id=$2 FOR SHARE`, s.restaurant, actor).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return State{}, ErrForbidden
	}
	if err != nil {
		return State{}, safe(ctx, err)
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.cart_conversations WHERE restaurant_id=$1 AND id=$2 AND source='local_fixture'`+lock, s.restaurant, id.String()).Scan(&locked); err != nil {
		return State{}, missing(ctx, err)
	}
	current := State{ConversationID: id}
	err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2`+lock, s.restaurant, id.String()).Scan(&current.Mode, &current.Version)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return State{}, safe(ctx, err)
	}
	if exists && (!current.Mode.valid() || current.Version < 0) {
		return State{}, ErrStoredData
	}
	if req.action == "" {
		if !exists {
			return State{}, ErrNotFound
		}
		out = current
	} else {
		previous, found, e := s.replay(ctx, tx, actor, id, req)
		if e != nil {
			return State{}, e
		}
		if found {
			out = previous
		} else {
			if req.action == initialize {
				if exists {
					return State{}, ErrConflict
				}
				out = State{ConversationID: id, Mode: req.mode, Version: 0}
			} else {
				if !exists {
					return State{}, ErrNotFound
				}
				if current.Version != req.expected.Int64 || current.Version == math.MaxInt64 {
					return State{}, ErrStale
				}
				out = State{ConversationID: id, Mode: req.mode, Version: current.Version + 1}
			}
			if expired := contextError(ctx); expired != nil {
				return State{}, expired
			}
			// Explicit database timestamp, normalized to UTC, not a lease/fencing clock.
			var now time.Time
			if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
				return State{}, safe(ctx, err)
			}
			if req.action == initialize {
				_, err = tx.Exec(ctx, `INSERT INTO app.conversation_controls(restaurant_id,conversation_id,mode,version,updated_at) VALUES($1,$2,$3,0,$4)`, s.restaurant, id.String(), string(out.Mode), now.UTC())
			} else {
				// The restaurant/control locks serialize writers; the version predicate
				// still states CAS at the write boundary and verifies exactly one row.
				result, e := tx.Exec(ctx, `UPDATE app.conversation_controls SET mode=$1,version=$2,updated_at=$3 WHERE restaurant_id=$4 AND conversation_id=$5 AND version=$6`, string(out.Mode), out.Version, now.UTC(), s.restaurant, id.String(), req.expected.Int64)
				err = e
				if err == nil && result.RowsAffected() != 1 {
					return State{}, ErrStale
				}
			}
			if err != nil {
				return State{}, safe(ctx, err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO app.conversation_control_operations(restaurant_id,auth_user_id,operation_key,conversation_id,action,target_mode,expected_version,result_mode,result_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, s.restaurant, actor, req.key, id.String(), req.action, string(req.mode), req.expected, string(out.Mode), out.Version, now.UTC())
			if err != nil {
				return State{}, safe(ctx, err)
			}
		}
	}
	if expired := contextError(ctx); expired != nil {
		return State{}, expired
	}
	if err = tx.Commit(ctx); err != nil {
		return State{}, safe(ctx, err)
	}
	return out, nil
}

func (s *Store) replay(ctx context.Context, tx pgx.Tx, actor string, id cart.ConversationID, req intent) (State, bool, error) {
	var raw, action string
	var target, result Mode
	var expected pgtype.Int8
	var version int64
	err := tx.QueryRow(ctx, `SELECT conversation_id::text,action,target_mode,expected_version,result_mode,result_version FROM app.conversation_control_operations WHERE restaurant_id=$1 AND auth_user_id=$2 AND operation_key=$3 FOR SHARE`, s.restaurant, actor, req.key).Scan(&raw, &action, &target, &expected, &result, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, safe(ctx, err)
	}
	storedID, e := cart.ParseConversationID(raw)
	if e != nil || !target.valid() || !result.valid() || result != target || version < 0 {
		return State{}, false, ErrStoredData
	}
	switch action {
	case initialize:
		if expected.Valid || version != 0 {
			return State{}, false, ErrStoredData
		}
	case setMode:
		if !expected.Valid || expected.Int64 < 0 || expected.Int64 == math.MaxInt64 || version != expected.Int64+1 {
			return State{}, false, ErrStoredData
		}
	default:
		return State{}, false, ErrStoredData
	}
	if storedID != id || action != req.action || target != req.mode || expected.Valid != req.expected.Valid || expected.Valid && expected.Int64 != req.expected.Int64 {
		return State{}, false, ErrConflict
	}
	return State{ConversationID: storedID, Mode: result, Version: version}, true, nil
}
