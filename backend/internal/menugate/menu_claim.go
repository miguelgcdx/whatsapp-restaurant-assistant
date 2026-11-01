package menugate

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// executionRoute is a nullable database observation, not execution authority.
// Defer discovery errors until claim verification to retain permission precedence.
// Only lockJob decodes it into a canonical verifier's validated binding.
type executionRoute struct {
	kind    string
	mode    pgtype.Text
	version pgtype.Int8
	err     error
}

func discoverExecutionRoute(ctx context.Context, tx pgx.Tx, source cart.Source) executionRoute {
	var route executionRoute
	err := tx.QueryRow(ctx, `SELECT route,admission_mode,admission_version FROM app.jobs WHERE event_id=$1`, source.EventUUID()).Scan(&route.kind, &route.mode, &route.version)
	if errors.Is(err, pgx.ErrNoRows) {
		route.err = foundation.ErrStaleLease
	} else if err != nil {
		route.err = ErrUnavailable
	}
	return route
}

func lockExecutionSource(ctx context.Context, tx pgx.Tx, restaurant string, source cart.Source) (mode string, version int64, route executionRoute, err error) {
	// Discovery selects lock strength only. Recheck after ownership/control;
	// never lock a job first or upgrade shared ownership after taking control.
	route = discoverExecutionRoute(ctx, tx, source)
	mode, version, err = lockSourceWithStrength(ctx, tx, restaurant, scopeOf(source), route.kind == "menu_message")
	return
}

// permitsAI adds the immutable native admission fence to current control.
// Discovery failures and other routes still reach lockJob's canonical checks;
// this predicate cannot replace structural claim verification or its reread.
func (route executionRoute) permitsAI(mode string, version int64) bool {
	if mode != "ai" {
		return false
	}
	if route.err != nil || route.kind != "menu_message" {
		return true
	}
	return route.mode.Valid && route.mode.String == "ai" &&
		route.version.Valid && route.version.Int64 == version
}

func (route executionRoute) lockJob(ctx context.Context, tx pgx.Tx, source cart.Source, job foundation.Job) (func() error, error) {
	if route.err != nil {
		return nil, route.err
	}
	current := discoverExecutionRoute(ctx, tx, source)
	if current.err != nil {
		return nil, current.err
	}
	if current.kind != route.kind || current.mode != route.mode || current.version != route.version {
		return nil, foundation.ErrStaleLease
	}
	// This reread follows the validated source/control prefix. The verifier
	// below still locks and compares the complete persisted event/job tuple.
	switch current.kind {
	case "legacy_ack":
		if current.mode.Valid || current.version.Valid {
			return nil, ErrStoredData
		}
		return postgres.LockExecutionJob(ctx, tx, source.EventUUID(), source.Sender(), job)
	case "menu_message":
		if !current.mode.Valid || !current.version.Valid || current.version.Int64 < 0 || (current.mode.String != "ai" && current.mode.String != "staff") {
			return nil, ErrStoredData
		}
		binding := postgres.MenuExecutionBinding{
			EventUUID: source.EventUUID(), RestaurantID: source.RestaurantID(),
			ConversationID: source.ConversationID().String(), Business: source.Business(),
			Sender: source.Sender(), AdmissionMode: current.mode.String,
			AdmissionVersion: current.version.Int64,
		}
		return postgres.LockMenuExecutionJob(ctx, tx, binding, job)
	default:
		return nil, ErrStoredData
	}
}
