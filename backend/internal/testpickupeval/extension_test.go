package testpickupeval_test

// SOURCE ONLY: these new contracts have not been compiled or executed. Historical
// checks of the smaller fixture do not apply to this expanded revision.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/testpickupeval"
)

func currentWire(t *testing.T, requests []testpickupeval.ModelRequest, want pickupintent.ScopedHistoryInput) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("want one actual model HTTP request, got %d", len(requests))
	}
	w := requests[0]
	if w.Model != commandcode.Model || len(w.Messages) != 2 || w.Messages[0].Role != commandcode.RoleSystem ||
		w.Messages[1].Role != commandcode.RoleUser || w.Messages[1].Content == nil || len(w.Tools) != 1 ||
		w.Tools[0].Type != "function" || w.Tools[0].Function.Name != "assess_pickup_completion" {
		t.Fatalf("actual assessor wire shape: %+v", w)
	}
	encoded, err := json.Marshal(want)
	if err != nil || *w.Messages[1].Content != string(encoded) {
		t.Fatalf("actual wire scoped input: got=%s want=%s err=%v", *w.Messages[1].Content, encoded, err)
	}
}

func TestRunAnchoredUtensilsQuestionDoesNotInventCustomerTurn(t *testing.T) {
	inbound, question := "yes", "Would you like utensils?"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, PostSummaryQuestion: question,
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"not_complete"}`}})
	if !errors.Is(err, cart.ErrPickupConfirmationRequired) || o.ContextError != nil {
		t.Fatalf("unrelated assent refusal/context: %v / %v", err, o.ContextError)
	}
	presented(t, o, 2)
	baseline(t, o, true)
	if len(o.Context.History) != 1 || o.Context.History[0].Speaker != cart.PickupSpeakerRestaurant ||
		o.Context.History[0].Text != question || o.Context.History[0].SourceID != o.Deliveries[1].Message.CorrelationID ||
		o.Deliveries[1].Message.Text != question {
		t.Fatalf("expected ONLY the actual source-bound restaurant question: %+v", o.Context.History)
	}
	wire(t, o, inbound, []pickupintent.Message{{SourceID: o.Deliveries[1].Message.CorrelationID, Speaker: pickupintent.SpeakerRestaurant, Text: question}})
	noEffects(t, o)
}

func resolvedEdits() []testpickupeval.Edit {
	return []testpickupeval.Edit{{Kind: testpickupeval.AddSandwich}, {Kind: testpickupeval.NoteFirstUnit, Note: "no onions"}}
}

func assertResolvedEdits(t *testing.T, o testpickupeval.Observation) {
	t.Helper()
	if len(o.Edits) != 2 {
		t.Fatalf("want actual add+note observations: %+v", o.Edits)
	}
	add, note := o.Edits[0], o.Edits[1]
	for _, edit := range o.Edits {
		if edit.Pending || edit.Error != nil || edit.OperationID.String() == "" || edit.SourceID == "" ||
			edit.Result.OperationID != edit.OperationID || !reflect.DeepEqual(edit.Result.Cart, edit.After) ||
			edit.After.Version.Int64() != edit.Before.Version.Int64()+1 {
			t.Fatalf("actual native admission/execution/readback: %+v", edit)
		}
	}
	if len(note.Before.Units) == 0 {
		t.Fatal("note edit has no actual stored target")
	}
	wantFirst := note.Before.Units[0]
	wantFirst.Note = "no onions"
	if add.Kind != testpickupeval.AddSandwich || note.Kind != testpickupeval.NoteFirstUnit ||
		add.SourceID == note.SourceID || add.OperationID == note.OperationID ||
		len(add.Before.Units) != 2 || len(add.After.Units) != 3 || add.After.Subtotal != 1950 ||
		!reflect.DeepEqual(add.After.Units[:2], add.Before.Units) || add.After.Units[2].PriceMinor != 650 ||
		add.After.Units[2].MenuID != add.Before.Units[0].MenuID ||
		!reflect.DeepEqual(note.Before, add.After) || len(note.After.Units) != 3 ||
		note.After.Units[0] != wantFirst ||
		!reflect.DeepEqual(note.After.Units[1:], note.Before.Units[1:]) || note.After.Subtotal != 1950 ||
		!reflect.DeepEqual(o.Before.Cart, note.After) {
		t.Fatalf("resolved add/note setup effects not separated from attempt: %+v", o.Edits)
	}
}

func TestRunRealAddAndNoteInvalidateOldSummaryWithoutAttemptEffects(t *testing.T) {
	inbound := "No, that’s all"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Edits: resolvedEdits(),
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if !errors.Is(err, cart.ErrPickupSummaryRequired) || !errors.Is(o.ContextError, cart.ErrPickupSummaryRequired) {
		t.Fatalf("old-snapshot refusal: bridge=%v context=%v", err, o.ContextError)
	}
	assertResolvedEdits(t, o)
	presented(t, o, 1)
	baseline(t, o, true)
	if o.Summary.CartVersion == o.Before.Cart.Version || len(o.ModelRequests) != 0 {
		t.Fatal("stale summary was not fenced before model assessment")
	}
	noEffects(t, o)
}

func TestRunRefreshedSummarySubmitsCurrentNativeEditSnapshot(t *testing.T) {
	inbound := "Nothing else, thank you. Please place the order."
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Edits: resolvedEdits(), RefreshSummary: true, ProcessOrder: true,
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if err != nil || o.ContextError != nil {
		t.Fatalf("fresh summary completion: bridge=%v context=%v", err, o.ContextError)
	}
	assertResolvedEdits(t, o)
	if o.OriginalSummary.Intent.Text != exactSummary || o.Summary.CandidateID == o.OriginalSummary.CandidateID ||
		o.Summary.CartVersion != o.Before.Cart.Version || o.Summary.UnitCount != 3 || o.Summary.Total.AmountMinor != 1950 ||
		len(o.Deliveries) != 2 || o.Deliveries[0].Message.Text != exactSummary ||
		o.Deliveries[0].Message.CorrelationID != o.OriginalSummary.Intent.ID.String() ||
		o.Deliveries[1].Message.Text != o.Summary.Intent.Text || o.Deliveries[1].Message.CorrelationID != o.Summary.Intent.ID.String() ||
		o.Presentation.IntentID != o.Summary.Intent.ID || o.Context.PresentationID != o.Presentation.ID ||
		o.Presentation.ReceivedAt.IsZero() || o.Context.CandidateID != o.Summary.CandidateID || o.Context.Digest == ([32]byte{}) {
		t.Fatal("fresh registered summary/actual presentation/current candidate correlation missing")
	}
	for _, d := range o.Deliveries {
		digest := sha256.Sum256([]byte(d.Message.Text))
		if d.Acknowledgment.CorrelationID != d.Message.CorrelationID || d.Acknowledgment.ReceiverReference == "" ||
			d.Acknowledgment.ContentSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("actual correlated summary ACK: %+v", d)
		}
	}
	if o.Context.History == nil || len(o.Context.History) != 0 {
		t.Fatalf("fresh anchor must have established empty history: %+v", o.Context.History)
	}
	currentWire(t, o.ModelRequests, pickupintent.ScopedHistoryInput{SummaryText: o.Summary.Intent.Text, ClosingQuestion: o.Summary.ClosingQuestion,
		InboundText: inbound, History: []pickupintent.Message{}})
	if len(o.Before.Orders.Items) != 0 || len(o.Before.Intents) != 2 || len(o.After.Orders.Items) != 1 || len(o.After.Intents) != 3 ||
		o.After.Orders.Items[0] != o.Submission.Order.PickupOrderHeader || o.Submission.Order.Status != cart.PickupSubmitted || o.Submission.Order.Payment.State != cart.PickupUnpaid ||
		o.Submission.Order.Payment.CollectedAt != nil || o.Submission.Order.Confirmed.ID != o.Summary.CandidateID ||
		o.Submission.Order.Confirmed.UnitCount != 3 || o.Submission.Order.Total != o.Summary.Total ||
		o.Submission.Receipt.ID.String() == "" || o.Submission.Receipt.Text == "" ||
		o.After.Intents[2].Kind != "receipt" || o.After.Intents[2].ID != o.Submission.Receipt.ID ||
		o.Submission.Receipt.OrderID != o.Submission.Order.ID || o.After.Intents[2].Text != o.Submission.Receipt.Text ||
		len(o.StaffUnits.Items) != 3 || o.StaffUnits.Items[0].Note != "no onions" ||
		o.StaffProcessed.Payment.State != cart.PickupUnpaid || o.StaffProcessed.Status != cart.PickupPreparing {
		t.Fatal("current snapshot did not yield exactly one unpaid order, receipt and staff-visible edited units")
	}
	if !reflect.DeepEqual(o.After.Cart, o.Before.Cart) || !reflect.DeepEqual(o.After.Processing, o.Before.Processing) {
		t.Fatal("fresh completion changed draft or inbound processing counters")
	}
	equalEffects(t, "no replay after refreshed order staff processing", o.BeforeReplay, o.Final)
}

func TestRunValidPendingNativeWorkRefusesWithoutAttemptEffects(t *testing.T) {
	inbound := "No, that’s all"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Edits: []testpickupeval.Edit{{Kind: testpickupeval.AddSandwich, Pending: true}},
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if !errors.Is(err, cart.ErrPickupSummaryRequired) || !errors.Is(o.ContextError, cart.ErrPickupSummaryRequired) {
		t.Fatalf("pending valid-work refusal: bridge=%v context=%v", err, o.ContextError)
	}
	if len(o.Edits) != 1 {
		t.Fatalf("missing actual admission: %+v", o.Edits)
	}
	edit := o.Edits[0]
	if !edit.Pending || edit.OperationID.String() == "" || edit.SourceID == "" || edit.SourceID == o.InboundEventID ||
		edit.Error != nil || !reflect.DeepEqual(edit.Result, cart.Result{}) || !reflect.DeepEqual(edit.Before, edit.After) ||
		!reflect.DeepEqual(o.Before.Cart, edit.After) || len(o.ModelRequests) != 0 {
		t.Fatalf("valid admitted pending work was executed or misrepresented: %+v", edit)
	}
	presented(t, o, 1)
	baseline(t, o, true)
	noEffects(t, o)
}

func TestRunDistinctSecondCompletionDoesNotCreateAnotherOrder(t *testing.T) {
	first, second := "No, that’s all", "Nothing else, thank you. Please place the order."
	o, err := run(t, testpickupeval.Scenario{Inbound: &first, SecondInbound: &second,
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if err != nil || o.ContextError != nil || o.Second == nil {
		t.Fatalf("first submission/second attempt setup: %v / %+v", err, o.Second)
	}
	presented(t, o, 1)
	baseline(t, o, true)
	wire(t, o, first, []pickupintent.Message{})
	next := o.Second
	if next.RetiredFirstProcessing.Event != o.Before.Processing.Event || next.RetiredFirstProcessing.JobState != "completed" ||
		next.RetiredFirstProcessing.JobCount != 1 || next.RetiredFirstProcessing.JobAttempt != 1 ||
		next.RetiredFirstProcessing.OutboxCount != 1 || next.RetiredFirstProcessing.OutboxState != "pending" {
		t.Fatalf("first source was not normally retired through a queued gate reply: %+v", next.RetiredFirstProcessing)
	}
	if next.InboundEventID == "" || next.InboundEventID == o.InboundEventID || next.ContextError != nil ||
		next.Context.InboundEventID != next.InboundEventID || next.Context.CandidateID != o.Summary.CandidateID ||
		len(next.Context.History) != 1 || next.Context.History[0].SourceID != o.InboundEventID ||
		next.Context.History[0].Speaker != cart.PickupSpeakerCustomer || next.Context.History[0].Text != first ||
		next.Before.Processing == nil || next.Before.Processing.Event.ID == o.Before.Processing.Event.ID ||
		next.Before.Processing.Event.Text != second || next.Before.Processing.JobState != "leased" ||
		next.Before.Processing.JobCount != 1 || next.Before.Processing.JobAttempt != 1 || next.Before.Processing.OutboxCount != 0 {
		t.Fatalf("not an actual sequential distinct-source claim/history: %+v", next)
	}
	currentWire(t, next.ModelRequests, pickupintent.ScopedHistoryInput{SummaryText: o.Summary.Intent.Text, ClosingQuestion: o.Summary.ClosingQuestion,
		InboundText: second, History: []pickupintent.Message{{SourceID: o.InboundEventID, Speaker: pickupintent.SpeakerCustomer, Text: first}}})
	if !errors.Is(next.Error, cart.ErrConflict) || !reflect.DeepEqual(next.Submission, cart.PickupSubmission{}) ||
		len(next.Before.Orders.Items) != 1 || len(next.Before.Intents) != 2 ||
		next.Before.Orders.Items[0] != o.Submission.Order.PickupOrderHeader || o.Submission.Order.Payment.State != cart.PickupUnpaid ||
		!reflect.DeepEqual(next.Before.Intents, o.Final.Intents) || !reflect.DeepEqual(next.Before.Cart, o.Final.Cart) {
		t.Fatalf("second source should conflict against one existing unpaid order: %+v", next)
	}
	equalEffects(t, "distinct second completion", next.Before, next.After)
}

func TestRunActualModelHTTPBarrierFencesPublicControlTakeover(t *testing.T) {
	inbound := "No, that’s all"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Interleave: testpickupeval.TakeStaffControl,
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if !errors.Is(err, cart.ErrStale) || o.ContextError != nil || o.Interleaving == nil {
		t.Fatalf("actual model barrier/control fence: %v / %+v", err, o.Interleaving)
	}
	presented(t, o, 1)
	baseline(t, o, true)
	wire(t, o, inbound, []pickupintent.Message{})
	mutation := o.Interleaving
	if mutation.ControlBefore.Mode != conversation.ModeAI || mutation.ControlBefore.Version != 0 ||
		mutation.ControlAfter.Mode != conversation.ModeStaff || mutation.ControlAfter.Version != 1 ||
		mutation.ControlBefore.ConversationID != o.Before.Cart.ConversationID ||
		mutation.ControlAfter.ConversationID != mutation.ControlBefore.ConversationID {
		t.Fatalf("actual public control mutation: before=%+v after=%+v", mutation.ControlBefore, mutation.ControlAfter)
	}
	equalEffects(t, "HTTP pause before mutation", o.Before, mutation.BeforeMutation)
	equalEffects(t, "control-only mutation business effects", mutation.BeforeMutation, mutation.AfterMutation)
	equalEffects(t, "stale bridge after mutation", mutation.AfterMutation, o.After)
	noEffects(t, o)
}
