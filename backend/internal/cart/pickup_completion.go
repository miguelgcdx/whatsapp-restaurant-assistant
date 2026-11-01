package cart

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/pickupintent"
)

// AssessAndSubmit is the trusted LOCAL orchestration boundary. runtimeSlot is an
// explicit nonnegative host-owned registration slot, not model input. The caller
// supplies no summary, history, candidate, digest, decision or evidence metadata.
// The assessor must be constructed with pickupintent.New and trusted configuration.
// No model call runs inside a database transaction. Submit retains ownership of
// atomic persistence, replay validation and current source/claim/control fences.
func (p *PickupStore) AssessAndSubmit(ctx context.Context, execution PickupExecution, runtimeSlot int64, assessor pickupintent.Assessor) (PickupSubmission, error) {
	if ctx == nil || p == nil || p.cart == nil || p.cart.pool == nil || p.assessment != PickupCompletionAssessmentVersion || runtimeSlot < 0 || nilPickupAssessor(assessor) {
		return PickupSubmission{}, ErrInput
	}
	if err := sourceContextError(ctx); err != nil {
		return PickupSubmission{}, err
	}
	// A committed intent must replay before loading today's cart/history or
	// assessing again. The descriptor is identity, never permission: Submit
	// reloads it and checks actual current authority before releasing the result.
	registered, evidence, found, err := p.pickupCompletionReplay(ctx, execution, runtimeSlot)
	if err != nil {
		return PickupSubmission{}, err
	}
	if found {
		return p.Submit(ctx, registered, evidence)
	}
	trusted, err := p.CompletionContext(ctx, execution)
	if err != nil {
		return PickupSubmission{}, err
	}
	input, err := pickupCompletionInput(trusted)
	if err != nil {
		return PickupSubmission{}, err
	}
	decision, err := assessor.Assess(ctx, input)
	if err != nil {
		return PickupSubmission{}, err
	}
	if err := sourceContextError(ctx); err != nil {
		return PickupSubmission{}, err
	}
	switch decision {
	case pickupintent.NotComplete:
		return PickupSubmission{}, ErrPickupConfirmationRequired
	case pickupintent.Complete:
		// Only this exact semantic proposal can reach registration.
	default:
		return PickupSubmission{}, commandcode.ErrProtocol
	}
	// Refuse a stale assessment before allocating its registered operation.
	// Submit repeats all fences atomically; this read is not commit authority.
	current, err := p.CompletionContext(ctx, execution)
	if err != nil {
		return PickupSubmission{}, err
	}
	if current.Digest != trusted.Digest {
		return PickupSubmission{}, ErrPickupConfirmationRequired
	}
	evidence = PickupCompletionEvidence{
		CandidateID:       trusted.CandidateID,
		InboundEventID:    trusted.InboundEventID,
		ContextDigest:     trusted.Digest,
		AssessmentVersion: p.assessment,
		Decision:          PickupComplete,
	}
	call, err := SubmitPickupCall(evidence)
	if err != nil {
		return PickupSubmission{}, err
	}
	execution.Execution, err = p.cart.RegisterExecution(ctx, execution.Local, execution.Source.EventProviderID(), runtimeSlot, call)
	if err != nil {
		return PickupSubmission{}, err
	}
	return p.Submit(ctx, execution, evidence)
}

// An interface containing a nil concrete assessor is not itself nil. Check only
// nil-capable kinds; configuration/protocol validation remains assessor-owned.
func nilPickupAssessor(assessor pickupintent.Assessor) bool {
	if assessor == nil {
		return true
	}
	value := reflect.ValueOf(assessor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func pickupCompletionInput(trusted PickupCompletionContext) (pickupintent.ScopedHistoryInput, error) {
	if trusted.History == nil {
		return pickupintent.ScopedHistoryInput{}, ErrStoredData
	}
	input := pickupintent.ScopedHistoryInput{
		SummaryText:     trusted.SummaryText,
		ClosingQuestion: trusted.ClosingQuestion,
		InboundText:     trusted.InboundText,
		History:         make([]pickupintent.Message, len(trusted.History)),
	}
	for i, message := range trusted.History {
		var speaker pickupintent.Speaker
		switch message.Speaker {
		case PickupSpeakerCustomer:
			speaker = pickupintent.SpeakerCustomer
		case PickupSpeakerRestaurant:
			speaker = pickupintent.SpeakerRestaurant
		default:
			return pickupintent.ScopedHistoryInput{}, ErrStoredData
		}
		input.History[i] = pickupintent.Message{SourceID: message.SourceID, Speaker: speaker, Text: message.Text}
	}
	return input, nil
}

// pickupCompletionReplay only locates an existing committed registration. It
// never allocates an identity, writes state or returns cached business output.
// Canonical descriptor/ledger validation and actual execution authority use the
// existing domain primitives, with the same lock prefix as Submit. The short
// transaction ends before either assessment or the final Submit call.
func (p *PickupStore) pickupCompletionReplay(ctx context.Context, execution PickupExecution, runtimeSlot int64) (PickupExecution, PickupCompletionEvidence, bool, error) {
	tx, err := p.cart.begin(ctx, execution.Local)
	if err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, err
	}
	defer rollbackNative(tx)
	var operation string
	err = tx.QueryRow(ctx, `SELECT operation_id::text FROM app.cart_execution_calls WHERE restaurant_id=$1 AND conversation_id=$2 AND event_id=$3 AND runtime_slot=$4`, p.cart.restaurant, execution.Local.conversation.value, execution.Source.eventUUID, runtimeSlot).Scan(&operation)
	if errors.Is(err, pgx.ErrNoRows) {
		return PickupExecution{}, PickupCompletionEvidence{}, false, nil
	}
	if err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrUnavailable
	}
	op, err := ParseOperationID(operation)
	if err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrStoredData
	}
	execution.Execution = Execution{event: execution.Source.eventUUID, slot: runtimeSlot, operation: op}
	call, guard, err := p.pickupAuthority(ctx, tx, execution, true)
	if err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, err
	}
	if call.pickup == nil || call.req.Action != pickupSubmitAction {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrConflict
	}
	_, found, err := p.ledger(ctx, tx, execution, call)
	if err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, err
	}
	if !found {
		return PickupExecution{}, PickupCompletionEvidence{}, false, nil
	}
	// loadRegisteredExecution already validated canonical bytes through the
	// shared codec. Decode the references explicitly, without unchecked casts.
	stored := call.pickup
	var candidate PickupCandidateID
	if candidate.UnmarshalText([]byte(stored.Candidate)) != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrStoredData
	}
	digest, err := hex.DecodeString(stored.ContextDigest)
	if err != nil || len(digest) != 32 {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrStoredData
	}
	var hash [32]byte
	copy(hash[:], digest)
	evidence := PickupCompletionEvidence{CandidateID: candidate, InboundEventID: stored.InboundEvent, ContextDigest: hash, AssessmentVersion: stored.AssessmentVersion, Decision: stored.Decision}
	if evidence.InboundEventID != execution.Source.EventUUID() || evidence.AssessmentVersion != p.assessment || evidence.Decision != PickupComplete {
		return PickupExecution{}, PickupCompletionEvidence{}, false, ErrPickupConfirmationRequired
	}
	if err := guard(); err != nil {
		return PickupExecution{}, PickupCompletionEvidence{}, false, err
	}
	return execution, evidence, true, nil
}
