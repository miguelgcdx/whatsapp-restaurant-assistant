package cart_test

import (
	"errors"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

func ambiguousRegister(t *testing.T, f *ambiguousFixture, slot int64, call cart.Call) cart.PickupExecution {
	t.Helper()
	x := f.x
	var err error
	x.Execution, err = f.s.RegisterExecution(t.Context(), f.local, x.Source.EventProviderID(), slot, call)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestAmbiguousEditBlocksAllNewPickupMaterialUntilActualResolution(t *testing.T) {
	f := ambiguousSetup(t)
	summary, prepared := f.prepare(t)
	f.present(t, summary)
	f.finish(t, prepared)
	f.turn(t, "remove that one")
	_, evidence := f.evidence(t, f.x)
	submit := ambiguousRegister(t, f, 200, pickupSubmitCall(t, evidence))
	ref := f.mark(t, 100, cart.UnresolvedRemove)
	view := f.view(t)
	prepareCall, err := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	newPrepare := ambiguousRegister(t, f, 201, prepareCall)
	before := f.effects(t)
	if out, err := f.p.Prepare(t.Context(), newPrepare, f.snapshot.ID, f.snapshot.Version); !errors.Is(err, cart.ErrUnresolvedEdit) || !reflect.DeepEqual(out, cart.PreparedPickupSummary{}) {
		t.Fatalf("new prepare despite unchanged version: %+v %v", out, err)
	}
	if out, err := f.p.CompletionContext(t.Context(), f.x); !errors.Is(err, cart.ErrUnresolvedEdit) || !reflect.DeepEqual(out, cart.PickupCompletionContext{}) {
		t.Fatalf("new context despite unchanged version: %+v %v", out, err)
	}
	if out, err := f.p.Submit(t.Context(), submit, evidence); !errors.Is(err, cart.ErrUnresolvedEdit) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("previously assessed complete must not bypass marker: %+v %v", out, err)
	}
	f.unchanged(t, before)
	if len(before.orders.Items) != 0 || len(before.intents) != 1 {
		t.Fatalf("unexpected order/receipt before resolution: %+v", before)
	}
	// not_complete is only a decision, not persistence/resolution/abandonment.
	notComplete := evidence
	notComplete.Decision = cart.PickupNotComplete
	denied := ambiguousRegister(t, f, 202, pickupSubmitCall(t, notComplete))
	if out, err := f.p.Submit(t.Context(), denied, notComplete); !errors.Is(err, cart.ErrPickupConfirmationRequired) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("not_complete: %+v %v", out, err)
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t), view) {
		t.Fatal("completion decision cleared marker")
	}
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	work, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 203, view, ref, call)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.p.Submit(t.Context(), submit, evidence); !errors.Is(err, cart.ErrUnresolvedEdit) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("admission is not resolution: %+v %v", out, err)
	}
	f.snapshot = ambiguousExecute(t, f, work).Cart
	if len(f.view(t).Entries()) != 0 {
		t.Fatal("actual successful edit did not resolve record")
	}
	if out, err := f.p.Submit(t.Context(), submit, evidence); !errors.Is(err, cart.ErrPickupSummaryRequired) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("old summary must stay invalid after edit: %+v %v", out, err)
	}
	f.finish(t, f.x)
	fresh, x := f.prepare(t)
	if fresh.CandidateID == summary.CandidateID || fresh.CartVersion != f.snapshot.Version || fresh.UnitCount != 1 {
		t.Fatalf("fresh summary: %+v", fresh)
	}
	f.present(t, fresh)
	f.finish(t, x)
	f.turn(t, "No, that is all")
	_, current := f.evidence(t, f.x)
	x = ambiguousRegister(t, f, 204, pickupSubmitCall(t, current))
	out, err := f.p.Submit(t.Context(), x, current)
	if err != nil || out.Order.Status != cart.PickupSubmitted || out.Order.Confirmed.ID != fresh.CandidateID || out.Order.Confirmed.UnitCount != 1 || out.Receipt.Text == "" {
		t.Fatalf("fresh completion: %+v %v", out, err)
	}
	after := f.effects(t)
	if len(after.orders.Items) != 1 || len(after.intents) != 3 || !reflect.DeepEqual(after.cart, f.snapshot) {
		t.Fatalf("one order/receipt and unchanged resolved cart: %+v", after)
	}
}

func TestAmbiguousEditCommittedReplaySurvivesLaterMarkerWithCurrentAuthority(t *testing.T) {
	f := ambiguousSetup(t)
	summary, prepared := f.prepare(t)
	f.present(t, summary)
	f.finish(t, prepared)
	f.turn(t, "That is all; remove that one")
	_, evidence := f.evidence(t, f.x)
	x := ambiguousRegister(t, f, 200, pickupSubmitCall(t, evidence))
	committed, err := f.p.Submit(t.Context(), x, evidence)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a delayed host marker after a submission has already committed.
	// The same actual claim is still current. No stale/retired claim is invented
	// to make historical replay appear permitted after a new inbound.
	f.mark(t, 100, cart.UnresolvedRemove)
	view := f.view(t)
	before := f.effects(t)
	out, err := f.p.Submit(t.Context(), x, evidence)
	if err != nil || !reflect.DeepEqual(out, committed) {
		t.Fatalf("committed replay must precede material guard: %+v %v", out, err)
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t), view) {
		t.Fatal("historical replay cleared later ambiguity")
	}
	f.finish(t, f.x)
	before = f.effects(t)
	if out, err := f.p.Submit(t.Context(), x, evidence); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatalf("replay still requires actual current authority: %+v %v", out, err)
	}
	f.unchanged(t, before)
}

func TestAmbiguousEditUnmarkedClearNativeAndPickupRemainNormal(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "add two sandwiches")
	call := ambiguousCall(t, f.snapshot, add(t, 2))
	ref, err := f.s.AdmitNativeEdit(t.Context(), f.local, f.claim, 100, call)
	if err != nil {
		t.Fatal(err)
	}
	before := f.snapshot
	f.snapshot = ambiguousExecute(t, f, ref).Cart
	if f.snapshot.Version.Int64() != before.Version.Int64()+1 || len(f.snapshot.Units) != len(before.Units)+2 {
		t.Fatalf("clear edit normal path: %+v", f.snapshot)
	}
	f.finish(t, f.x)
	summary, prepared := f.prepare(t)
	f.present(t, summary)
	f.finish(t, prepared)
	f.turn(t, "No, that is all")
	_, evidence := f.evidence(t, f.x)
	x := ambiguousRegister(t, f, 200, pickupSubmitCall(t, evidence))
	out, err := f.p.Submit(t.Context(), x, evidence)
	if err != nil || out.Order.Status != cart.PickupSubmitted || out.Order.Confirmed.UnitCount != len(f.snapshot.Units) || out.Receipt.Text == "" {
		t.Fatalf("unmarked completion: %+v %v", out, err)
	}
}
