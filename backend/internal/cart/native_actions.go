package cart

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// ErrNativeAdmissionClosed distinguishes lost conversational permission from a stale lease.
var ErrNativeAdmissionClosed = errors.New("native cart admission is no longer permitted")

// NativeActionRef is server metadata, not model input or execution authority.
// Only successful admission produces a populated value; there is no parser.
type NativeActionRef struct {
	source    Source
	slot      int64
	operation OperationID
}

func (r NativeActionRef) OperationID() OperationID { return r.operation }

// AdmitNativeEdit persists one trusted edit without applying it.
func (s *Store) AdmitNativeEdit(parent context.Context, local LocalContext, claim MenuClaim, slot int64, call Call) (NativeActionRef, error) {
	return s.admitNativeEdit(parent, local, claim, slot, call, nil)
}

// Both entry points share canonical registration, eligibility and actual
// admission authority. Only the explicit entry point supplies a resolution.
func (s *Store) admitNativeEdit(parent context.Context, local LocalContext, claim MenuClaim, slot int64, call Call, resolution *resolvedAdmission) (out NativeActionRef, err error) {
	if parent == nil || s == nil || s.pool == nil || !local.valid() || local.restaurant != s.restaurant || slot < 0 {
		return NativeActionRef{}, ErrInput
	}
	if call.unresolved != nil || call.resolution != nil || call.draft != nil || call.preview != nil || (call.req.Action != "add" && call.req.Action != "remove" && call.req.Action != "set_note") {
		return NativeActionRef{}, ErrInput
	}
	body, err := encodeCall(call)
	if err != nil || len(body) > resultBudget {
		return NativeActionRef{}, ErrInput
	}
	validated, err := decodeCall(string(body))
	if err != nil {
		return NativeActionRef{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out = NativeActionRef{}
		}
	}()
	if err = sourceContextError(ctx); err != nil {
		return NativeActionRef{}, err
	}
	if claim == (MenuClaim{}) || claim.source.restaurant != local.restaurant || claim.source.conversation != local.conversation || claim.source.business != local.business || claim.source.sender != local.sender || claim.mode != "ai" || claim.version < 0 {
		return NativeActionRef{}, foundation.ErrStaleLease
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return NativeActionRef{}, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	source, err := s.bindSourceTx(ctx, tx, local, claim.source.eventProviderID)
	if err != nil {
		if err == ErrConflict || err == ErrNotFound {
			err = foundation.ErrStaleLease
		}
		return NativeActionRef{}, err
	}
	if source != claim.source {
		return NativeActionRef{}, foundation.ErrStaleLease
	}
	var mode string
	var epoch int64
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&mode, &epoch); err != nil {
		return NativeActionRef{}, unavailable(err)
	}
	if (mode != "ai" && mode != "staff") || epoch < 0 {
		return NativeActionRef{}, ErrStoredData
	}
	binding := postgres.MenuExecutionBinding{EventUUID: source.eventUUID, RestaurantID: source.restaurant, ConversationID: source.conversation.value, Business: source.business, Sender: source.sender, AdmissionMode: claim.mode, AdmissionVersion: claim.version}
	clock, err := postgres.LockMenuExecutionJob(ctx, tx, binding, claim.job)
	if err != nil {
		return NativeActionRef{}, cartClaimError(err)
	}
	if err = clock(); err != nil {
		return NativeActionRef{}, cartClaimError(err)
	}
	if mode != "ai" || epoch != claim.version {
		return NativeActionRef{}, ErrNativeAdmissionClosed
	}
	// The source lock serializes both registration paths. A legacy slot may
	// never acquire admission, even when its intent happens to be identical.
	var occupied, admitted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2),EXISTS(SELECT 1 FROM app.native_cart_actions WHERE event_id=$1 AND runtime_slot=$2)`, source.eventUUID, slot).Scan(&occupied, &admitted); err != nil {
		return NativeActionRef{}, ErrUnavailable
	}
	if occupied && !admitted {
		return NativeActionRef{}, ErrConflict
	}
	if admitted && !occupied {
		return NativeActionRef{}, ErrStoredData
	}
	validated, body, err = s.linkNativeAdmission(ctx, tx, local, claim, slot, validated, body, admitted, resolution)
	if err != nil {
		return NativeActionRef{}, err
	}
	if !admitted {
		snapshot, err := s.read(ctx, tx, local, CartID{validated.req.Cart})
		if err != nil {
			return NativeActionRef{}, err
		}
		if snapshot.Version.value != validated.req.Expected || validated.req.Expected == math.MaxInt64 {
			return NativeActionRef{}, ErrStale
		}
		if err = s.validateNativeEdit(ctx, tx, snapshot, validated); err != nil {
			return NativeActionRef{}, err
		}
	}
	execution, err := s.registerExecutionTx(ctx, tx, local, source, slot, validated, body)
	if err != nil {
		return NativeActionRef{}, err
	}
	if admitted {
		var operation, restaurant, conversation, job string
		var version int64
		if err = tx.QueryRow(ctx, `SELECT operation_id::text,restaurant_id::text,conversation_id::text,admission_job_id::text,admission_version FROM app.native_cart_actions WHERE event_id=$1 AND runtime_slot=$2 FOR UPDATE`, source.eventUUID, slot).Scan(&operation, &restaurant, &conversation, &job, &version); err != nil {
			return NativeActionRef{}, ErrUnavailable
		}
		if operation != execution.operation.value || restaurant != s.restaurant || conversation != local.conversation.value || job != claim.job.ID || version != claim.version {
			return NativeActionRef{}, ErrStoredData
		}
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO app.native_cart_actions(event_id,runtime_slot,operation_id,restaurant_id,conversation_id,admission_job_id,admission_version,admission_generation,admission_attempt,admitted_at,due_at,unresolved_operation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,clock_timestamp(),clock_timestamp(),$10)`, source.eventUUID, slot, execution.operation.value, s.restaurant, local.conversation.value, claim.job.ID, claim.version, claim.job.Generation, claim.job.Attempt, nativeResolutionOperation(validated))
		if err != nil {
			return NativeActionRef{}, ErrUnavailable
		}
	}
	if err = clock(); err != nil {
		return NativeActionRef{}, cartClaimError(err)
	}
	if err = sourceContextError(ctx); err != nil {
		return NativeActionRef{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeActionRef{}, ErrUnavailable
	}
	return NativeActionRef{source: source, slot: slot, operation: execution.operation}, nil
}

