package cart_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/staff"
)

// Additional conversations share one owned PostgreSQL fixture, never a copied
// source/dependency tree or one database per workload row.
func pickupPeer(t *testing.T, root *pickupFixture, n int) *pickupFixture {
	t.Helper()
	sender := fmt.Sprintf("pickup-peer-%d", n)
	local, e := root.s.ProvisionLocal(t.Context(), "fictional-business", sender)
	if e != nil {
		t.Fatal(e)
	}
	f := fixture{s: root.s, cat: root.cat, local: local, dsn: root.dsn}
	r := apply(t, f, create(t, f), add(t, 2))
	event := foundation.Event{ID: sender, Sender: sender, Text: "select pickup"}
	now := time.Now()
	if _, e = root.jobs.Accept(t.Context(), event, now); e != nil {
		t.Fatal(e)
	}
	job, ok, e := root.jobs.ClaimJob(t.Context(), now, time.Minute)
	if e != nil || !ok || job.Event.ID != event.ID {
		t.Fatal(job, ok, e)
	}
	call, _ := cart.SetFulfillmentCall(r.Cart.ID, r.Cart.Version, cart.PickupFulfillment())
	plan, e := root.s.RegisterExecution(t.Context(), local, event.ID, 0, call)
	if e != nil {
		t.Fatal(e)
	}
	selected, e := root.s.ExecuteDraft(t.Context(), local, plan, job)
	if e != nil {
		t.Fatal(e)
	}
	if e = root.jobs.CompleteJob(t.Context(), job, "legacy setup", now); e != nil {
		t.Fatal(e)
	}
	if _, e = root.control.Initialize(t.Context(), root.token, local.ConversationID(), conversation.ModeAI, sender); e != nil {
		t.Fatal(e)
	}
	return &pickupFixture{draftFixture: &draftFixture{fixture: f, jobs: root.jobs, job: job, event: event.ID, conn: root.conn}, p: root.p, snapshot: selected.Cart, gate: root.gate, control: root.control, token: root.token, reply: root.reply}
}
func pickupOrdered(t *testing.T, f *pickupFixture) cart.PickupSubmission {
	t.Helper()
	s, x := f.prepare(t)
	f.present(t, s)
	f.finish(t, x)
	x = f.inbound(t, "Nothing else, thank you. Please place the order.")
	_, ev := f.evidence(t, x)
	x = f.register(t, x, pickupSubmitCall(t, ev))
	o, e := f.p.Submit(t.Context(), x, ev)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func pickupAction(t *testing.T, f *pickupFixture, o cart.PickupOrder, a cart.PickupStaffAction) cart.PickupOrder {
	t.Helper()
	out, e := f.p.ApplyAction(t.Context(), "menu-operator", o.ID, cart.PickupStaffActionRequest{Action: a, ExpectedVersion: o.Version, IdempotencyKey: string(a)})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPickupDomainStaffScopeConcurrencyAndUnpaidFulfillment(t *testing.T) {
	f := pickupSetup(t)
	first := pickupOrdered(t, f)
	peer := pickupPeer(t, f, 1)
	second := pickupOrdered(t, peer)
	subject := cart.PickupStaffSubject("menu-operator")
	page, e := f.p.ListOrders(t.Context(), subject, cart.PickupPageRequest{Limit: 1})
	if e != nil || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal(page, e)
	}
	next, e := f.p.ListOrders(t.Context(), subject, cart.PickupPageRequest{Limit: 1, Cursor: *page.NextCursor})
	if e != nil || len(next.Items) != 1 || next.NextCursor != nil || next.Items[0].ID == page.Items[0].ID {
		t.Fatal(next, e)
	}
	members, e := staff.OpenStore(t.Context(), f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer members.Close()
	if _, e = members.EnsureMember(t.Context(), "second-operator"); e != nil {
		t.Fatal(e)
	}
	for _, cursor := range []string{*page.NextCursor + "x", strings.Repeat("x", 2049), "forged"} {
		if _, e = f.p.ListOrders(t.Context(), subject, cart.PickupPageRequest{Cursor: cursor}); !errors.Is(e, cart.ErrInput) {
			t.Fatal(e)
		}
	}
	if _, e = f.p.ListOrders(t.Context(), "second-operator", cart.PickupPageRequest{Cursor: *page.NextCursor}); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e = f.p.ReadUnits(t.Context(), subject, first.Order.ID, second.Order.Confirmed.ID, cart.PickupPageRequest{}); !errors.Is(e, cart.ErrNotFound) {
		t.Fatal(e)
	}
	before, intents, snapshot := pickupEffects(t, f)
	if _, e = f.p.ApplyAction(t.Context(), subject, first.Order.ID, cart.PickupStaffActionRequest{Action: cart.PickupMarkReady, ExpectedVersion: first.Order.Version, IdempotencyKey: "skip"}); !errors.Is(e, cart.ErrPickupInvalidTransition) {
		t.Fatal(e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	req := cart.PickupStaffActionRequest{Action: cart.PickupAccept, ExpectedVersion: first.Order.Version, IdempotencyKey: "same-key"}
	var wg sync.WaitGroup
	out := make([]cart.PickupOrder, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() { out[i], errs[i] = f.p.ApplyAction(t.Context(), subject, first.Order.ID, req) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(out[0], out[1]) {
		t.Fatal(out, errs)
	}
	after, e := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if e != nil || len(after) != len(intents)+1 {
		t.Fatal(after, e)
	}
	// A key is scoped by canonical order resource as well as actor and method.
	rejected, e := f.p.ApplyAction(t.Context(), subject, second.Order.ID, cart.PickupStaffActionRequest{Action: cart.PickupReject, ExpectedVersion: second.Order.Version, IdempotencyKey: "same-key"})
	if e != nil || rejected.Status != cart.PickupRejected || rejected.Payment.State != cart.PickupUnpaid {
		t.Fatal(rejected, e)
	}
	live := out[0]
	for _, a := range []cart.PickupStaffAction{cart.PickupStartPreparing, cart.PickupMarkReady, cart.PickupMarkPickedUp} {
		live = pickupAction(t, f, live, a)
		if live.Payment.State != cart.PickupUnpaid || live.Payment.CollectedAt != nil {
			t.Fatal("fulfillment collected payment", live)
		}
	}
	if !reflect.DeepEqual(live.PermittedActions, []cart.PickupStaffAction{cart.PickupMarkPaid}) {
		t.Fatal(live.PermittedActions)
	}
	live = pickupAction(t, f, live, cart.PickupMarkPaid)
	if live.Status != cart.PickupPickedUp || live.Payment.State != cart.PickupPaid || len(live.PermittedActions) != 0 {
		t.Fatal(live)
	}
}

func TestPickupDomainAuthoritativeWorkloadRefresh(t *testing.T) {
	f := pickupSetup(t)
	orders := []cart.PickupOrder{pickupOrdered(t, f).Order}
	peers := []*pickupFixture{f}
	for i := 1; i < 3; i++ {
		peer := pickupPeer(t, f, i)
		peers = append(peers, peer)
		orders = append(orders, pickupOrdered(t, peer).Order)
	}
	candidate := pickupPeer(t, f, 10)
	s, x := candidate.prepare(t)
	if s.Estimate == nil || s.Estimate.Minutes != 10 {
		t.Fatal("unaccepted orders entered workload", s)
	}
	candidate.present(t, s)
	candidate.finish(t, x)
	for i := range orders {
		orders[i] = pickupAction(t, peers[i], orders[i], cart.PickupAccept)
	}
	x = candidate.inbound(t, "Please place it")
	_, ev := candidate.evidence(t, x)
	x = candidate.register(t, x, pickupSubmitCall(t, ev))
	submitted, e := candidate.p.Submit(t.Context(), x, ev)
	if e != nil || submitted.Order.Estimate == nil || submitted.Order.Estimate.Minutes != 20 || !submitted.Order.Estimate.AsOf.After(s.Estimate.AsOf) || submitted.Order.Total != s.Total || strings.Contains(submitted.Receipt.Text, "anything else?") || !strings.Contains(submitted.Receipt.Text, "20 minutes") {
		t.Fatal(submitted, e)
	}
	for i := range orders {
		orders[i] = pickupAction(t, peers[i], orders[i], cart.PickupStartPreparing)
	}
	fourth := pickupAction(t, candidate, submitted.Order, cart.PickupAccept)
	if _, e = candidate.p.ApplyAction(t.Context(), "menu-operator", fourth.ID, cart.PickupStaffActionRequest{Action: cart.PickupStartPreparing, ExpectedVersion: fourth.Version, IdempotencyKey: "capacity-full"}); !errors.Is(e, cart.ErrPickupInvalidTransition) {
		t.Fatal("released active capacity", e)
	}
	orders[0] = pickupAction(t, f, orders[0], cart.PickupMarkReady)
	fourth = pickupAction(t, candidate, fourth, cart.PickupStartPreparing)
	if fourth.PreparationStartedAt == nil || fourth.Payment.State != cart.PickupUnpaid {
		t.Fatal(fourth)
	}
}

func TestPickupDomainGapElapsedWorkloadAndOverdue(t *testing.T) {
	f := pickupSetup(t)
	peers := []*pickupFixture{f}
	orders := []cart.PickupOrder{pickupOrdered(t, f).Order}
	for i := 1; i < 6; i++ {
		peer := pickupPeer(t, f, i)
		peers = append(peers, peer)
		orders = append(orders, pickupOrdered(t, peer).Order)
	}
	ids := []string{}
	for i := range orders {
		orders[i] = pickupAction(t, peers[i], orders[i], cart.PickupAccept)
		if i < 3 {
			orders[i] = pickupAction(t, peers[i], orders[i], cart.PickupStartPreparing)
			ids = append(ids, orders[i].ID.String())
		}
	}
	// Only fixture clock setup uses SQL. All six real orders and legal states
	// were created publicly; no storage-level success assertion follows.
	if _, e := f.conn.Exec(t.Context(), `UPDATE app.pickup_orders SET preparation_started_at=clock_timestamp()-interval '6 minutes' WHERE id=ANY($1::uuid[])`, ids); e != nil {
		t.Fatal(e)
	}
	candidate := pickupPeer(t, f, 10)
	summary, first := candidate.prepare(t)
	if summary.Estimate == nil || summary.Estimate.Minutes != 24 || !summary.Estimate.Provisional || summary.Estimate.AsOf.IsZero() || summary.Total.AmountMinor != 1300 || !strings.Contains(summary.Intent.Text, "24 minutes") {
		t.Fatalf("three six-minute Preparing + three Accepted: %+v", summary)
	}
	candidate.present(t, summary)
	candidate.finish(t, first)
	x := candidate.inbound(t, "Please place it")
	_, evidence := candidate.evidence(t, x)
	x = candidate.register(t, x, pickupSubmitCall(t, evidence))
	// Age one still-Preparing order past ten minutes after assessment. This is
	// workload-only freshness, not a cart/price change or automatic readiness.
	if _, e := f.conn.Exec(t.Context(), `UPDATE app.pickup_orders SET preparation_started_at=clock_timestamp()-interval '11 minutes' WHERE id=$1`, ids[0]); e != nil {
		t.Fatal(e)
	}
	for i := range orders {
		var e error
		orders[i], e = f.p.ReadOrder(t.Context(), "menu-operator", orders[i].ID)
		if e != nil {
			t.Fatal(e)
		}
	}
	out, e := candidate.p.Submit(t.Context(), x, evidence)
	if e != nil || out.Order.Estimate != nil || out.Order.Status != cart.PickupSubmitted || out.Order.Payment.State != cart.PickupUnpaid || out.Order.Payment.CollectedAt != nil || out.Order.Total != summary.Total || out.Order.Confirmed.UnitCount != 2 || !strings.Contains(out.Receipt.Text, "unavailable (active work overdue)") {
		t.Fatal(out, e)
	}
	detail, e := candidate.p.ReadOrder(t.Context(), "menu-operator", out.Order.ID)
	if e != nil || !reflect.DeepEqual(detail, out.Order) {
		t.Fatal(detail, e)
	}
	replay, e := candidate.p.Submit(t.Context(), x, evidence)
	if e != nil || !reflect.DeepEqual(replay, out) {
		t.Fatal(replay, e)
	}
	intents, e := candidate.p.ReadLocalIntents(t.Context(), candidate.local, cart.PickupIntentID{}, 100)
	if e != nil || len(intents) != 2 || intents[1].Kind != "receipt" || intents[1].Text != out.Receipt.Text {
		t.Fatal(intents, e)
	}
	for i, order := range orders {
		actual, e := f.p.ReadOrder(t.Context(), "menu-operator", order.ID)
		if e != nil || !reflect.DeepEqual(actual, order) {
			t.Fatalf("workload order %d auto-progressed: %+v %v", i, actual, e)
		}
	}
	unknown := pickupPeer(t, f, 11)
	unavailable, _ := unknown.prepare(t)
	if unavailable.Estimate != nil || !strings.Contains(unavailable.Intent.Text, "unavailable (active work overdue)") {
		t.Fatal(unavailable)
	}
	before, effects, snapshot := pickupEffects(t, peers[3])
	if len(before.Items) != 7 {
		t.Fatal(before)
	}
	blocked, e := f.p.ApplyAction(t.Context(), "menu-operator", orders[3].ID, cart.PickupStaffActionRequest{Action: cart.PickupStartPreparing, ExpectedVersion: orders[3].Version, IdempotencyKey: "overdue-is-not-capacity"})
	if !errors.Is(e, cart.ErrPickupInvalidTransition) || !reflect.DeepEqual(blocked, cart.PickupOrder{}) {
		t.Fatal(blocked, e)
	}
	pickupUnchanged(t, peers[3], before, effects, snapshot)
}
