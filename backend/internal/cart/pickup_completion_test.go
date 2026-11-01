package cart_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

// These tests deliberately reuse pickupSetup and its existing testpg/testsocket
// lifetime, trusted cart editing, pricing, per-unit note and membership fixtures.
// Neither a Go decision fake nor hand-built successful evidence crosses the bridge.
type pickupBridgeWire struct {
	Model    string                `json:"model"`
	Messages []commandcode.Message `json:"messages"`
	Tools    []struct {
		Type     string                     `json:"type"`
		Function commandcode.ToolDefinition `json:"function"`
	} `json:"tools"`
}

type pickupBridgeModel struct {
	assessor pickupintent.Assessor
	requests chan pickupBridgeWire
}

// The real commandcode client still constructs/validates its wire request. Only
// the transport destination is redirected to a fixture-owned real HTTP server.
func pickupBridgeModelServer(t *testing.T, arguments string, status int, release <-chan struct{}) pickupBridgeModel {
	t.Helper()
	requests := make(chan pickupBridgeWire, 16)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire pickupBridgeWire
		if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&wire) != nil {
			t.Error("invalid model HTTP request")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		select {
		case requests <- wire:
		case <-r.Context().Done():
			return
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = io.WriteString(w, `{"error":"synthetic provider unavailable"}`)
			return
		}
		// Synthetic protocol response, not an assertion of language quality.
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"index": 0, "finish_reason": "tool_calls",
			"message": map[string]any{"role": "assistant", "content": nil,
				"tool_calls": []commandcode.ToolCall{{ID: "pickup-bridge-fixture", Type: "function", Function: commandcode.FunctionCall{Name: "assess_pickup_completion", Arguments: arguments}}}},
		}}})
	}))
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 5 * time.Second
	server.Config.WriteTimeout = 5 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-bridge-key", Transport: pickupTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != commandcode.Endpoint {
			return nil, errors.New("unexpected model endpoint")
		}
		local := r.Clone(r.Context())
		local.URL = destination
		local.Host = destination.Host
		return server.Client().Transport.RoundTrip(local)
	})})
	if err != nil {
		t.Fatal(err)
	}
	assessor, err := pickupintent.New(pickupintent.Config{Client: client, MaxOutputTokens: 80})
	if err != nil {
		t.Fatal(err)
	}
	return pickupBridgeModel{assessor: assessor, requests: requests}
}

// Decode the actual HTTP 200 ACK, validate its correlation/digest/reference, and
// return its bytes. Expected local SHA is validation only, never receipt evidence.
func pickupBridgeReceive(t *testing.T, receiver *testpickupreceiver.Receiver, correlation, text string) (testpickupreceiver.Acknowledgement, [32]byte) {
	t.Helper()
	body, err := json.Marshal(testpickupreceiver.Message{CorrelationID: correlation, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, receiver.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := receiver.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var ack testpickupreceiver.Acknowledgement
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK || decoder.Decode(&ack) != nil {
		t.Fatalf("receiver ACK status=%d ack=%+v", response.StatusCode, ack)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("receiver ACK trailing data")
	}
	decoded, err := hex.DecodeString(ack.ContentSHA256)
	if err != nil || len(decoded) != sha256.Size || ack.CorrelationID != correlation || ack.ReceiverReference == "" {
		t.Fatalf("invalid actual ACK: %+v %v", ack, err)
	}
	var digest [32]byte
	copy(digest[:], decoded)
	if digest != sha256.Sum256([]byte(text)) {
		t.Fatal("receiver acknowledged different text")
	}
	return ack, digest
}

func pickupBridgePrepare(t *testing.T, f *pickupFixture, receiver *testpickupreceiver.Receiver, presented bool) cart.PreparedPickupSummary {
	t.Helper()
	summary, execution := f.prepare(t)
	queued, err := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if err != nil || len(queued) != 1 || queued[0].Kind != "summary" || queued[0].ID != summary.Intent.ID || queued[0].CandidateID != summary.CandidateID || queued[0].Text != summary.Intent.Text {
		t.Fatal("actual authoritative queue", queued, err)
	}
	if presented {
		ack, digest := pickupBridgeReceive(t, receiver, queued[0].ID.String(), queued[0].Text)
		receipt, err := f.p.RecordLocalPresentation(t.Context(), f.local, cart.PickupLocalPresentation{IntentID: queued[0].ID, CandidateID: queued[0].CandidateID, ReceiverReference: ack.ReceiverReference, ReceivedTextDigest: digest})
		if err != nil || receipt.ID.String() == "" || receipt.ReceivedAt.IsZero() {
			t.Fatal(receipt, err)
		}
	}
	f.finish(t, execution)
	return summary
}

func pickupBridgeCheckWire(t *testing.T, wire pickupBridgeWire, want pickupintent.ScopedHistoryInput) {
	t.Helper()
	if wire.Model != commandcode.Model || len(wire.Messages) != 2 || wire.Messages[0].Role != commandcode.RoleSystem || wire.Messages[1].Role != commandcode.RoleUser || wire.Messages[1].Content == nil || len(wire.Tools) != 1 || wire.Tools[0].Type != "function" || wire.Tools[0].Function.Name != "assess_pickup_completion" {
		t.Fatal("actual assessor wire shape", wire)
	}
	// Exact serialized equality also excludes forwarded authority metadata and
	// detects normalization, anchor duplication, missing turns and last-N input.
	exact, err := json.Marshal(want)
	if err != nil || *wire.Messages[1].Content != string(exact) {
		t.Fatalf("portable input mismatch: got=%s want=%s err=%v", *wire.Messages[1].Content, exact, err)
	}
}

// Inspect is a public read of processing state/counters, not direct SQL. A
// bridge assessment must not retire/retry the inbound job or queue an AI reply.
func pickupBridgePreserveProcessing(t *testing.T, f *pickupFixture, x cart.PickupExecution) func() {
	t.Helper()
	before, err := f.jobs.Inspect(t.Context(), x.Source.EventProviderID())
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		after, err := f.jobs.Inspect(t.Context(), x.Source.EventProviderID())
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Errorf("bridge changed public processing state/counters: before=%+v after=%+v err=%v", before, after, err)
		}
	}
}

