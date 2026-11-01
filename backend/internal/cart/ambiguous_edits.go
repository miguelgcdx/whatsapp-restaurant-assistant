package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// ErrUnresolvedEdit blocks new pickup material while a clarification is active.
var ErrUnresolvedEdit = errors.New("cart edit target requires clarification")

// UnresolvedEditKind names the requested supported edit, never its target.
// Zero and unknown values are rejected at the constructor boundary.
type UnresolvedEditKind string

const (
	UnresolvedAdd     UnresolvedEditKind = "add"
	UnresolvedRemove  UnresolvedEditKind = "remove"
	UnresolvedSetNote UnresolvedEditKind = "set_note"
)

// PendingEditRef is opaque, comparable server metadata, not bearer authority.
// It has no parser or exported identity fields.
type PendingEditRef struct {
	source    Source
	slot      int64
	operation OperationID
	cart      CartID
}

// PendingEdit displays an ACTIVE record's exact stored original request.
type PendingEdit struct {
	Ref     PendingEditRef
	Kind    UnresolvedEditKind
	Request string
}

// PendingEditView binds the COMPLETE bounded ACTIVE set to its actual reading
// source/control/cart version. The host retains the exact per-call selector/ref
// mapping separately. Its zero value is invalid.
type PendingEditView struct {
	source  Source
	cart    CartID
	version Version
	control int64
	digest  [32]byte
	entries []PendingEdit
}

// Entries returns detached presentation copies, not a mutable backend binding.
func (v PendingEditView) Entries() []PendingEdit { return append([]PendingEdit(nil), v.entries...) }

// PendingEditViewLimit bounds the complete view; overflow is never truncated.
const PendingEditViewLimit = 64

// beginAmbiguousEdit reuses ownership, stored source binding and actual menu job
// authority in the established order. Neither returned refs nor caller clocks
// replace these locks. The caller owns rollback/commit and final guard checks.
func (s *Store) beginAmbiguousEdit(ctx context.Context, local LocalContext, claim MenuClaim) (pgx.Tx, authorityGuard, error) {
	if !local.valid() || local.restaurant != s.restaurant {
		return nil, nil, ErrInput
	}
	if claim == (MenuClaim{}) || claim.source.restaurant != local.restaurant || claim.source.conversation != local.conversation || claim.source.business != local.business || claim.source.sender != local.sender || claim.mode != "ai" || claim.version < 0 {
		return nil, nil, foundation.ErrStaleLease
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (pgx.Tx, authorityGuard, error) { rollbackNative(tx); return nil, nil, err }
	source, err := s.bindSourceTx(ctx, tx, local, claim.source.eventProviderID)
	if err != nil {
		if err == ErrConflict || err == ErrNotFound {
			err = foundation.ErrStaleLease
		}
		return fail(err)
	}
	if source != claim.source {
		return fail(foundation.ErrStaleLease)
	}
	var mode string
	var epoch int64
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&mode, &epoch); err != nil {
		return fail(unavailable(err))
	}
	if (mode != "ai" && mode != "staff") || epoch < 0 {
		return fail(ErrStoredData)
	}
	clock, err := postgres.LockMenuExecutionJob(ctx, tx, postgres.MenuExecutionBinding{EventUUID: source.eventUUID, RestaurantID: source.restaurant, ConversationID: source.conversation.value, Business: source.business, Sender: source.sender, AdmissionMode: claim.mode, AdmissionVersion: claim.version}, claim.job)
	if err != nil {
		return fail(cartClaimError(err))
	}
	guard := func() error {
		if err := clock(); err != nil {
			return cartClaimError(err)
		}
		return sourceContextError(ctx)
	}
	if err = guard(); err != nil {
		return fail(err)
	}
	if mode != "ai" || epoch != claim.version {
		return fail(ErrNativeAdmissionClosed)
	}
	return tx, guard, nil
}

