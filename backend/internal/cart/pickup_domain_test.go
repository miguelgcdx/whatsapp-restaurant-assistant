package cart_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/staff"
)

type pickupTransport func(*http.Request) (*http.Response, error)

func (f pickupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pickupFixture struct {
	*draftFixture
	p        *cart.PickupStore
	snapshot cart.Snapshot
	gate     *menugate.Gate
	control  *conversation.Store
	token    string
	current  cart.PickupExecution
	next     int
	reply    *string
}

func pickupSetup(t *testing.T) *pickupFixture {
	t.Helper()
	f := draftSetup(t, 650)
	selected := previewSelected(t, f, 2, cart.PickupFulfillment())
	note, e := cart.SetNote([]cart.UnitID{selected.Cart.Units[0].ID}, "  no salt 🥪\n")
	if e != nil {
		t.Fatal(e)
	}
	s := apply(t, f.fixture, cart.Result{Cart: selected.Cart}, note).Cart
	if e = f.jobs.CompleteJob(t.Context(), f.job, "legacy unchanged", time.Now()); e != nil {
		t.Fatal(e)
	}
	control, token := menuControl(t, f.fixture)
	if _, e = control.Initialize(t.Context(), token, f.local.ConversationID(), conversation.ModeAI, "pickup-init"); e != nil {
		t.Fatal(e)
	}
	p, e := cart.NewPickupStore(f.s, restaurant, cart.PickupCompletionAssessmentVersion)
	if e != nil {
		t.Fatal(e)
	}
	reply := "Local placeholder (not presented)"
	client, e := commandcode.New(commandcode.Config{APIKey: "synthetic-key", Transport: pickupTransport(func(*http.Request) (*http.Response, error) {
		text, _ := json.Marshal(reply)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"message":{"role":"assistant","content":` + string(text) + `},"finish_reason":"stop"}]}`))}, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	g, e := menugate.Open(t.Context(), f.dsn, restaurant, client, 100)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(g.Close)
	return &pickupFixture{draftFixture: f, p: p, snapshot: s, gate: g, control: control, token: token, reply: &reply}
}
func (f *pickupFixture) inbound(t *testing.T, text string) cart.PickupExecution {
	t.Helper()
	f.next++
	event := foundation.Event{ID: "pickup-" + time.Now().Format("150405.000000000"), Sender: f.job.Event.Sender, Text: text}
	a, fresh, e := f.s.AcceptMenu(t.Context(), f.local, event, time.Now())
	if e != nil || !fresh {
		t.Fatal(a, fresh, e)
	}
	c, ok, e := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
	if e != nil || !ok {
		t.Fatal(c, ok, e)
	}
	f.current = cart.PickupExecution{Local: f.local, Source: c.Source(), Claim: c.Job(), ControlVersion: version(t, 0)}
	return f.current
}
func (f *pickupFixture) register(t *testing.T, x cart.PickupExecution, c cart.Call) cart.PickupExecution {
	t.Helper()
	e, err := f.s.RegisterExecution(t.Context(), f.local, x.Source.EventProviderID(), int64(f.next), c)
	if err != nil {
		t.Fatal(err)
	}
	x.Execution = e
	return x
}
func (f *pickupFixture) finish(t *testing.T, x cart.PickupExecution) menugate.Receipt {
	t.Helper()
	r, e := f.gate.RunAndQueue(t.Context(), x.Source, x.Claim)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *pickupFixture) prepare(t *testing.T) (cart.PreparedPickupSummary, cart.PickupExecution) {
	t.Helper()
	x := f.inbound(t, "Show the final pickup summary")
	c, e := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	if e != nil {
		t.Fatal(e)
	}
	x = f.register(t, x, c)
	s, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version)
	if e != nil {
		t.Fatal("prepare valid actual menu claim:", e)
	}
	return s, x
}
func (f *pickupFixture) present(t *testing.T, s cart.PreparedPickupSummary) cart.PickupPresentation {
	t.Helper()
	r, e := f.p.RecordLocalPresentation(t.Context(), f.local, cart.PickupLocalPresentation{IntentID: s.Intent.ID, CandidateID: s.CandidateID, ReceiverReference: "fake-receiver:" + s.Intent.ID.String(), ReceivedTextDigest: sha256.Sum256([]byte(s.Intent.Text))})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *pickupFixture) evidence(t *testing.T, x cart.PickupExecution) (cart.PickupCompletionContext, cart.PickupCompletionEvidence) {
	t.Helper()
	c, e := f.p.CompletionContext(t.Context(), x)
	if e != nil {
		t.Fatal(e)
	}
	return c, cart.PickupCompletionEvidence{CandidateID: c.CandidateID, InboundEventID: c.InboundEventID, ContextDigest: c.Digest, AssessmentVersion: cart.PickupCompletionAssessmentVersion, Decision: cart.PickupComplete}
}
func pickupSubmitCall(t *testing.T, e cart.PickupCompletionEvidence) cart.Call {
	t.Helper()
	c, err := cart.SubmitPickupCall(e)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestPickupDomainConnectedSynthetic(t *testing.T) {
	f := pickupSetup(t)
	s, x := f.prepare(t)
	exact := "Pickup summary (not submitted)\nItems:\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"  no salt 🥪\\n\"\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"\"\nFulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD 13.00\nDelivery fee: USD 0.00\nTotal: USD 13.00\nPreparation estimate: 10 minutes (provisional, preparation only).\nWould you like anything else?"
	if s.Intent.Text != exact || s.ClosingQuestion != "Would you like anything else?" || s.UnitCount != 2 || s.Total.AmountMinor != 1300 || s.Estimate == nil || s.Estimate.Minutes != 10 || !s.Estimate.Provisional || s.Estimate.AsOf.IsZero() || s.Intent.TextDigest != sha256.Sum256([]byte(exact)) {
		t.Fatalf("complete stored summary: %+v", s)
	}
	// Queue/acceptance alone is not presentation, and returned copies grant nothing.
	if _, e := f.p.CompletionContext(t.Context(), x); !errors.Is(e, cart.ErrPickupSummaryRequired) {
		t.Fatalf("unpresented context: %v", e)
	}
	bad := cart.PickupLocalPresentation{IntentID: s.Intent.ID, CandidateID: s.CandidateID, ReceiverReference: "fake-bad", ReceivedTextDigest: sha256.Sum256([]byte("rewritten"))}
	if _, e := f.p.RecordLocalPresentation(t.Context(), f.local, bad); !errors.Is(e, cart.ErrConflict) {
		t.Fatal(e)
	}
	received := f.present(t, s)
	if received.ID.String() == "" || received.ReceivedAt.IsZero() {
		t.Fatal(received)
	}
	f.finish(t, x)
	x = f.inbound(t, "No, that’s all")
	c, e := f.evidence(t, x)
	if c.InboundEventID != x.Source.EventUUID() || c.InboundText != "No, that’s all" || c.SummaryText != exact || c.History == nil || len(c.History) != 0 || c.PresentationID != received.ID || c.Digest == [32]byte{} {
		t.Fatalf("stored correlation: %+v", c)
	}
	rejected := e
	rejected.Decision = cart.PickupNotComplete
	denied := f.register(t, x, pickupSubmitCall(t, rejected))
	if _, err := f.p.Submit(t.Context(), denied, rejected); !errors.Is(err, cart.ErrPickupConfirmationRequired) {
		t.Fatal(err)
	}
	f.next++
	x = f.register(t, x, pickupSubmitCall(t, e))
	var wg sync.WaitGroup
	out := make([]cart.PickupSubmission, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { out[i], errs[i] = f.p.Submit(t.Context(), x, e) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(out[0], out[1]) {
		t.Fatal(out, errs)
	}
	order := out[0].Order
	if order.Status != cart.PickupSubmitted || order.Payment.State != cart.PickupUnpaid || order.Payment.CollectedAt != nil || order.Total != s.Total || order.Confirmed.ID != s.CandidateID || order.Confirmed.DeliveryFee.AmountMinor != 0 || out[0].Receipt.Text == "" {
		t.Fatal(out[0])
	}
	transport, err := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if err != nil || len(transport) != 2 || transport[0].Kind != "summary" || transport[0].Text != exact || transport[1].Kind != "receipt" || transport[1].Text != out[0].Receipt.Text || transport[1].ID != out[0].Receipt.ID {
		t.Fatal(transport, err)
	}
	subject := cart.PickupStaffSubject("menu-operator")
	page, err := f.p.ListOrders(t.Context(), subject, cart.PickupPageRequest{})
	if err != nil || len(page.Items) != 1 || page.Items[0] != order.PickupOrderHeader {
		t.Fatal(page, err)
	}
	units, err := f.p.ReadUnits(t.Context(), subject, order.ID, s.CandidateID, cart.PickupPageRequest{Limit: 1})
	if err != nil || len(units.Items) != 1 || units.NextCursor == nil || units.Items[0].Note != "  no salt 🥪\n" || units.Items[0].Name != "Fictional sandwich" || units.Items[0].Price != (cart.PickupMoney{AmountMinor: 650, Currency: "USD", Exponent: 2}) {
		t.Fatal(units, err)
	}
	next, err := f.p.ReadUnits(t.Context(), subject, order.ID, s.CandidateID, cart.PickupPageRequest{Limit: 1, Cursor: *units.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != nil || next.Items[0].Note != "" {
		t.Fatal(next, err)
	}
	if _, err = f.p.ListOrders(t.Context(), subject, cart.PickupPageRequest{Cursor: *units.NextCursor}); !errors.Is(err, cart.ErrInput) {
		t.Fatal("wrong resource cursor", err)
	}
	// Mutating the draft cannot rewrite the confirmed snapshot or original replay.
	current := apply(t, f.fixture, cart.Result{Cart: f.snapshot}, add(t, 1))
	f.snapshot = current.Cart
	replay, err := f.p.Submit(t.Context(), x, e)
	if err != nil || !reflect.DeepEqual(replay, out[0]) {
		t.Fatal(replay, err)
	}
	got, err := f.p.ReadOrder(t.Context(), subject, order.ID)
	if err != nil || !reflect.DeepEqual(got, order) {
		t.Fatal(got, err)
	}
	// Collection is independent of fulfillment. Original action replay survives advance.
	paidReq := cart.PickupStaffActionRequest{Action: cart.PickupMarkPaid, ExpectedVersion: order.Version, IdempotencyKey: "collect-once"}
	paid, err := f.p.ApplyAction(t.Context(), subject, order.ID, paidReq)
	if err != nil || paid.Status != cart.PickupSubmitted || paid.Payment.State != cart.PickupPaid || paid.Payment.CollectedAt == nil {
		t.Fatal(paid, err)
	}
	if _, err = f.p.ApplyAction(t.Context(), subject, order.ID, cart.PickupStaffActionRequest{Action: cart.PickupCancel, ExpectedVersion: paid.Version, IdempotencyKey: "paid-cancel"}); !errors.Is(err, cart.ErrPickupPolicyUnresolved) {
		t.Fatal(err)
	}
	live := paid
	for i, a := range []cart.PickupStaffAction{cart.PickupAccept, cart.PickupStartPreparing, cart.PickupMarkReady, cart.PickupMarkPickedUp} {
		live, err = f.p.ApplyAction(t.Context(), subject, order.ID, cart.PickupStaffActionRequest{Action: a, ExpectedVersion: live.Version, IdempotencyKey: string(a)})
		if err != nil || live.Payment.State != cart.PickupPaid || live.Version.Int64() != int64(i+2) {
			t.Fatal(live, err)
		}
	}
	old, err := f.p.ApplyAction(t.Context(), subject, order.ID, paidReq)
	if err != nil || !reflect.DeepEqual(old, paid) {
		t.Fatal(old, err)
	}
	changed := paidReq
	changed.Action = cart.PickupReject
	if _, err = f.p.ApplyAction(t.Context(), subject, order.ID, changed); !errors.Is(err, cart.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.p.ApplyAction(t.Context(), subject, order.ID, cart.PickupStaffActionRequest{Action: cart.PickupAccept, ExpectedVersion: live.Version, IdempotencyKey: "terminal"}); !errors.Is(err, cart.ErrPickupInvalidTransition) {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(t.Context(), f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	if err = members.RevokeMember(t.Context(), string(subject)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.ApplyAction(t.Context(), subject, order.ID, paidReq); !errors.Is(err, cart.ErrPickupForbidden) {
		t.Fatal("replay after revocation", err)
	}
}

func TestPickupDomainPublicEmptyStaffAndConstructor(t *testing.T) {
	f := pickupSetup(t)
	page, e := f.p.ListOrders(t.Context(), "menu-operator", cart.PickupPageRequest{})
	if e != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatal(page, e)
	}
	if _, e = f.p.ListOrders(t.Context(), "not-a-member", cart.PickupPageRequest{}); !errors.Is(e, cart.ErrPickupForbidden) {
		t.Fatal(e)
	}
	for _, policy := range []string{"", "model-policy"} {
		if _, e = cart.NewPickupStore(f.s, restaurant, policy); !errors.Is(e, cart.ErrInput) {
			t.Fatal(e)
		}
	}
	// Existing preview keeps its deliberately different workload contract.
	call, _ := cart.CaptureSummaryPreviewCall(f.snapshot.ID, f.snapshot.Version)
	legacy := f.plan(t, call)
	// Completed old claim cannot be reused, even though the descriptor is valid.
	if _, e = f.s.ExecuteSummaryPreview(t.Context(), f.local, legacy, f.job); !errors.Is(e, foundation.ErrStaleLease) {
		t.Fatal(e)
	}
}
