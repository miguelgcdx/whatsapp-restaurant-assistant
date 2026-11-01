package cart

import (
	"context"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
)

// AdmitResolvedNativeEdit explicitly links one ACTIVE record from the retained
// exact view to a concrete matching edit. Admission is not resolution; the
// existing native business lease/application transaction owns that transition.
func (s *Store) AdmitResolvedNativeEdit(ctx context.Context, local LocalContext, claim MenuClaim, hostSlot int64, view PendingEditView, ref PendingEditRef, edit Call) (NativeActionRef, error) {
	return s.admitNativeEdit(ctx, local, claim, hostSlot, edit, &resolvedAdmission{view, ref})
}

type resolvedAdmission struct {
	view PendingEditView
	ref  PendingEditRef
}

func (r resolvedAdmission) binding() resolutionBinding {
	return resolutionBinding{r.ref.source.eventUUID, r.ref.slot, r.ref.operation.value, r.view.source.eventUUID, r.view.control, hex.EncodeToString(r.view.digest[:])}
}

func nativeResolutionOperation(call Call) any {
	if call.resolution == nil {
		return nil
	}
	return call.resolution.MarkerOperation
}

// Called inside existing native admission AFTER actual source/control/job
// authority and slot discovery, BEFORE fresh eligibility or registration.
// Occupied intent is immutable: replay precedes mutable view/status checks.
func (s *Store) linkNativeAdmission(ctx context.Context, tx pgx.Tx, local LocalContext, claim MenuClaim, slot int64, call Call, body []byte, admitted bool, resolution *resolvedAdmission) (Call, []byte, error) {
	if admitted {
		var storedBody, linked string
		if err := tx.QueryRow(ctx, `SELECT c.canonical_intent,COALESCE(n.unresolved_operation::text,'') FROM app.cart_execution_calls c JOIN app.native_cart_actions n ON n.operation_id=c.operation_id AND n.event_id=c.event_id AND n.runtime_slot=c.runtime_slot WHERE c.event_id=$1 AND c.runtime_slot=$2`, claim.source.eventUUID, slot).Scan(&storedBody, &linked); err != nil {
			return Call{}, nil, ErrUnavailable
		}
		stored, err := decodeCall(storedBody)
		if err != nil {
			return Call{}, nil, ErrStoredData
		}
		if (stored.resolution == nil && linked != "") || (stored.resolution != nil && linked != stored.resolution.MarkerOperation) {
			return Call{}, nil, ErrStoredData
		}
		if resolution != nil {
			binding := resolution.binding()
			if stored.resolution == nil || *stored.resolution != binding {
				return Call{}, nil, ErrConflict
			}
			call.resolution = &binding
			body, err = encodeCall(call)
			if err != nil {
				return Call{}, nil, ErrInput
			}
		}
		// registerExecutionTx compares complete canonical bytes/fingerprint,
		// including the concrete edit, for both linked and ordinary replays.
		return call, body, nil
	}
	if resolution == nil {
		return call, body, nil
	}
	view, ref := resolution.view, resolution.ref
	if view.digest == ([32]byte{}) || ref == (PendingEditRef{}) {
		return Call{}, nil, ErrInput
	}
	if view.source != claim.source || view.control != claim.version {
		return Call{}, nil, ErrStale
	}
	if view.cart.value != call.req.Cart || ref.cart != view.cart {
		return Call{}, nil, ErrInput
	}
	if view.version.value != call.req.Expected {
		return Call{}, nil, ErrStale
	}
	current, err := s.pendingView(ctx, tx, local, claim, view.cart, view.version)
	if err != nil {
		// A formerly complete view cannot authorize a now oversized set.
		if err == ErrLimit {
			err = ErrStale
		}
		return Call{}, nil, err
	}
	if current.digest != view.digest || len(current.entries) != len(view.entries) {
		return Call{}, nil, ErrStale
	}
	var selected *PendingEdit
	for i, entry := range current.entries {
		if entry != view.entries[i] {
			return Call{}, nil, ErrStale
		}
		if entry.Ref == ref {
			e := entry
			selected = &e
		}
	}
	if selected == nil {
		return Call{}, nil, ErrNotFound
	}
	if string(selected.Kind) != call.req.Action {
		return Call{}, nil, ErrInput
	}
	binding := resolution.binding()
	if !binding.valid() {
		return Call{}, nil, ErrInput
	}
	call.resolution = &binding
	body, err = encodeCall(call)
	if err != nil || len(body) > resultBudget {
		return Call{}, nil, ErrInput
	}
	if _, err = decodeCall(string(body)); err != nil {
		return Call{}, nil, ErrInput
	}
	return call, body, nil
}

// Stage in the existing application's SQL transaction, never a second commit.
// The deferred result FK makes a committed RESOLVED marker require the scoped
// cart result; domain/version/final-lease failure rolls this update back with it.
// Native replay validates the original resolution but does not transition again.
func (s *Store) stageResolvedNativeEdit(ctx context.Context, tx pgx.Tx, local LocalContext, ref NativeActionRef, call Call, replay bool) error {
	var linked string
	var control int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(unresolved_operation::text,''),admission_version FROM app.native_cart_actions WHERE event_id=$1 AND runtime_slot=$2 AND operation_id=$3`, ref.source.eventUUID, ref.slot, ref.operation.value).Scan(&linked, &control); err != nil {
		return ErrUnavailable
	}
	if call.resolution == nil {
		if linked != "" {
			return ErrStoredData
		}
		return nil
	}
	b := call.resolution
	if linked != b.MarkerOperation || b.ViewSource != ref.source.eventUUID || b.ViewControl != control {
		return ErrStoredData
	}
	op, err := ParseOperationID(linked)
	if err != nil {
		return ErrStoredData
	}
	record, err := s.loadPendingEdit(ctx, tx, local, op)
	if err != nil {
		return err
	}
	if record.Event != b.MarkerEvent || record.Slot != b.MarkerSlot || record.Cart != call.req.Cart || string(record.Kind) != call.req.Action {
		return ErrStoredData
	}
	if replay {
		if record.Resolved != ref.operation.value {
			return ErrStoredData
		}
		return nil
	}
	if record.Resolved != "" {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE app.unresolved_cart_edits SET resolved_operation=$1,resolved_at=clock_timestamp() WHERE operation_id=$2 AND restaurant_id=$3 AND conversation_id=$4 AND resolved_operation IS NULL`, ref.operation.value, linked, s.restaurant, local.conversation.value)
	if err != nil {
		return ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