func TestPickupCompletionBridgeHTTPAssessorOrderReplayAndStaff(t *testing.T) {
	f := pickupSetup(t)
	receiver := testpickupreceiver.New(t)
	summary := pickupBridgePrepare(t, f, receiver, true)
	exact := "Pickup summary (not submitted)\nItems:\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"  no salt 🥪\\n\"\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"\"\nFulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD 13.00\nDelivery fee: USD 0.00\nTotal: USD 13.00\nPreparation estimate: 10 minutes (provisional, preparation only).\nWould you like anything else?"
	if summary.Intent.Text != exact || summary.UnitCount != 2 || summary.Total.AmountMinor != 1300 || summary.Estimate == nil || summary.Estimate.Minutes != 10 || !summary.Estimate.Provisional || summary.Estimate.AsOf.IsZero() {
		t.Fatal("existing authoritative S0 pricing/note/preparation contract", summary)
	}
	// Two unfiltered intervening turns, including a non-order clarification. The
	// restaurant turn must traverse HTTP too, not f.present or fabricated evidence.
	clarification := f.inbound(t, "  What is the pickup address?\n")
	*f.reply = "  Pickup address: fictional restaurant 🥪\n"
	answer := f.finish(t, clarification)
	ack, digest := pickupBridgeReceive(t, receiver, answer.OutboxID, answer.Content)
	if err := f.p.RecordLocalReply(t.Context(), f.local, answer.OutboxID, ack.ReceiverReference, digest); err != nil {
		t.Fatal(err)
	}
	x := f.inbound(t, "No, that’s all")
	defer pickupBridgePreserveProcessing(t, f, x)()
	trusted, err := f.p.CompletionContext(t.Context(), x)
	want := pickupintent.ScopedHistoryInput{SummaryText: exact, ClosingQuestion: summary.ClosingQuestion, InboundText: "No, that’s all", History: []pickupintent.Message{
		{SourceID: clarification.Source.EventUUID(), Speaker: pickupintent.SpeakerCustomer, Text: clarification.Claim.Event.Text},
		{SourceID: answer.OutboxID, Speaker: pickupintent.SpeakerRestaurant, Text: answer.Content},
	}}
	if err != nil || trusted.History == nil || len(trusted.History) != 2 || trusted.SummaryText != want.SummaryText || trusted.InboundText != want.InboundText || trusted.CandidateID != summary.CandidateID {
		t.Fatal("trusted context setup", trusted, err)
	}
	model := pickupBridgeModelServer(t, `{"decision":"complete"}`, http.StatusOK, nil)
	orders, intents, snapshot := pickupEffects(t, f)
	out, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
	if err != nil {
		pickupUnchanged(t, f, orders, intents, snapshot)
		t.Fatalf("connected bridge should submit one unpaid order: %v", err)
	}
	select {
	case wire := <-model.requests:
		pickupBridgeCheckWire(t, wire, want)
	default:
		t.Fatal("bridge did not invoke actual assessor over HTTP")
	}
	if out.Order.Status != cart.PickupSubmitted || out.Order.Payment.State != cart.PickupUnpaid || out.Order.Payment.CollectedAt != nil || out.Order.Total != summary.Total || out.Order.Confirmed.ID != summary.CandidateID || out.Order.Confirmed.UnitCount != 2 || out.Order.Confirmed.DeliveryFee.AmountMinor != 0 || out.Receipt.ID.String() == "" || out.Receipt.Text == "" || strings.Contains(out.Receipt.Text, summary.ClosingQuestion) || out.Order.Estimate == nil || out.Order.Estimate.Minutes != 10 || !out.Order.Estimate.Provisional {
		t.Fatal("one immutable unpaid pickup result", out)
	}
	replay, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
	if err != nil || !reflect.DeepEqual(replay, out) {
		t.Fatal("original bridge replay", replay, err)
	}
	afterOrders, afterIntents, afterCart := pickupEffects(t, f)
	if len(afterOrders.Items) != 1 || afterOrders.Items[0] != out.Order.PickupOrderHeader || len(afterIntents) != 2 || afterIntents[0].ID != summary.Intent.ID || afterIntents[1].Kind != "receipt" || afterIntents[1].ID != out.Receipt.ID || afterIntents[1].Text != out.Receipt.Text || !reflect.DeepEqual(afterCart, snapshot) {
		t.Fatal("replay must not add order/receipt/intent or change cart", afterOrders, afterIntents, afterCart)
	}
	detail, err := f.p.ReadOrder(t.Context(), "menu-operator", out.Order.ID)
	if err != nil || !reflect.DeepEqual(detail, out.Order) {
		t.Fatal(detail, err)
	}
	units, err := f.p.ReadUnits(t.Context(), "menu-operator", out.Order.ID, summary.CandidateID, cart.PickupPageRequest{})
	if err != nil || len(units.Items) != 2 || units.Items[0].Note != "  no salt 🥪\n" || units.Items[1].Note != "" || units.Items[0].Name != "Fictional sandwich" || units.Items[0].Price != (cart.PickupMoney{AmountMinor: 650, Currency: "USD", Exponent: 2}) || units.NextCursor != nil {
		t.Fatal(units, err)
	}
	if _, err := f.p.ReadOrder(t.Context(), "not-a-member", out.Order.ID); !errors.Is(err, cart.ErrPickupForbidden) {
		t.Fatal("staff membership boundary", err)
	}
	// Existing staff DOMAIN actions, not JWT HTTP/main/r3 wiring. Unpaid stays
	// independent of preparation; editing the draft cannot rewrite the snapshot.
	accepted := pickupAction(t, f, out.Order, cart.PickupAccept)
	preparing := pickupAction(t, f, accepted, cart.PickupStartPreparing)
	f.snapshot = apply(t, f.fixture, cart.Result{Cart: f.snapshot}, add(t, 1)).Cart
	current, err := f.p.ReadOrder(t.Context(), "menu-operator", out.Order.ID)
	if err != nil || !reflect.DeepEqual(current, preparing) || current.Status != cart.PickupPreparing || current.Payment.State != cart.PickupUnpaid || current.PreparationStartedAt == nil || current.Confirmed != out.Order.Confirmed {
		t.Fatal("observable processing and immutable confirmed terms", current, err)
	}
	immutable, err := f.p.ReadUnits(t.Context(), "menu-operator", out.Order.ID, summary.CandidateID, cart.PickupPageRequest{})
	if err != nil || !reflect.DeepEqual(immutable, units) {
		t.Fatal(immutable, err)
	}
	advancedOrders, advancedIntents, advancedCart := pickupEffects(t, f)
	replay, err = f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
	if err != nil || !reflect.DeepEqual(replay, out) {
		t.Fatal("original replay survives later draft/staff changes", replay, err)
	}
	pickupUnchanged(t, f, advancedOrders, advancedIntents, advancedCart)
	if received := receiver.Received(); len(received) != 2 || received[0].Text != exact || received[1].Text != answer.Content {
		t.Fatal("actual receiver path", received)
	}
}

