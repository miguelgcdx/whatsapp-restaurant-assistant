package cart

import (
	"fmt"
	"testing"
)

// Fixed legacy bytes pin all existing commands sharing the descriptor codec.
// The same public constructors run in the transient before/after differential;
// this committed regression test never reads Git or a historical checkout.
func TestPickupCodecLegacyCompatibility(t *testing.T) {
	const c = "10000000-0000-4000-8000-000000000001"
	const m = "10000000-0000-4000-8000-000000000002"
	const u = "10000000-0000-4000-8000-000000000003"
	const p = "10000000-0000-4000-8000-000000000004"
	id, _ := ParseCartID(c)
	menu, _ := ParseMenuID(m)
	unit, _ := ParseUnitID(u)
	preview, _ := ParseSummaryPreviewID(p)
	version, _ := ParseVersion(3)
	add, _ := Add(menu, 2)
	remove, _ := Remove([]UnitID{unit})
	note, _ := SetNote([]UnitID{unit}, "  no onions\n")
	delivery, _ := DeliveryFulfillment("Exact address", "555-local")
	tests := []struct {
		name string
		make func() (Call, error)
		want string
	}{
		{"create", func() (Call, error) { return CreateCall(), nil }, `{"action":"create","cart":"","expected":0,"menu":"","count":0,"targets":[],"note":""}`},
		{"read", func() (Call, error) { return ReadCall(id) }, `{"action":"read","cart":"` + c + `","expected":0,"menu":"","count":0,"targets":[],"note":""}`},
		{"add", func() (Call, error) { return EditCall(id, version, add) }, `{"action":"add","cart":"` + c + `","expected":3,"menu":"` + m + `","count":2,"targets":[],"note":""}`},
		{"remove", func() (Call, error) { return EditCall(id, version, remove) }, `{"action":"remove","cart":"` + c + `","expected":3,"menu":"","count":0,"targets":["` + u + `"],"note":""}`},
		{"set_note", func() (Call, error) { return EditCall(id, version, note) }, `{"action":"set_note","cart":"` + c + `","expected":3,"menu":"","count":0,"targets":["` + u + `"],"note":"  no onions\n"}`},
		{"pickup", func() (Call, error) { return SetFulfillmentCall(id, version, PickupFulfillment()) }, `{"codec":"draft-v1","action":"set_fulfillment","cart":"` + c + `","expected":3,"fulfillment":{"kind":"pickup","payment_method":"pay_at_pickup"}}`},
		{"delivery", func() (Call, error) { return SetFulfillmentCall(id, version, delivery) }, `{"codec":"draft-v1","action":"set_fulfillment","cart":"` + c + `","expected":3,"fulfillment":{"kind":"delivery","payment_method":"cash_on_delivery","address":"Exact address","recipient_phone":"555-local"}}`},
		{"quote", func() (Call, error) { return QuoteCall(id) }, `{"codec":"draft-v1","action":"quote","cart":"` + c + `","expected":0,"fulfillment":null}`},
		{"capture_preview", func() (Call, error) { return CaptureSummaryPreviewCall(id, version) }, `{"codec":"local-preview-v1","action":"capture_preview","cart":"` + c + `","expected":3}`},
		{"read_preview", func() (Call, error) { return ReadSummaryPreviewCall(preview) }, `{"codec":"local-preview-v1","action":"read_preview","preview_id":"` + p + `"}`},
	}
	for _, tt := range tests {
		call, err := tt.make()
		if err != nil {
			t.Fatalf("%s constructor: %v", tt.name, err)
		}
		body, err := encodeCall(call)
		if err != nil || string(body) != tt.want {
			t.Fatalf("%s bytes=%s err=%v, want %s", tt.name, body, err, tt.want)
		}
		restored, err := decodeCall(string(body))
		if err != nil {
			t.Fatalf("%s reload: %v", tt.name, err)
		}
		again, err := encodeCall(restored)
		if err != nil || string(again) != tt.want {
			t.Fatalf("%s reloaded bytes=%s err=%v", tt.name, again, err)
		}
		fmt.Printf("%s=%s\n", tt.name, body)
	}
}
