package cart_test

import (
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
)

// PRESERVE: additive descriptor routing and shared quote extraction leave each
// old call kind working through its original public result seam.
func TestSummaryPreviewPreservesOldCallKinds(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	execute := func(call cart.Call) cart.Result {
		t.Helper()
		p := f.plan(t, call)
		r, e := f.s.Execute(ctx, f.local, p, f.job)
		if e != nil || r.OperationID != p.OperationID() {
			t.Fatalf("legacy %+v %v", r, e)
		}
		same, e := f.s.RegisterExecution(ctx, f.local, f.event, f.slot, call)
		if e != nil || same != p {
			t.Fatal(same, e)
		}
		again, e := f.s.Execute(ctx, f.local, p, f.job)
		if e != nil || !reflect.DeepEqual(again, r) {
			t.Fatal(again, e)
		}
		return r
	}
	created := execute(cart.CreateCall())
	edit, e := cart.EditCall(created.Cart.ID, created.Cart.Version, add(t, 2))
	if e != nil {
		t.Fatal(e)
	}
	added := execute(edit)
	note, e := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, " exact note\n")
	if e != nil {
		t.Fatal(e)
	}
	edit, e = cart.EditCall(added.Cart.ID, added.Cart.Version, note)
	if e != nil {
		t.Fatal(e)
	}
	noted := execute(edit)
	remove, e := cart.Remove([]cart.UnitID{noted.Cart.Units[1].ID})
	if e != nil {
		t.Fatal(e)
	}
	edit, e = cart.EditCall(noted.Cart.ID, noted.Cart.Version, remove)
	if e != nil {
		t.Fatal(e)
	}
	removed := execute(edit)
	read, e := cart.ReadCall(removed.Cart.ID)
	if e != nil {
		t.Fatal(e)
	}
	readback := execute(read)
	if removed.Cart.Version.Int64() != 3 || len(removed.Cart.Units) != 1 || removed.Cart.Subtotal != 650 || removed.Cart.Units[0].ID != added.Cart.Units[0].ID || removed.Cart.Units[0].Note != " exact note\n" || !reflect.DeepEqual(readback.Cart, removed.Cart) {
		t.Fatal(removed, readback)
	}
	selected := f.execute(t, setCall(t, removed.Cart, delivery(t, "a", "p")))
	if selected.Cart.Version.Int64() != 4 || selected.Quote.Total != 950 || selected.Quote.Fee != 300 || selected.Quote.PaymentMethod != "cash_on_delivery" || !reflect.DeepEqual(selected.Cart.Units, removed.Cart.Units) {
		t.Fatal(selected)
	}
	call := quoteCall(t, selected.Cart)
	p := f.plan(t, call)
	first, e := f.s.ExecuteDraft(ctx, f.local, p, f.job)
	if e != nil || first.Quote != selected.Quote {
		t.Fatal(first, e)
	}
	if _, e = f.conn.Exec(ctx, `UPDATE app.restaurant_settings SET flat_fee_minor=7,version=version+1`); e != nil {
		t.Fatal(e)
	}
	fresh, e := f.s.ExecuteDraft(ctx, f.local, p, f.job)
	if e != nil || fresh.Quote.Fee != 7 || fresh.Quote.Total != 657 || fresh.Quote.SettingsVersion != 1 || !reflect.DeepEqual(fresh.Cart, selected.Cart) {
		t.Fatal(fresh, e)
	}
}
