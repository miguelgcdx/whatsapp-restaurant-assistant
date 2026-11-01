package testpickupeval_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/testpickupeval"
)

const exactSummary = "Pickup summary (not submitted)\nItems:\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"  no salt 🥪\\n\"\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"\"\nFulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD 13.00\nDelivery fee: USD 0.00\nTotal: USD 13.00\nPreparation estimate: 10 minutes (provisional, preparation only).\nWould you like anything else?"

func run(t *testing.T, s testpickupeval.Scenario) (testpickupeval.Observation, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("isolated PostgreSQL and loopback HTTP integration")
	}
	o, err := testpickupeval.Run(t.Context(), t, s)
	if errors.Is(err, testpickupeval.ErrNotImplemented) {
		t.Fatalf("functional RED: Run returned ErrNotImplemented: %v", err)
	}
	return o, err
}

func presented(t *testing.T, o testpickupeval.Observation, deliveries int) {
	t.Helper()
	if o.Summary.Intent.Text != exactSummary || o.Summary.ClosingQuestion != "Would you like anything else?" ||
		o.Summary.UnitCount != 2 || o.Summary.Total.AmountMinor != 1300 || o.Summary.Total.Currency != "USD" ||
		o.Summary.Intent.ID.String() == "" || o.Summary.CandidateID.String() == "" ||
		o.Presentation.ID.String() == "" || o.Presentation.IntentID != o.Summary.Intent.ID || o.Presentation.ReceivedAt.IsZero() {
		t.Fatalf("authoritative summary and stored presentation missing: %+v %+v", o.Summary, o.Presentation)
	}
	if len(o.Deliveries) != deliveries || o.Deliveries[0].Message.Text != exactSummary ||
		o.Deliveries[0].Message.CorrelationID != o.Summary.Intent.ID.String() {
		t.Fatalf("actual receiver deliveries: %+v", o.Deliveries)
	}
	for _, d := range o.Deliveries {
		digest := sha256.Sum256([]byte(d.Message.Text))
		if d.Acknowledgment.CorrelationID != d.Message.CorrelationID || d.Acknowledgment.ReceiverReference == "" ||
			d.Acknowledgment.ContentSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("actual HTTP ACK correlation: %+v", d)
		}
	}
	if o.Summary.Intent.TextDigest != sha256.Sum256([]byte(exactSummary)) {
		t.Fatal("summary digest did not bind exact text")
	}
}

func wire(t *testing.T, o testpickupeval.Observation, inbound string, history []pickupintent.Message) {
	t.Helper()
	if len(o.ModelRequests) != 1 {
		t.Fatalf("want one actual model HTTP request, got %d", len(o.ModelRequests))
	}
	w := o.ModelRequests[0]
	if w.Model != commandcode.Model || len(w.Messages) != 2 || w.Messages[0].Role != commandcode.RoleSystem ||
		w.Messages[1].Role != commandcode.RoleUser || w.Messages[1].Content == nil || len(w.Tools) != 1 ||
		w.Tools[0].Type != "function" || w.Tools[0].Function.Name != "assess_pickup_completion" {
		t.Fatalf("actual assessor wire shape: %+v", w)
	}
	want := pickupintent.ScopedHistoryInput{SummaryText: exactSummary, ClosingQuestion: o.Summary.ClosingQuestion, InboundText: inbound, History: history}
	encoded, err := json.Marshal(want)
	if err != nil || *w.Messages[1].Content != string(encoded) {
		t.Fatalf("complete exact scoped history: got=%s want=%s err=%v", *w.Messages[1].Content, encoded, err)
	}
	if o.Context.SummaryText != exactSummary || o.Context.InboundText != inbound || o.Context.History == nil ||
		o.Context.CandidateID != o.Summary.CandidateID || o.Context.PresentationID != o.Presentation.ID ||
		o.Context.IntentID != o.Summary.Intent.ID || o.Context.InboundEventID == "" || o.Context.Digest == ([32]byte{}) {
		t.Fatalf("actual trusted completion context missing: %+v", o.Context)
	}
}

func baseline(t *testing.T, o testpickupeval.Observation, hasInbound bool) {
	t.Helper()
	if o.Before.Orders.Items == nil || len(o.Before.Orders.Items) != 0 || o.Before.Orders.NextCursor != nil ||
		len(o.Before.Intents) != 1 || o.Before.Intents[0].Kind != "summary" ||
		o.Before.Intents[0].ID != o.Summary.Intent.ID || o.Before.Intents[0].Text != exactSummary {
		t.Fatalf("public before state: %+v", o.Before)
	}
	if hasInbound {
		p := o.Before.Processing
		if p == nil || p.JobState != "leased" || p.JobCount != 1 || p.JobAttempt != 1 || p.OutboxCount != 0 {
			t.Fatalf("claimed inbound processing baseline: %+v", p)
		}
	} else if o.Before.Processing != nil {
		t.Fatal("silence must not create a current inbound job")
	}
	if !reflect.DeepEqual(o.Before.Processing, o.After.Processing) || !reflect.DeepEqual(o.Before.Cart, o.After.Cart) {
		t.Fatal("assessment changed inbound counters/state or cart")
	}
}

