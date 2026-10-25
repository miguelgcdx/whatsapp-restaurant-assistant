package cart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

// Call is a closed native descriptor. Parameters contain no actor, source,
// runtime slot, claim, or operation nonce. Its zero value is invalid.
type Call struct {
	req    request
	action Action
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
	body, err := json.Marshal(call.req)
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
	var eid, sender string
	if err = tx.QueryRow(ctx, `SELECT id::text,sender FROM app.inbound_events WHERE provider_event_id=$1 FOR SHARE`, event).Scan(&eid, &sender); err != nil {
		return Execution{}, unavailable(err)
	}
	var restaurant, conversation string
	loadSource := func() error {
		return tx.QueryRow(ctx, `SELECT restaurant_id::text,conversation_id::text FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`, eid).Scan(&restaurant, &conversation)
	}
	err = loadSource()
	if errors.Is(err, pgx.ErrNoRows) {
		// Validate sender before creating ANY setup state. Existing bindings
		// are checked first so every attempted context rebind is a conflict.
		if sender != local.sender {
			return Execution{}, ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO app.cart_execution_sources(event_id,restaurant_id,conversation_id,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(event_id) DO NOTHING`, eid, s.restaurant, local.conversation.value, time.Now().UTC())
		if err != nil {
			return Execution{}, ErrUnavailable
		}
		// Another context may have registered the event while INSERT waited.
		err = loadSource()
	}
	if err != nil {
		return Execution{}, unavailable(err)
	}
	if restaurant != s.restaurant || conversation != local.conversation.value {
		return Execution{}, ErrConflict
	}
	if sender != local.sender {
		return Execution{}, ErrNotFound
	}
	// The whole-event source lock serializes registration, so allocate an
	// operation UUID only for a genuinely new slot, never for retry/reclaim.
	hash := fingerprint(call.req)
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
	var restaurant, conversation string
	if err = tx.QueryRow(ctx, `SELECT restaurant_id::text,conversation_id::text FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`, execution.event).Scan(&restaurant, &conversation); err != nil {
		return Result{}, unavailable(err)
	}
	if restaurant != s.restaurant || conversation != local.conversation.value {
		return Result{}, ErrNotFound
	}
	var body, action, op string
	var hash []byte
	if err = tx.QueryRow(ctx, `SELECT action,canonical_intent,fingerprint,operation_id::text FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2 AND restaurant_id=$3 AND conversation_id=$4 FOR UPDATE`, execution.event, execution.slot, s.restaurant, local.conversation.value).Scan(&action, &body, &hash, &op); err != nil {
		return Result{}, unavailable(err)
	}
	if op != execution.operation.value {
		return Result{}, ErrNotFound
	}
	call, err := decodeCall(body)
	if err != nil {
		return Result{}, err
	}
	expected := fingerprint(call.req)
	if action != call.req.Action || !bytes.Equal(hash, expected[:]) {
		return Result{}, ErrStoredData
	}
	guard, err := lockExecutionJob(ctx, tx, execution.event, local.sender, job)
	if err != nil {
		return Result{}, err
	}
	// An initial check handles already terminal/stale claims before even querying
	// cached business output; later guards cover time spent in blocking statements.
	if err = guard(); err != nil {
		return Result{}, err
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
		// Read holds locks until rollback at return; no result ledger or commit.
		return Result{execution.operation, snapshot}, nil
	default:
		return s.apply(ctx, tx, local, execution.operation, CartID{call.req.Cart}, Version{call.req.Expected}, call.action, call.req, guard)
	}
}

func lockExecutionJob(ctx context.Context, tx pgx.Tx, event, sender string, job foundation.Job) (authorityGuard, error) {
	if !validID(job.ID) || job.Generation <= 0 || job.Attempt <= 0 {
		return nil, foundation.ErrStaleLease
	}
	var id, eid, state, provider, storedSender, content string
	var generation int64
	var attempt int32
	var deadline pgtype.Timestamptz
	err := tx.QueryRow(ctx, `SELECT j.id::text,j.event_id::text,j.state,j.generation,j.attempt,j.lease_until,e.provider_event_id,e.sender,e.content FROM app.jobs j JOIN app.inbound_events e ON e.id=j.event_id WHERE j.id=$1 FOR UPDATE OF j FOR SHARE OF e`, job.ID).Scan(&id, &eid, &state, &generation, &attempt, &deadline, &provider, &storedSender, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, foundation.ErrStaleLease
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if id != job.ID || eid != event || state != "leased" || generation != job.Generation || attempt != job.Attempt || provider != job.Event.ID || storedSender != sender || storedSender != job.Event.Sender || content != job.Event.Text || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite {
		return nil, foundation.ErrStaleLease
	}
	return func() error {
		// Separate statement AFTER blocking locks/statements. Transaction-start now()
		// and SELECT expressions evaluated before LockRows are not authority clocks.
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return ErrUnavailable
		}
		if !deadline.Time.After(now) {
			return foundation.ErrStaleLease
		}
		return nil
	}, nil
}
