// Package testpickupeval provides a testing-owned, offline LOCAL pickup journey.
// Scripted model responses exercise integration, not natural-language quality.
// Expanded G1–G3 paths are source-only: uncompiled, unexecuted and unverified.
package testpickupeval

import (
	"context"
	"errors"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

// ErrNotImplemented identifies the original test-first scaffold failure.
// Retained for callers that distinguish unavailable support from domain refusal;
// the implemented runner does not return it.
var ErrNotImplemented = errors.New("testpickupeval: scenario runner not implemented")

// Exchange is one stored customer turn followed by its stored restaurant reply.
// Exchanges are persisted in slice order after summary presentation and before
// Inbound. Both texts must be nonblank. They are synthetic conversation data,
// never instructions or host authority. Source IDs are assigned by the stores.
type Exchange struct {
	CustomerText   string
	RestaurantText string
}

// ModelResponse scripts the offline HTTP provider's response, not a Go decision.
// HTTPStatus accepts zero/200 for success or 400–599 for failure.
// ToolArguments is passed verbatim as the arguments
// of assess_pickup_completion on successful responses; errors retain the real
// commandcode client's public error identity. No provider metadata grants power.
type ModelResponse struct {
	HTTPStatus    int
	ToolArguments string
}

// Scenario uses a fixed fictional USD 6.50 sandwich, two units, pickup and
// pay_at_pickup. The first unit has the exact note "  no salt 🥪\n".
// No live endpoint, DSN, credentials, authority or policy version is accepted.
type Scenario struct {
	// Inbound nil means silence: do not admit an event, assess or submit.
	// A nonnil blank string is invalid input, not silence.
	Inbound *string
	History []Exchange
	Model   ModelResponse
	// MissingPresentation queues the summary but does not send it or record ACK.
	MissingPresentation bool
	// Replay repeats the same completion source and runtime slot after submission,
	// including after ProcessOrder when requested; it must not call the model again.
	Replay bool
	// ProcessOrder reads the submitted order and units through staff domain APIs,
	// then accepts and starts preparation, without collecting payment.
	ProcessOrder bool
	// PostSummaryQuestion is queued on the existing summary-request source after
	// summary ACK, then actually received/ACKed. It is not a new customer turn.
	// Only one source-bound reply is possible; arbitrary restaurant turns are not.
	PostSummaryQuestion string
	// Edits are real admitted native work after presentation/history. At most two
	// edits are supported; pending work must be last and cannot be refreshed.
	Edits []Edit
	// RefreshSummary prepares and ACKs a new registered summary after resolved
	// edits. Summary/Presentation then describe the current, not original anchor.
	RefreshSummary bool
	// SecondInbound retires the first source through the existing menu gate, then
	// admits a DISTINCT source and invokes the real assessor using the same script.
	// This is sequential, never simultaneous-authorized-claims proof.
	SecondInbound *string
	// Interleave supports only a controlled staff takeover during actual model
	// HTTP. It cannot combine with edits, second inbound, processing or replay.
	Interleave Interleave
}

// EditKind selects a resolved public domain action, never semantic text parsing.
type EditKind string

const (
	AddSandwich   EditKind = "add_sandwich"
	NoteFirstUnit EditKind = "note_first_unit"
)

type Edit struct {
	Kind EditKind
	// Note is used only by NoteFirstUnit and obeys cart.SetNote validation.
	Note string
	// Pending admits a VALID action but does not acquire/execute business work.
	// It is not a representation of an unresolved ambiguous target.
	Pending bool
}

type Interleave string

const TakeStaffControl Interleave = "take_staff_control"

// EditObservation contains actual public admission/execution and cart readback.
// No opaque work lease/ref escapes. Error is the ExecuteNativeEdit outcome;
// pending admission has a zero Result and nil Error because it was not executed.
type EditObservation struct {
	Kind        EditKind
	SourceID    string
	OperationID cart.OperationID
	Pending     bool
	Before      cart.Snapshot
	After       cart.Snapshot
	Result      cart.Result
	Error       error
}

// CompletionAttempt describes the optional distinct second source, not replay.
type CompletionAttempt struct {
	// Actual public job read after normal gate retirement, before second intake.
	RetiredFirstProcessing foundation.Snapshot
	InboundEventID         string
	Context                cart.PickupCompletionContext
	ContextError           error
	ModelRequests          []ModelRequest
	Before                 Effects
	After                  Effects
	Submission             cart.PickupSubmission
	Error                  error
}

// InterleaveObservation separates legitimate control mutation from forbidden
// submission effects. These snapshots are read while the model response is held.
type InterleaveObservation struct {
	BeforeMutation Effects
	AfterMutation  Effects
	ControlBefore  conversation.State
	ControlAfter   conversation.State
}

// ModelRequest is decoded from the actual offline model HTTP request body.
// Headers (especially authorization), endpoints and credentials are not exposed.
// It is not reconstructed from Scenario or CompletionContext.
type ModelRequest struct {
	Model    string                `json:"model"`
	Messages []commandcode.Message `json:"messages"`
	Tools    []ModelTool           `json:"tools"`
}

type ModelTool struct {
	Type     string                     `json:"type"`
	Function commandcode.ToolDefinition `json:"function"`
}

// Delivery pairs the actual receiver request and decoded HTTP 200 ACK. Summary
// additionally has a stored Presentation; reply deliveries do not invent one.
type Delivery struct {
	Message        testpickupreceiver.Message
	Acknowledgment testpickupreceiver.Acknowledgement
}

// Effects are public reads, never SQL row dumps or fixture-maintained counters.
// On completed readback, Processing is nil only for silence. Canceled runs can
// return partial observations with later Effects left zero, never invented.
// Orders and Intents must be complete for this small isolated scenario.
type Effects struct {
	Cart       cart.Snapshot
	Orders     cart.PickupOrderPage
	Intents    []cart.PickupLocalIntent
	Processing *foundation.Snapshot
}

// Observation contains detached outputs only; no pools, tokens or DSNs escape.
// Before is captured after setup/history/current claim, immediately before the
// bridge call. After is captured immediately after it. BeforeReplay is a fresh
// read after optional staff actions (which legitimately append status intents),
// immediately before optional replay. Final is read after replay so its effects
// are distinguishable from staff effects. Optional Second has separate snapshots
// after retiring the first source; Final intentionally precedes Second.
type Observation struct {
	Summary         cart.PreparedPickupSummary
	Presentation    cart.PickupPresentation
	Deliveries      []Delivery
	OriginalSummary cart.PreparedPickupSummary
	Edits           []EditObservation
	InboundEventID  string
	// ContextError is actual preflight read failure, not swallowed or repaired.
	// AssessAndSubmit is still invoked and its error is returned independently.
	ContextError error
	Second       *CompletionAttempt
	Interleaving *InterleaveObservation
	// Context is the actual public CompletionContext read; History is never
	// repaired from nil to empty, filtered, or synthesized from expected input.
	Context        cart.PickupCompletionContext
	ModelRequests  []ModelRequest
	Submission     cart.PickupSubmission
	Before         Effects
	After          Effects
	BeforeReplay   Effects
	Final          Effects
	StaffRead      cart.PickupOrder
	StaffUnits     cart.PickupOrderUnitPage
	StaffProcessed cart.PickupOrder
	Replay         cart.PickupSubmission
	ReplayError    error
}

// Run owns resources through t.Cleanup and bounds domain/HTTP work by ctx and a
// two-minute scenario deadline. testpg.Start has its own bounded startup/cleanup.
// Cancellation after bridge/replay returns partial observations and a context
// error without attempting canceled readback. Setup/readback failures are test
// failures; otherwise the returned error is the primary AssessAndSubmit outcome
// or an input/cancellation error. Distinct-source errors are in Second.Error;
// replay errors are in ReplayError. Silence returns nil. Error identities are
// preserved for errors.Is/errors.As. No expanded-source runtime proof is claimed.
func Run(ctx context.Context, t testing.TB, scenario Scenario) (Observation, error) {
	t.Helper()
	return runScenario(ctx, t, scenario)
}