func equalEffects(t *testing.T, stage string, want, got testpickupeval.Effects) {
	t.Helper()
	if !reflect.DeepEqual(got.Cart, want.Cart) {
		t.Errorf("%s Cart changed: got=%+v want=%+v", stage, got.Cart, want.Cart)
	}
	if !reflect.DeepEqual(got.Orders, want.Orders) {
		t.Errorf("%s Orders changed: got=%+v want=%+v", stage, got.Orders, want.Orders)
	}
	if !reflect.DeepEqual(got.Intents, want.Intents) {
		t.Errorf("%s Intents changed: got=%+v want=%+v", stage, got.Intents, want.Intents)
	}
	if !reflect.DeepEqual(got.Processing, want.Processing) {
		t.Errorf("%s Processing changed: got=%+v want=%+v", stage, got.Processing, want.Processing)
	}
}

func noEffects(t *testing.T, o testpickupeval.Observation) {
	t.Helper()
	if !reflect.DeepEqual(o.Submission, cart.PickupSubmission{}) ||
		!reflect.DeepEqual(o.Replay, cart.PickupSubmission{}) || o.ReplayError != nil ||
		!reflect.DeepEqual(o.StaffRead, cart.PickupOrder{}) || !reflect.DeepEqual(o.StaffProcessed, cart.PickupOrder{}) {
		t.Fatal("refused/silent scenario fabricated a submission/staff/replay result")
	}
	equalEffects(t, "assessment", o.Before, o.After)
	equalEffects(t, "without staff actions", o.After, o.BeforeReplay)
	equalEffects(t, "without replay", o.BeforeReplay, o.Final)
}

func TestRunCompleteHTTPHistoryStaffAndReplay(t *testing.T) {
	for _, inbound := range []string{"No, that’s all", "Nothing else, thank you. Please place the order."} {
		t.Run(inbound, func(t *testing.T) {
			history := []testpickupeval.Exchange{{CustomerText: "  What is the pickup address?\n", RestaurantText: "  Pickup address: fictional restaurant 🥪\n"}}
			o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, History: history,
				Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}, ProcessOrder: true, Replay: true})
			if err != nil {
				t.Fatalf("connected completion: %v", err)
			}
			presented(t, o, 2)
			baseline(t, o, true)
			if len(o.Context.History) != 2 || o.Context.History[0].SourceID == "" || o.Context.History[1].SourceID == "" ||
				o.Context.History[0].SourceID == o.Context.History[1].SourceID ||
				o.Context.History[1].SourceID != o.Deliveries[1].Message.CorrelationID ||
				o.Deliveries[1].Message.Text != history[0].RestaurantText {
				t.Fatal("stored clarification source correlation/order missing")
			}
			if o.Context.History[0].Speaker != cart.PickupSpeakerCustomer || o.Context.History[0].Text != history[0].CustomerText ||
				o.Context.History[1].Speaker != cart.PickupSpeakerRestaurant || o.Context.History[1].Text != history[0].RestaurantText {
				t.Fatal("stored clarification text/speakers changed")
			}
			wire(t, o, inbound, []pickupintent.Message{
				{SourceID: o.Context.History[0].SourceID, Speaker: pickupintent.SpeakerCustomer, Text: history[0].CustomerText},
				{SourceID: o.Context.History[1].SourceID, Speaker: pickupintent.SpeakerRestaurant, Text: history[0].RestaurantText},
			})
			order := o.Submission.Order
			if order.ID.String() == "" || order.Status != cart.PickupSubmitted || order.Payment.State != cart.PickupUnpaid ||
				order.Payment.CollectedAt != nil || order.Total != o.Summary.Total || order.Confirmed.ID != o.Summary.CandidateID ||
				order.Confirmed.UnitCount != 2 || order.Confirmed.DeliveryFee.AmountMinor != 0 ||
				o.Submission.Receipt.ID.String() == "" || o.Submission.Receipt.OrderID != order.ID || o.Submission.Receipt.Text == "" {
				t.Fatalf("one unpaid immutable submission: %+v", o.Submission)
			}
			if len(o.After.Orders.Items) != 1 || o.After.Orders.Items[0] != order.PickupOrderHeader ||
				len(o.After.Intents) != 2 || o.After.Intents[1].Kind != "receipt" ||
				o.After.Intents[1].ID != o.Submission.Receipt.ID || o.After.Intents[1].Text != o.Submission.Receipt.Text {
				t.Fatal("submission public order/receipt counters or correlation incorrect")
			}
			if !reflect.DeepEqual(o.StaffRead, order) || len(o.StaffUnits.Items) != 2 || o.StaffUnits.NextCursor != nil ||
				o.StaffUnits.SummaryID != o.Summary.CandidateID || o.StaffUnits.Items[0].Note != "  no salt 🥪\n" ||
				o.StaffUnits.Items[1].Note != "" || o.StaffProcessed.ID != order.ID || o.StaffProcessed.Status != cart.PickupPreparing ||
				o.StaffProcessed.PreparationStartedAt == nil || o.StaffProcessed.Payment.State != cart.PickupUnpaid ||
				o.StaffProcessed.Payment.CollectedAt != nil || o.StaffProcessed.Confirmed != order.Confirmed {
				t.Fatal("staff read/process lost unpaid snapshot or units")
			}
			// Staff transitions legitimately append status intents. Observe and pin
			// them before replay, rather than blaming them on the replay call.
			if len(o.BeforeReplay.Orders.Items) != 1 || o.BeforeReplay.Orders.NextCursor != nil ||
				o.BeforeReplay.Orders.Items[0] != o.StaffProcessed.PickupOrderHeader {
				t.Fatalf("staff order readback: got=%+v want=%+v", o.BeforeReplay.Orders, o.StaffProcessed.PickupOrderHeader)
			}
			if !reflect.DeepEqual(o.BeforeReplay.Cart, o.Before.Cart) {
				t.Errorf("staff changed Cart: got=%+v want=%+v", o.BeforeReplay.Cart, o.Before.Cart)
			}
			if !reflect.DeepEqual(o.BeforeReplay.Processing, o.Before.Processing) {
				t.Errorf("staff changed inbound Processing: got=%+v want=%+v", o.BeforeReplay.Processing, o.Before.Processing)
			}
			if len(o.BeforeReplay.Intents) != 4 {
				t.Fatalf("staff must add exactly two status intents: got=%+v", o.BeforeReplay.Intents)
			}
			if !reflect.DeepEqual(o.BeforeReplay.Intents[:2], o.After.Intents) {
				t.Errorf("staff changed original summary/receipt intents: got=%+v want=%+v", o.BeforeReplay.Intents[:2], o.After.Intents)
			}
			for i, status := range []cart.PickupStatus{cart.PickupAccepted, cart.PickupPreparing} {
				intent := o.BeforeReplay.Intents[i+2]
				wantText := "Pickup order " + order.ID.String() + ": " + string(status) + "; payment: unpaid."
				if intent.ID.String() == "" || intent.Kind != "status" || intent.Text != wantText ||
					intent.CandidateID != o.Summary.CandidateID || intent.OrderID == nil || *intent.OrderID != order.ID {
					t.Errorf("staff status intent %d correlation/content: got=%+v wantText=%q", i, intent, wantText)
				}
				for _, earlier := range o.BeforeReplay.Intents[:i+2] {
					if intent.ID == earlier.ID {
						t.Errorf("staff reused intent ID %s", intent.ID.String())
					}
				}
			}
			if o.ReplayError != nil {
				t.Errorf("same-event ReplayError: %v", o.ReplayError)
			}
			if !reflect.DeepEqual(o.Replay, o.Submission) {
				t.Errorf("same-event Replay changed original result: got=%+v want=%+v", o.Replay, o.Submission)
			}
			equalEffects(t, "same-event replay", o.BeforeReplay, o.Final)
		})
	}
}

