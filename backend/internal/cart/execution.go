package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// Call is a closed native descriptor. Parameters contain no actor, source,
// runtime slot, claim, or operation nonce. Its zero value is invalid.
type Call struct {
	req     request
	action  Action
	draft   *draftRequest
	preview *summaryPreviewRequest
}

func CreateCall() Call { return Call{req: request{Action: "create", Targets: []string{}}} }
func ReadCall(id CartID) (Call, error) {
	if !validID(id.value) {
		return Call{}, ErrInput
	}
	return Call{req: request{Action: "read", Cart: id.value, Targets: []string{}}}, nil
}

func EditCall(id CartID, v Version, a Action) (Call, error) {
	if !validID(id.value) || v.value < 0 {
		return Call{}, ErrInput
	}
	req, err := canonical(id, v, a)
	if err != nil {
		return Call{}, err
	}
	return Call{req: req, action: a}, nil
}

// Execution is an opaque registered plan handle, NOT a claim or customer
// credential. The server-issued operation identity survives crash and reclaim.
type Execution struct {
	event     string
	slot      int64
	operation OperationID
}

func (e Execution) OperationID() OperationID { return e.operation }

// RegisterExecution explicitly binds an already persisted event to ONE freshly
// validated local context and an immutable slot intent. No mutation is executed
// or committed-result identity reserved by registration.
func (s *Store) RegisterExecution(ctx context.Context, local LocalContext, event string, slot int64, call Call) (Execution, error) {
	if slot < 0 || !text(event, 128, true) {
		return Execution{}, ErrInput
	}
	body, err := encodeCall(call)
	if err != nil || len(body) > resultBudget {
		return Execution{}, ErrInput
	}
	if _, err = decodeCall(string(body)); err != nil {
		return Execution{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Execution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	source, err := s.bindSourceTx(ctx, tx, local, event)
	if err != nil {
		return Execution{}, err
	}
	eid := source.eventUUID
	// The whole-event source lock serializes registration, so allocate an
	// operation UUID only for a genuinely new slot, never for retry/reclaim.
	hash := sha256.Sum256(body)
	var storedBody, storedAction, storedOp string
	var storedHash []byte
	err = tx.QueryRow(ctx, `SELECT action,canonical_intent,fingerprint,operation_id::text FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2 FOR UPDATE`, eid, slot).Scan(&storedAction, &storedBody, &storedHash, &storedOp)
	var op OperationID
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		op, err = NewOperationID()
		if err != nil {
			return Execution{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, eid, slot, s.restaurant, local.conversation.value, call.req.Action, string(body), hash[:], op.value, time.Now().UTC())
		if err != nil {
			return Execution{}, ErrUnavailable
		}
	case err != nil:
		return Execution{}, unavailable(err)
	default:
		if storedAction != call.req.Action || storedBody != string(body) || !bytes.Equal(hash[:], storedHash) {
			return Execution{}, ErrConflict
		}
		op, err = ParseOperationID(storedOp)
		if err != nil {
			return Execution{}, ErrStoredData
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Execution{}, ErrUnavailable
	}
	return Execution{eid, slot, op}, nil
}

// decodeCall validates persisted intent with the SAME public constructors and
// canonicalizer as direct cart operations. Canonical byte equality rejects
// unknown fields, extraneous parameters, alternate encodings and invalid data.
func decodeCall(body string) (Call, error) {
	if len(body) == 0 || len(body) > resultBudget {
		return Call{}, ErrStoredData
	}
	var kind struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(body), &kind) != nil {
		return Call{}, ErrStoredData
	}
	if kind.Action == "capture_preview" || kind.Action == "read_preview" {
		return decodeSummaryPreviewCall(body)
	}
	if kind.Action == "set_fulfillment" || kind.Action == "quote" {
		return decodeDraftCall(body)
	}
	var r request
	if json.Unmarshal([]byte(body), &r) != nil {
		return Call{}, ErrStoredData
	}
	var call Call
	var err error
	switch r.Action {
	case "create":
		call = CreateCall()
	case "read":
		id, e := ParseCartID(r.Cart)
		if e != nil {
			return Call{}, ErrStoredData
		}
		call, err = ReadCall(id)
	case "add", "remove", "set_note":
		id, e := ParseCartID(r.Cart)
		if e != nil {
			return Call{}, ErrStoredData
		}
		v, e := ParseVersion(r.Expected)
		if e != nil {
			return Call{}, ErrStoredData
		}
		var a Action
		switch r.Action {
		case "add":
			m, e := ParseMenuID(r.Menu)
			if e != nil {
				return Call{}, ErrStoredData
			}
			a, err = Add(m, r.Count)
		case "remove", "set_note":
			ids := make([]UnitID, 0, len(r.Targets))
			for _, raw := range r.Targets {
				uid, e := ParseUnitID(raw)
				if e != nil {
					return Call{}, ErrStoredData
				}
				ids = append(ids, uid)
			}
			if r.Action == "remove" {
				a, err = Remove(ids)
			} else {
				a, err = SetNote(ids, r.Note)
			}
		}
		if err == nil {
			call, err = EditCall(id, v, a)
		}
	default:
		return Call{}, ErrStoredData
	}
	encoded, e := json.Marshal(call.req)
	if err != nil || e != nil || string(encoded) != body {
		return Call{}, ErrStoredData
	}
	return call, nil
}

// Execute reloads actual job/event authority in the cart transaction. The lock
// order is restaurant -> conversation -> source/call -> job -> cart/resources.
// No caller time or mutable claim field alone can grant access to saved output.
func (s *Store) Execute(ctx context.Context, local LocalContext, execution Execution, job foundation.Job) (Result, error) {
	if !validID(execution.event) || execution.slot < 0 || !validID(execution.operation.value) {
		return Result{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	call, guard, err := s.loadExecution(ctx, tx, local, execution, job)
	if err != nil {
		return Result{}, err
	}
	if call.draft != nil || call.preview != nil {
		return Result{}, ErrInput
	}
	switch call.req.Action {
	case "create":
		return s.create(ctx, tx, local, execution.operation, guard)
	case "read":
		snapshot, e := s.read(ctx, tx, local, CartID{call.req.Cart})
		if e != nil {
			return Result{}, e
		}
		if e = guard(); e != nil {
			return Result{}, e
		}
		return Result{execution.operation, snapshot}, nil
	default:
		return s.apply(ctx, tx, local, execution.operation, CartID{call.req.Cart}, Version{call.req.Expected}, call.action, call.req, guard)
	}
}

// loadExecution is shared by all result seams: canonical plan and actual job
// authority are always loaded under the same locks in the cart transaction.
func (s *Store) loadExecution(ctx context.Context, tx pgx.Tx, local LocalContext, execution Execution, job foundation.Job) (Call, authorityGuard, error) {
	var restaurant, conversation string
	if err := tx.QueryRow(ctx, `SELECT restaurant_id::text,conversation_id::text FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`, execution.event).Scan(&restaurant, &conversation); err != nil {
		return Call{}, nil, unavailable(err)
	}
	if restaurant != s.restaurant || conversation != local.conversation.value {
		return Call{}, nil, ErrNotFound
	}
	var body, action, op string
	var hash []byte
	if err := tx.QueryRow(ctx, `SELECT action,canonical_intent,fingerprint,operation_id::text FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2 AND restaurant_id=$3 AND conversation_id=$4 FOR UPDATE`, execution.event, execution.slot, s.restaurant, local.conversation.value).Scan(&action, &body, &hash, &op); err != nil {
		return Call{}, nil, unavailable(err)
	}
	if op != execution.operation.value {
		return Call{}, nil, ErrNotFound
	}
	call, err := decodeCall(body)
	if err != nil {
		return Call{}, nil, err
	}
	expected := sha256.Sum256([]byte(body))
	if action != call.req.Action || !bytes.Equal(hash, expected[:]) {
		return Call{}, nil, ErrStoredData
	}
	guard, err := lockExecutionJob(ctx, tx, execution.event, local.sender, job)
	if err != nil {
		return Call{}, nil, err
	}
	// Check current authority before any cached business output.
	if err = guard(); err != nil {
		return Call{}, nil, err
	}
	return call, guard, nil
}

func lockExecutionJob(ctx context.Context, tx pgx.Tx, event, sender string, job foundation.Job) (authorityGuard, error) {
	guard, err := postgres.LockExecutionJob(ctx, tx, event, sender, job)
	if err != nil {
		return nil, cartClaimError(err)
	}
	return func() error { return cartClaimError(guard()) }, nil
}

func cartClaimError(err error) error {
	if errors.Is(err, postgres.ErrClaimUnavailable) {
		return ErrUnavailable
	}
	return err
}
