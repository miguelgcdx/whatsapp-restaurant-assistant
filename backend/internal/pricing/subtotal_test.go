package pricing_test

import (
	"math"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/pricing"
)

func TestSubtotalWithoutFulfillment(t *testing.T) {
	s, c := fixture()
	c[0].Available = false
	e, err := pricing.New(s, c)
	if err != nil {
		t.Fatal(err)
	}
	draft := []pricing.Unit{{ID: u1, MenuID: sandwich, Note: " exact 🥪\n"}}
	before := append([]pricing.Unit(nil), draft...)
	got, err := e.Subtotal(draft)
	if err != nil || got != 650 || !reflect.DeepEqual(draft, before) {
		t.Fatalf("subtotal=%d err=%v draft=%v", got, err, draft)
	}
	if got, err := e.Subtotal(nil); err != nil || got != 0 {
		t.Fatalf("empty=%d %v", got, err)
	}
	if _, err := e.Quote(draft, pricing.Fulfillment{}); err == nil || err.Error() != "invalid fulfillment" {
		t.Fatalf("Quote changed: %v", err)
	}
}

func TestSubtotalCheckedValidation(t *testing.T) {
	s, c := fixture()
	c[0].Price = math.MaxInt64
	e, err := pricing.New(s, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		engine  *pricing.Engine
		draft   []pricing.Unit
		message string
	}{
		{"zero engine", &pricing.Engine{}, nil, "invalid engine"},
		{"nil engine", nil, nil, "invalid engine"},
		{"invalid", e, []pricing.Unit{{ID: "bad", MenuID: sandwich}}, "invalid unit"},
		{"unknown", e, []pricing.Unit{{ID: u1, MenuID: u2}}, "unknown menu item"},
		{"duplicate", e, []pricing.Unit{{ID: u1, MenuID: sandwich}, {ID: u1, MenuID: sandwich}}, "duplicate unit ID"},
		{"overflow", e, []pricing.Unit{{ID: u1, MenuID: sandwich}, {ID: u2, MenuID: sandwich}}, "amount overflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.engine.Subtotal(tc.draft)
			if got != 0 || err == nil || err.Error() != tc.message {
				t.Fatalf("got=%d err=%v", got, err)
			}
		})
	}
}