func TestPickupCompletionBridgeNotCompletePreservesEffects(t *testing.T) {
	f := pickupSetup(t)
	summary := pickupBridgePrepare(t, f, testpickupreceiver.New(t), true)
	x := f.inbound(t, "Yes, add a drink")
	defer pickupBridgePreserveProcessing(t, f, x)()
	model := pickupBridgeModelServer(t, `{"decision":"not_complete"}`, http.StatusOK, nil)
	orders, intents, snapshot := pickupEffects(t, f)
	out, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
	pickupUnchanged(t, f, orders, intents, snapshot)
	if !errors.Is(err, cart.ErrPickupConfirmationRequired) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("not_complete must refuse without effects: %+v %v", out, err)
	}
	select {
	case wire := <-model.requests:
		pickupBridgeCheckWire(t, wire, pickupintent.ScopedHistoryInput{SummaryText: summary.Intent.Text, ClosingQuestion: summary.ClosingQuestion, InboundText: x.Claim.Event.Text, History: []pickupintent.Message{}})
	default:
		t.Fatal("not_complete did not come from the actual HTTP assessor")
	}
}

func TestPickupCompletionBridgeAssessmentErrorPreservesEffects(t *testing.T) {
	for _, tc := range []struct {
		name, arguments string
		status          int
		want            error
	}{
		{"provider-error", `{"decision":"complete"}`, http.StatusServiceUnavailable, commandcode.ErrUnavailable},
		{"invalid-result", `{"decision":"complete","authority":"forged"}`, http.StatusOK, commandcode.ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := pickupSetup(t)
			pickupBridgePrepare(t, f, testpickupreceiver.New(t), true)
			x := f.inbound(t, "No, that’s all")
			defer pickupBridgePreserveProcessing(t, f, x)()
			model := pickupBridgeModelServer(t, tc.arguments, tc.status, nil)
			orders, intents, snapshot := pickupEffects(t, f)
			out, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
			pickupUnchanged(t, f, orders, intents, snapshot)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
				t.Fatalf("assessment failure must retain error and have no effects: %+v %v want %v", out, err, tc.want)
			}
			if len(model.requests) != 1 {
				t.Fatal("expected one real HTTP assessment attempt")
			}
		})
	}
}

