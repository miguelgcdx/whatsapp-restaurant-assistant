package cart_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

func localPreview(ctx context.Context, store *cart.Store, local cart.LocalContext,
	eventID string, nextSlot, readSlot int64, job foundation.Job,
	snapshot cart.Snapshot) (cart.SummaryPreviewResult, error) {
	call, err := cart.CaptureSummaryPreviewCall(snapshot.ID, snapshot.Version)
	if err != nil {
		return cart.SummaryPreviewResult{}, err
	}
	plan, err := store.RegisterExecution(ctx, local, eventID, nextSlot, call)
	if err != nil {
		return cart.SummaryPreviewResult{}, err
	}
	captured, err := store.ExecuteSummaryPreview(ctx, local, plan, job)
	if err != nil {
		return cart.SummaryPreviewResult{}, err
	}
	read, err := cart.ReadSummaryPreviewCall(captured.Preview.ID)
	if err != nil {
		return cart.SummaryPreviewResult{}, err
	}
	readPlan, err := store.RegisterExecution(ctx, local, eventID, readSlot, read)
	if err != nil {
		return cart.SummaryPreviewResult{}, err
	}
	return store.ExecuteSummaryPreview(ctx, local, readPlan, job)
}

// S2/S3/S5: execute the guide API and literal examples, not merely similar output.
func TestSummaryPreviewIndependentGuide(t *testing.T) {
	for _, kind := range []string{"pickup", "delivery", "empty"} {
		t.Run(kind, func(t *testing.T) {
			f := draftSetup(t, 650)
			count := int64(2)
			choice := cart.PickupFulfillment()
			if kind == "delivery" {
				choice = delivery(t, "fictional address", "fictional phone")
			}
			if kind == "empty" {
				count = 0
			}
			selected := previewSelected(t, f, count, choice)
			snapshot := selected.Cart
			if count > 0 {
				action, err := cart.SetNote([]cart.UnitID{snapshot.Units[0].ID}, "no salt")
				if err != nil {
					t.Fatal(err)
				}
				snapshot = apply(t, f.fixture, cart.Result{Cart: snapshot}, action).Cart
			}
			r, err := localPreview(t.Context(), f.s, f.local, f.event, f.slot+1, f.slot+2, f.job, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			items := "(empty)\n"
			subtotal, fee, total := "0.00", "0.00", "0.00"
			fulfillment := "Fulfillment: pickup\nPayment method: pay_at_pickup\n"
			if count > 0 {
				items = "- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"no salt\"\n- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"\"\n"
				subtotal, total = "13.00", "13.00"
			}
			if kind == "delivery" {
				fulfillment = "Fulfillment: delivery\nDelivery address: \"fictional address\"\nRecipient phone: \"fictional phone\"\nPayment method: cash_on_delivery\n"
				fee, total = "3.00", "16.00"
			}
			want := "Local draft preview (not submitted)\nItems:\n" + items + fulfillment + "Subtotal: USD " + subtotal + "\nDelivery fee: USD " + fee + "\nTotal: USD " + total + "\nPreparation estimate: unavailable (workload not connected).\nWould you like anything else?"
			if r.Preview.Text != want || r.Preview.NumericEstimate != nil || r.Preview.EstimateReason != cart.WorkloadNotConnected || r.Preview.SourceCartVersion != snapshot.Version || r.OperationID == r.Preview.SourceOperationID {
				t.Fatalf("guide result %+v", r)
			}
		})
	}
}

// S2: the configured exponent contract includes values between the writer examples.
func TestSummaryPreviewIndependentIntermediateMoney(t *testing.T) {
	for _, tc := range []struct {
		exponent int16
		amount   string
	}{{1, "65.0"}, {4, "0.0650"}, {5, "0.00650"}} {
		t.Run(fmt.Sprint(tc.exponent), func(t *testing.T) {
			f := draftSetup(t, 650)
			if _, err := f.conn.Exec(t.Context(), `UPDATE app.restaurant_settings SET currency_exponent=$1`, tc.exponent); err != nil {
				t.Fatal(err)
			}
			selected := previewSelected(t, f, 1, cart.PickupFulfillment())
			r := previewRun(t, f, f.plan(t, previewCall(t, selected.Cart)))
			want := "Local draft preview (not submitted)\nItems:\n- \"Fictional sandwich\" | quantity: 1 | price: USD " + tc.amount + " | note: \"\"\nFulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD " + tc.amount + "\nDelivery fee: USD " + fmt.Sprintf("%.*f", tc.exponent, 0.0) + "\nTotal: USD " + tc.amount + "\nPreparation estimate: unavailable (workload not connected).\nWould you like anything else?"
			if r.Preview.Text != want {
				t.Fatalf("money got %q want %q", r.Preview.Text, want)
			}
		})
	}
}

// S3/S4/S5: history needs neither current settings nor current units/catalog;
// detached money is not authority, and even semantically equivalent JSON rejects.
func TestSummaryPreviewIndependentMissingSources(t *testing.T) {
	f := draftSetup(t, 650)
	selected := previewSelected(t, f, 1, delivery(t, "a", "p"))
	capture := f.plan(t, previewCall(t, selected.Cart))
	original := previewRun(t, f, capture)
	read := f.plan(t, previewReadCall(t, original.Preview.ID))
	changed := previewRun(t, f, capture)
	changed.Preview.Units[0].Note = "forged"
	changed.Preview.Units[0].PriceMinor = 0
	changed.Preview.Quote.Total = 0
	changed.Preview.Text = "forged"
	remove, err := cart.Remove([]cart.UnitID{selected.Cart.Units[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	current := apply(t, f.fixture, cart.Result{Cart: selected.Cart}, remove)
	if _, err = f.conn.Exec(t.Context(), `DELETE FROM app.menu_items; DELETE FROM app.restaurant_settings`); err != nil {
		t.Fatal(err)
	}
	before := previewState(t, f)
	for _, plan := range []cart.Execution{capture, read} {
		got := previewRun(t, f, plan)
		want := original
		want.OperationID = plan.OperationID()
		if !reflect.DeepEqual(got, want) || previewState(t, f) != before {
			t.Fatal("history depended on mutable sources", got)
		}
	}
	if _, err = f.conn.Exec(t.Context(), `UPDATE app.cart_summary_previews SET body=' '||body`); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []cart.Execution{capture, read} {
		previewRejected(t, f, plan, f.job, cart.ErrStoredData, current.Cart)
	}
}
