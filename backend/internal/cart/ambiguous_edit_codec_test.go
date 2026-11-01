package cart_test

import (
	"errors"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
)

func TestAmbiguousEditDescriptorValidation(t *testing.T) {
	id, err := cart.ParseCartID("b0000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	v := version(t, 7)
	for _, kind := range []cart.UnresolvedEditKind{cart.UnresolvedAdd, cart.UnresolvedRemove, cart.UnresolvedSetNote} {
		call, err := cart.UnresolvedEditCall(id, v, kind)
		if err != nil || reflect.DeepEqual(call, cart.Call{}) {
			t.Fatalf("supported ambiguity descriptor %q: %+v %v", kind, call, err)
		}
	}
	for _, tc := range []struct {
		name string
		id   cart.CartID
		kind cart.UnresolvedEditKind
	}{
		{"zero-cart", cart.CartID{}, cart.UnresolvedRemove},
		{"zero-kind", id, ""},
		{"unknown-kind", id, "replace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := cart.UnresolvedEditCall(tc.id, v, tc.kind)
			if !errors.Is(err, cart.ErrInput) || !reflect.DeepEqual(out, cart.Call{}) {
				t.Fatalf("out=%+v error=%v want zero/ErrInput", out, err)
			}
		})
	}
}

func TestAmbiguousEditDescriptorCannotAdmitAsConcreteNativeEdit(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	call, err := cart.UnresolvedEditCall(f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove)
	if err != nil {
		t.Fatal(err)
	}
	before := f.effects(t)
	out, err := f.s.AdmitNativeEdit(t.Context(), f.local, f.claim, 100, call)
	if !errors.Is(err, cart.ErrInput) || out != (cart.NativeActionRef{}) {
		t.Fatalf("ambiguity is not concrete native work: %+v %v", out, err)
	}
	f.unchanged(t, before)
	// Failure must not reserve the host slot or invent a marker.
	if got, err := f.s.AdmitNativeEdit(t.Context(), f.local, f.claim, 100, ambiguousCall(t, f.snapshot, add(t, 1))); err != nil || got == (cart.NativeActionRef{}) {
		t.Fatalf("rejected descriptor reserved slot: %+v %v", got, err)
	}
	if len(f.view(t).Entries()) != 0 {
		t.Fatal("invalid admission implicitly marked ambiguity")
	}
}

func TestAmbiguousEditInvalidProposalIsNotPersistentAmbiguity(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "maybe")
	before := f.effects(t)
	for _, proposal := range []string{`{"action":"remove","unit_ids":[]}`, `{"action":"unresolved_target","kind":"remove"}`, `{"action":"resolve_target","pending_selector":"first","edit":{"action":"remove"}}`} {
		out, err := cartintent.Decode(proposal, f.snapshot.ID)
		if err != cartintent.ErrInput || !reflect.DeepEqual(out, cart.Call{}) {
			t.Fatalf("unchanged concrete decoder: out=%+v error=%v", out, err)
		}
		f.unchanged(t, before)
	}
	if len(f.view(t).Entries()) != 0 {
		t.Fatal("failed decode or vague assent created a marker")
	}
}

func TestAmbiguousEditPendingViewBoundIsNeverSilentTruncation(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	for i := 0; i < cart.PendingEditViewLimit; i++ {
		f.mark(t, int64(100+i), cart.UnresolvedRemove)
	}
	view := f.view(t)
	if len(view.Entries()) != cart.PendingEditViewLimit {
		t.Fatal("bounded active set was truncated")
	}
	f.mark(t, 1000, cart.UnresolvedRemove)
	before := f.effects(t)
	out, err := f.s.ReadPendingEdits(t.Context(), f.local, f.claim, f.snapshot.ID, f.snapshot.Version)
	if !errors.Is(err, cart.ErrLimit) || !reflect.DeepEqual(out, cart.PendingEditView{}) {
		t.Fatalf("overflow must fail closed: %+v %v", out, err)
	}
	f.unchanged(t, before)
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 1001, view, view.Entries()[0].Ref, call); !errors.Is(err, cart.ErrStale) || out != (cart.NativeActionRef{}) {
		t.Fatalf("old partial view grants no resolution authority: %+v %v", out, err)
	}
	f.unchanged(t, before)
}