func TestRunNotCompletePreservesPublicEffects(t *testing.T) {
	inbound := "maybe"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"not_complete"}`}})
	if !errors.Is(err, cart.ErrPickupConfirmationRequired) {
		t.Fatalf("not_complete error identity: %v", err)
	}
	presented(t, o, 1)
	baseline(t, o, true)
	wire(t, o, inbound, []pickupintent.Message{})
	noEffects(t, o)
}

func TestRunProviderFailurePreservesIdentityAndEffects(t *testing.T) {
	inbound := "No, that’s all"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, Model: testpickupeval.ModelResponse{HTTPStatus: http.StatusServiceUnavailable}})
	var provider *commandcode.HTTPError
	if !errors.Is(err, commandcode.ErrUnavailable) || !errors.As(err, &provider) || provider.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("provider failure identity: %v", err)
	}
	presented(t, o, 1)
	baseline(t, o, true)
	wire(t, o, inbound, []pickupintent.Message{})
	noEffects(t, o)
}

func TestRunMissingPresentationDoesNotAssessOrSubmit(t *testing.T) {
	inbound := "No, that’s all"
	o, err := run(t, testpickupeval.Scenario{Inbound: &inbound, MissingPresentation: true,
		Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if !errors.Is(err, cart.ErrPickupSummaryRequired) {
		t.Fatalf("missing presentation error identity: %v", err)
	}
	baseline(t, o, true)
	if len(o.ModelRequests) != 0 || len(o.Deliveries) != 0 || o.Presentation != (cart.PickupPresentation{}) {
		t.Fatal("queued summary was treated as received or model was called without presentation")
	}
	noEffects(t, o)
}

func TestRunSilenceDoesNotAdmitAssessOrSubmit(t *testing.T) {
	o, err := run(t, testpickupeval.Scenario{Model: testpickupeval.ModelResponse{ToolArguments: `{"decision":"complete"}`}})
	if err != nil {
		t.Fatalf("silence is no assessment, not a domain refusal: %v", err)
	}
	presented(t, o, 1)
	baseline(t, o, false)
	if len(o.ModelRequests) != 0 || o.Context.InboundEventID != "" || o.After.Processing != nil || o.Final.Processing != nil {
		t.Fatal("silence admitted an inbound event or invoked the model")
	}
	noEffects(t, o)
}