func TestPickupCompletionBridgeMissingReceiptPreservesEffects(t *testing.T) {
	f := pickupSetup(t)
	receiver := testpickupreceiver.New(t)
	pickupBridgePrepare(t, f, receiver, false)
	x := f.inbound(t, "No, that’s all")
	defer pickupBridgePreserveProcessing(t, f, x)()
	model := pickupBridgeModelServer(t, `{"decision":"complete"}`, http.StatusOK, nil)
	orders, intents, snapshot := pickupEffects(t, f)
	out, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
	pickupUnchanged(t, f, orders, intents, snapshot)
	if !errors.Is(err, cart.ErrPickupSummaryRequired) || !reflect.DeepEqual(out, cart.PickupSubmission{}) || len(model.requests) != 0 || len(receiver.Received()) != 0 {
		t.Fatalf("queued summary is not received presentation: %+v %v", out, err)
	}
}

func TestPickupCompletionBridgeStaleControlDuringAssessmentPreservesEffects(t *testing.T) {
	f := pickupSetup(t)
	pickupBridgePrepare(t, f, testpickupreceiver.New(t), true)
	x := f.inbound(t, "No, that’s all")
	defer pickupBridgePreserveProcessing(t, f, x)()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// Always unblock the owned HTTP handler before its server cleanup.
	model := pickupBridgeModelServer(t, `{"decision":"complete"}`, http.StatusOK, release)
	defer unblock()
	type result struct {
		out cart.PickupSubmission
		err error
	}
	finished := make(chan result, 1)
	done := make(chan struct{})
	orders, intents, snapshot := pickupEffects(t, f)
	go func() {
		defer close(done)
		out, err := f.p.AssessAndSubmit(t.Context(), x, int64(f.next), model.assessor)
		finished <- result{out, err}
	}()
	defer func() {
		unblock()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("bridge goroutine did not finish within client deadline")
		}
	}()
	select {
	case early := <-finished:
		pickupUnchanged(t, f, orders, intents, snapshot)
		t.Fatalf("bridge returned before real model HTTP handoff (functional RED): %+v %v", early.out, early.err)
	case <-model.requests:
	case <-t.Context().Done():
		t.Fatal("test canceled before model handoff")
	}
	if _, err := f.control.SetMode(t.Context(), f.token, f.local.ConversationID(), conversation.ModeStaff, 0, "bridge-takeover"); err != nil {
		t.Fatal(err)
	}
	// Public control mutation happens while the actual model HTTP response is held.
	unblock()
	var got result
	select {
	case got = <-finished:
	case <-t.Context().Done():
		t.Fatal("test canceled before bridge return")
	}
	pickupUnchanged(t, f, orders, intents, snapshot)
	state, err := f.control.Read(t.Context(), f.token, f.local.ConversationID())
	if !errors.Is(got.err, cart.ErrStale) || !reflect.DeepEqual(got.out, cart.PickupSubmission{}) || err != nil || state.Mode != conversation.ModeStaff || state.Version != 1 {
		t.Fatal("stale completion must not commit or return AI control", got, state, err)
	}
}