// MarkUnresolvedEdit persists one immutable registered marker, taking the exact
// original request from storage. Exact replay never reopens a resolved marker.
func (s *Store) MarkUnresolvedEdit(parent context.Context, local LocalContext, claim MenuClaim, hostSlot int64, id CartID, expected Version, kind UnresolvedEditKind) (out PendingEditRef, err error) {
	call, err := UnresolvedEditCall(id, expected, kind)
	if err != nil || hostSlot < 0 {
		return PendingEditRef{}, ErrInput
	}
	ctx, cancel, err := s.nativeContext(parent)
	if err != nil {
		return PendingEditRef{}, err
	}
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out = PendingEditRef{}
		}
	}()
	tx, guard, err := s.beginAmbiguousEdit(ctx, local, claim)
	if err != nil {
		return PendingEditRef{}, err
	}
	defer rollbackNative(tx)
	var occupied, marked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2),EXISTS(SELECT 1 FROM app.unresolved_cart_edits WHERE event_id=$1 AND runtime_slot=$2)`, claim.source.eventUUID, hostSlot).Scan(&occupied, &marked); err != nil {
		return PendingEditRef{}, ErrUnavailable
	}
	if occupied && !marked {
		return PendingEditRef{}, ErrConflict
	}
	if marked && !occupied {
		return PendingEditRef{}, ErrStoredData
	}
	if !marked {
		snapshot, err := s.read(ctx, tx, local, id)
		if err != nil {
			return PendingEditRef{}, err
		}
		if snapshot.Version != expected {
			return PendingEditRef{}, ErrStale
		}
	}
	body, err := encodeCall(call)
	if err != nil {
		return PendingEditRef{}, ErrInput
	}
	execution, err := s.registerExecutionTx(ctx, tx, local, claim.source, hostSlot, call, body)
	if err != nil {
		return PendingEditRef{}, err
	}
	out = PendingEditRef{claim.source, hostSlot, execution.operation, id}
	if !marked {
		// No supplied prose: even whitespace and Unicode come from the source.
		_, err = tx.Exec(ctx, `INSERT INTO app.unresolved_cart_edits(event_id,runtime_slot,operation_id,restaurant_id,conversation_id,cart_id,expected_version,kind,original_request,admission_job_id,control_version,admission_generation,admission_attempt,created_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,content,$9,$10,$11,$12,clock_timestamp() FROM app.inbound_events WHERE id=$1 AND sender=$13`, claim.source.eventUUID, hostSlot, execution.operation.value, s.restaurant, local.conversation.value, id.value, expected.value, string(kind), claim.job.ID, claim.version, claim.job.Generation, claim.job.Attempt, local.sender)
		if err != nil {
			return PendingEditRef{}, ErrUnavailable
		}
	}
	record, err := s.loadPendingEdit(ctx, tx, local, execution.operation)
	if err != nil {
		return PendingEditRef{}, err
	}
	if record.entry.Ref != out {
		return PendingEditRef{}, ErrStoredData
	}
	if err = guard(); err != nil {
		return PendingEditRef{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PendingEditRef{}, ErrUnavailable
	}
	return out, nil
}

// pendingRecord's wire form includes private identity and provenance omitted
// from public JSON refs. It is also the exact digest input for pending views.
type pendingRecord struct {
	Event      string
	Slot       int64
	Operation  string
	Cart       string
	Expected   int64
	Kind       UnresolvedEditKind
	Request    string
	Job        string
	Control    int64
	Generation int64
	Attempt    int64
	Resolved   string
	entry      PendingEdit
}

// The restaurant lock already serializes domain writers. These joins read
// immutable provenance without acquiring a second source/job lock out of order.
func (s *Store) loadPendingEdit(ctx context.Context, tx pgx.Tx, local LocalContext, op OperationID) (pendingRecord, error) {
	var r pendingRecord
	var provider, sender, original, body, action, jobEvent, jobRestaurant, jobConversation, jobMode, jobRoute string
	var hash []byte
	var jobControl, jobGeneration, jobAttempt int64
	err := tx.QueryRow(ctx, `SELECT m.event_id::text,m.runtime_slot,m.operation_id::text,m.cart_id::text,m.expected_version,m.kind,m.original_request,m.admission_job_id::text,m.control_version,m.admission_generation,m.admission_attempt,COALESCE(m.resolved_operation::text,''),e.provider_event_id,e.sender,e.content,c.action,c.canonical_intent,c.fingerprint,j.event_id::text,j.restaurant_id::text,j.conversation_id::text,j.admission_mode,j.admission_version,j.generation,j.attempt,j.route FROM app.unresolved_cart_edits m JOIN app.cart_execution_calls c ON c.operation_id=m.operation_id AND c.event_id=m.event_id AND c.runtime_slot=m.runtime_slot AND c.restaurant_id=m.restaurant_id AND c.conversation_id=m.conversation_id JOIN app.cart_execution_sources b ON b.event_id=m.event_id AND b.restaurant_id=m.restaurant_id AND b.conversation_id=m.conversation_id JOIN app.inbound_events e ON e.id=m.event_id JOIN app.jobs j ON j.id=m.admission_job_id WHERE m.operation_id=$1 AND m.restaurant_id=$2 AND m.conversation_id=$3 FOR UPDATE OF m`, op.value, s.restaurant, local.conversation.value).Scan(&r.Event, &r.Slot, &r.Operation, &r.Cart, &r.Expected, &r.Kind, &r.Request, &r.Job, &r.Control, &r.Generation, &r.Attempt, &r.Resolved, &provider, &sender, &original, &action, &body, &hash, &jobEvent, &jobRestaurant, &jobConversation, &jobMode, &jobControl, &jobGeneration, &jobAttempt, &jobRoute)
	if err != nil {
		return pendingRecord{}, unavailable(err)
	}
	call, err := decodeCall(body)
	expectedHash := sha256.Sum256([]byte(body))
	if err != nil || call.unresolved == nil || action != unresolvedEditAction || !bytes.Equal(hash, expectedHash[:]) || call.unresolved.Cart != r.Cart || call.unresolved.Expected != r.Expected || call.unresolved.Kind != r.Kind || sender != local.sender || original != r.Request || !text(r.Request, 4000, true) || jobEvent != r.Event || jobRestaurant != s.restaurant || jobConversation != local.conversation.value || jobMode != "ai" || jobRoute != "menu_message" || jobControl != r.Control || jobGeneration < r.Generation || jobAttempt < r.Attempt {
		return pendingRecord{}, ErrStoredData
	}
	source := Source{restaurant: s.restaurant, conversation: local.conversation, business: local.business, sender: local.sender, eventUUID: r.Event, eventProviderID: provider}
	r.entry = PendingEdit{Ref: PendingEditRef{source, r.Slot, op, CartID{r.Cart}}, Kind: r.Kind, Request: r.Request}
	return r, nil
}

func (s *Store) pendingView(ctx context.Context, tx pgx.Tx, local LocalContext, claim MenuClaim, id CartID, expected Version) (PendingEditView, error) {
	snapshot, err := s.read(ctx, tx, local, id)
	if err != nil {
		return PendingEditView{}, err
	}
	if snapshot.Version != expected {
		return PendingEditView{}, ErrStale
	}
	rows, err := tx.Query(ctx, `SELECT operation_id::text FROM app.unresolved_cart_edits WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3 AND resolved_operation IS NULL ORDER BY event_id,runtime_slot LIMIT $4 FOR UPDATE`, s.restaurant, local.conversation.value, id.value, PendingEditViewLimit+1)
	if err != nil {
		return PendingEditView{}, ErrUnavailable
	}
	ops := []OperationID{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return PendingEditView{}, ErrUnavailable
		}
		op, e := ParseOperationID(raw)
		if e != nil {
			rows.Close()
			return PendingEditView{}, ErrStoredData
		}
		ops = append(ops, op)
	}
	rows.Close()
	if rows.Err() != nil {
		return PendingEditView{}, ErrUnavailable
	}
	if len(ops) > PendingEditViewLimit {
		return PendingEditView{}, ErrLimit
	}
	v := PendingEditView{source: claim.source, cart: id, version: expected, control: claim.version, entries: []PendingEdit{}}
	records := make([]pendingRecord, 0, len(ops))
	for _, op := range ops {
		r, err := s.loadPendingEdit(ctx, tx, local, op)
		if err != nil {
			return PendingEditView{}, err
		}
		if r.Resolved != "" || r.Cart != id.value {
			return PendingEditView{}, ErrStoredData
		}
		records = append(records, r)
		v.entries = append(v.entries, r.entry)
	}
	bound := struct {
		Restaurant, Conversation, Business, Sender, Source, Cart string
		Version, Control                                         int64
		Records                                                  []pendingRecord
	}{s.restaurant, local.conversation.value, local.business, local.sender, claim.source.eventUUID, id.value, expected.value, claim.version, records}
	body, err := json.Marshal(bound)
	if err != nil {
		return PendingEditView{}, ErrStoredData
	}
	if len(body) > resultBudget {
		return PendingEditView{}, ErrLimit
	}
	v.digest = sha256.Sum256(body)
	return v, nil
}

// ReadPendingEdits checks current source/claim/control and reads every ACTIVE
// scoped record within the count/byte budget. It never allocates an operation.
func (s *Store) ReadPendingEdits(parent context.Context, local LocalContext, claim MenuClaim, id CartID, expected Version) (out PendingEditView, err error) {
	if !validID(id.value) || expected.value < 0 {
		return PendingEditView{}, ErrInput
	}
	ctx, cancel, err := s.nativeContext(parent)
	if err != nil {
		return PendingEditView{}, err
	}
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out = PendingEditView{}
		}
	}()
	tx, guard, err := s.beginAmbiguousEdit(ctx, local, claim)
	if err != nil {
		return PendingEditView{}, err
	}
	defer rollbackNative(tx)
	out, err = s.pendingView(ctx, tx, local, claim, id, expected)
	if err != nil {
		return PendingEditView{}, err
	}
	if err = guard(); err != nil {
		return PendingEditView{}, err
	}
	return out, nil
}

func (s *Store) guardUnresolvedEdits(ctx context.Context, tx pgx.Tx, local LocalContext, id CartID) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.unresolved_cart_edits WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3 AND resolved_operation IS NULL)`, s.restaurant, local.conversation.value, id.value).Scan(&active); err != nil {
		return ErrUnavailable
	}
	if active {
		return ErrUnresolvedEdit
	}
	return nil
}