// Validate domain eligibility without writing units, operations or cart version.
// Execution must revalidate these mutable facts before any later mutation.
func (s *Store) validateNativeEdit(ctx context.Context, tx pgx.Tx, snapshot Snapshot, call Call) error {
	switch action := call.action.(type) {
	case addAction:
		var price int64
		var currency string
		var exponent int16
		var available bool
		if err := tx.QueryRow(ctx, `SELECT m.price_minor,m.currency,s.currency_exponent,m.available FROM app.menu_items m JOIN app.restaurant_settings s ON s.restaurant_id=m.restaurant_id WHERE m.restaurant_id=$1 AND m.id=$2 FOR SHARE OF m,s`, s.restaurant, action.menu.value).Scan(&price, &currency, &exponent, &available); err != nil {
			return unavailable(err)
		}
		if price < 0 || currency != snapshot.Currency || exponent != snapshot.Exponent {
			return ErrStoredData
		}
		if !available {
			return ErrUnavailableItem
		}
		if action.count > int64(resultBudget/72-len(snapshot.Units)) || (price > 0 && action.count > (math.MaxInt64-snapshot.Subtotal)/price) {
			return ErrLimit
		}
		prospective := snapshot
		prospective.Units = make([]Unit, len(snapshot.Units), len(snapshot.Units)+int(action.count))
		copy(prospective.Units, snapshot.Units)
		for range action.count {
			prospective.Units = append(prospective.Units, Unit{MenuID: action.menu, PriceMinor: price, Currency: currency, Exponent: exponent})
		}
		// All UUIDs encode at the same width. Distinct scratch IDs satisfy the
		// codec without allocating authoritative identities or touching storage.
		for i := range prospective.Units {
			prospective.Units[i].ID = UnitID{value: fmt.Sprintf("00000000-0000-4000-8000-%012x", i)}
		}
		prospective.Subtotal += price * action.count
		prospective.Version = Version{value: snapshot.Version.value + 1}
		_, err := encodeResult(Result{OperationID: OperationID{value: "00000000-0000-4000-8000-000000000000"}, Cart: prospective})
		return err
	case removeAction:
		_, err := editTargets(snapshot, action.units, "", true)
		return err
	case noteAction:
		edited, err := editTargets(snapshot, action.units, action.note, false)
		if err != nil {
			return err
		}
		_, err = encodeResult(Result{OperationID: OperationID{value: "00000000-0000-4000-8000-000000000000"}, Cart: edited})
		return err
	default:
		return ErrInput
	}
}
