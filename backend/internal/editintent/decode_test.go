package editintent_test

import (
	"errors"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
	"restaurant-assistant/backend/internal/editintent"
)

const cartID = "a0000000-0000-4000-8000-000000000001"
const menuID = "10000000-0000-4000-8000-000000000001"
const unitID = "20000000-0000-4000-8000-000000000001"

func TestAIAmbiguityDecodeCarriersAndConcretePreservation(t *testing.T) {
	id, err := cart.ParseCartID(cartID)
	if err != nil {
		t.Fatal(err)
	}
	version, err := cart.ParseVersion(3)
	if err != nil {
		t.Fatal(err)
	}
	remove := `{"action":"remove","expected_version":"3","unit_ids":["` + unitID + `"]}`
	for _, tc := range []struct {
		raw      string
		variant  editintent.Variant
		kind     cart.UnresolvedEditKind
		selector string
	}{
		{`{"proposal":"unresolved_target","kind":"Add"}`, editintent.UnresolvedTarget, cart.UnresolvedAdd, ""},
		{`{"proposal":"unresolved_target","kind":"Remove"}`, editintent.UnresolvedTarget, cart.UnresolvedRemove, ""},
		{`{"proposal":"unresolved_target","kind":"SetNote"}`, editintent.UnresolvedTarget, cart.UnresolvedSetNote, ""},
		{`{"proposal":"resolve_target","pending_selector":"p1","edit":` + remove + `}`, editintent.ResolveTarget, "", "p1"},
		{`{"proposal":"no_edit"}`, editintent.NoEdit, "", ""},
		{remove, editintent.Concrete, "", ""},
		{`{"action":"add","expected_version":"3","menu_id":"` + menuID + `","count":2}`, editintent.Concrete, "", ""},
		{`{"action":"set_note","expected_version":"3","unit_ids":["` + unitID + `"],"note":"no salt"}`, editintent.Concrete, "", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := editintent.Decode(tc.raw, id, version)
			if err != nil || got.Variant() != tc.variant || got.Kind() != tc.kind || got.Selector() != tc.selector {
				t.Fatalf("decode=%+v error=%v", got, err)
			}
			if tc.variant == editintent.Concrete || tc.variant == editintent.ResolveTarget {
				raw := tc.raw
				if tc.variant == editintent.ResolveTarget {
					raw = remove
				}
				want, err := cartintent.Decode(raw, id)
				if err != nil || !reflect.DeepEqual(got.Call(), want) {
					t.Fatalf("legacy concrete changed: %v", err)
				}
			} else if !reflect.DeepEqual(got.Call(), cart.Call{}) {
				t.Fatal("non-concrete carrier contains a call")
			}
			if tc.variant != editintent.Concrete {
				legacy, err := cartintent.Decode(tc.raw, id)
				if !errors.Is(err, cartintent.ErrInput) || !reflect.DeepEqual(legacy, cart.Call{}) {
					t.Fatal("ambiguity must not expand legacy decoder")
				}
			}
		})
	}
}

func TestAIAmbiguityDecodeRejectsAuthorityAndMalformedCarriers(t *testing.T) {
	id, _ := cart.ParseCartID(cartID)
	version, _ := cart.ParseVersion(3)
	for _, raw := range []string{
		`{"proposal":"unresolved_target","kind":"remove"}`,
		`{"proposal":"unresolved_target","kind":"Remove","request":"invented"}`,
		`{"proposal":"unresolved_target","kind":"Remove","slot":1}`,
		`{"proposal":"unresolved_target","kind":"Remove","cart_id":"` + cartID + `"}`,
		`{"proposal":"unresolved_target","kind":"Remove","source":"invented"}`,
		`{"proposal":"unresolved_target","kind":"Remove","expected_version":"3"}`,
		`{"proposal":"no_edit","kind":"Remove"}`,
		`{"proposal":"no_edit","action":"remove"}`,
		`{"proposal":null}`,
		`{"proposal":"no_edit","proposal":"no_edit"}`,
		`{"proposal":"no_edit"} {}`,
		`{"proposal":"no_edit","proposal":"unresolved_target"}`,
		`{"proposal":"resolve_target","pending_selector":"","edit":{}}`,
		`{"proposal":"resolve_target","pending_selector":"p1","edit":{"proposal":"no_edit"}}`,
		`{"action":"remove","expected_version":"2","unit_ids":["` + unitID + `"]}`,
		`{"proposal":"not_complete"}`, `{} trailing`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := editintent.Decode(raw, id, version)
			if !errors.Is(err, editintent.ErrInput) || !reflect.DeepEqual(got, editintent.Proposal{}) {
				t.Fatalf("wanted zero/input error: %+v %v", got, err)
			}
		})
	}
}
