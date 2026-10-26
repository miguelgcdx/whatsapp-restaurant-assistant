package cart_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

// Public shared-authority preservation across all three native result seams.
// Fixture helpers construct public calls and owned data, not private library state.
// UUID/time equality is asserted within each run, not substituted with fixed IDs.
func TestLegacyAuthorityDifferential(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	trace := []any{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	register := func(c cart.Call) cart.Execution { t.Helper(); return f.plan(t, c) }
	execute := func(p cart.Execution, job foundation.Job) cart.Result {
		t.Helper()
		r, e := f.s.Execute(ctx, f.local, p, job)
		must(e)
		return r
	}
	draft := func(p cart.Execution, job foundation.Job) cart.DraftResult {
		t.Helper()
		r, e := f.s.ExecuteDraft(ctx, f.local, p, job)
		must(e)
		return r
	}
	preview := func(p cart.Execution, job foundation.Job) cart.SummaryPreviewResult {
		t.Helper()
		r, e := f.s.ExecuteSummaryPreview(ctx, f.local, p, job)
		must(e)
		return r
	}
	createPlan := register(cart.CreateCall())
	retry, e := f.s.RegisterExecution(ctx, f.local, f.event, f.slot, cart.CreateCall())
	must(e)
	if retry != createPlan || retry.OperationID() != createPlan.OperationID() {
		t.Fatal("registration identity")
	}
	trace = append(trace, map[string]any{"register_same_intent_same_operation": true})
	created := execute(createPlan, f.job)
	if len(created.Cart.Units) != 0 || created.Cart.Version.Int64() != 0 {
		t.Fatal("registered create")
	}
	editCall, e := cart.EditCall(created.Cart.ID, created.Cart.Version, add(t, 2))
	must(e)
	editPlan := register(editCall)
	edited := execute(editPlan, f.job)
	note := " exact 🍲\n"
	a, e := cart.SetNote([]cart.UnitID{edited.Cart.Units[0].ID}, note)
	must(e)
	noteCall, e := cart.EditCall(edited.Cart.ID, edited.Cart.Version, a)
	must(e)
	noted := execute(register(noteCall), f.job)
	readCall, e := cart.ReadCall(noted.Cart.ID)
	must(e)
	readPlan := register(readCall)
	if !reflect.DeepEqual(execute(readPlan, f.job).Cart, noted.Cart) {
		t.Fatal("registered read")
	}
	if noted.Cart.Subtotal != 1300 || len(noted.Cart.Units) != 2 || noted.Cart.Units[0].Note != note {
		t.Fatal("prices/notes")
	}
	for _, u := range noted.Cart.Units {
		if u.PriceMinor != 650 || u.Currency != "USD" || u.Exponent != 2 {
			t.Fatal("unit price")
		}
	}
	deliveryChoice := delivery(t, " exact street\n", " exact phone ")
	draftPlan := register(setCall(t, noted.Cart, deliveryChoice))
	selected := draft(draftPlan, f.job)
	quotePlan := register(quoteCall(t, selected.Cart))
	quoted := draft(quotePlan, f.job)
	if quoted.Quote.Subtotal != 1300 || quoted.Quote.Fee != 300 || quoted.Quote.Total != 1600 || quoted.Quote.PaymentMethod != "cash_on_delivery" || quoted.Fulfillment != deliveryChoice {
		t.Fatal("native fulfillment quote")
	}
	previewPlan := register(previewCall(t, quoted.Cart))
	captured := preview(previewPlan, f.job)
	exact := "Local draft preview (not submitted)\nItems:\n" +
		"- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \" exact 🍲\\n\"\n" +
		"- \"Fictional sandwich\" | quantity: 1 | price: USD 6.50 | note: \"\"\n" +
		"Fulfillment: delivery\nDelivery address: \" exact street\\n\"\nRecipient phone: \" exact phone \"\nPayment method: cash_on_delivery\nSubtotal: USD 13.00\nDelivery fee: USD 3.00\nTotal: USD 16.00\nPreparation estimate: unavailable (workload not connected).\nWould you like anything else?"
	if captured.Preview.Text != exact || captured.Preview.NumericEstimate != nil || captured.Preview.Quote != quoted.Quote {
		t.Fatalf("preview exact: %q", captured.Preview.Text)
	}
	previewRead := register(previewReadCall(t, captured.Preview.ID))
	readPreview := preview(previewRead, f.job)
	if !reflect.DeepEqual(readPreview.Preview, captured.Preview) {
		t.Fatal("public preview read")
	}
	trace = append(trace, map[string]any{"create_version": created.Cart.Version.Int64(), "edit_subtotal": edited.Cart.Subtotal, "notes": []string{noted.Cart.Units[0].Note, noted.Cart.Units[1].Note}, "prices": []int64{650, 650}, "currency": "USD", "exponent": 2, "fulfillment": []string{quoted.Fulfillment.Kind(), quoted.Fulfillment.Address(), quoted.Fulfillment.RecipientPhone()}, "quote": quoted.Quote, "preview_text": captured.Preview.Text, "numeric_estimate_nil": true})
	// Make every saved mutation/preview genuinely historical, not just an immediate retry.
	laterCall, e := cart.EditCall(quoted.Cart.ID, quoted.Cart.Version, add(t, 1))
	must(e)
	current := execute(register(laterCall), f.job).Cart
	if current.Subtotal != 1950 || current.Version.Int64() <= quoted.Cart.Version.Int64() {
		t.Fatal("later state")
	}
	replay := func(job foundation.Job) {
		t.Helper()
		if !reflect.DeepEqual(execute(createPlan, job), created) || !reflect.DeepEqual(execute(editPlan, job), edited) || !reflect.DeepEqual(draft(draftPlan, job), selected) || !reflect.DeepEqual(preview(previewPlan, job), captured) {
			t.Fatal("historical cached replay")
		}
		if !reflect.DeepEqual(execute(readPlan, job).Cart, current) {
			t.Fatal("registered read must remain fresh")
		}
		// Quote and read-preview are public reads, not cached mutation receipts.
		q := draft(quotePlan, job)
		if q.Quote.Total != 2250 || q.Quote.Subtotal != 1950 || q.Quote.Fee != 300 {
			t.Fatal("fresh quote")
		}
		if !reflect.DeepEqual(preview(previewRead, job).Preview, captured.Preview) {
			t.Fatal("immutable preview read")
		}
	}
	replay(f.job)
	trace = append(trace, map[string]any{"historical_replay_all_three": true, "registered_read_fresh": true, "historical_preview_read": true, "current_subtotal": current.Subtotal, "current_quote_total": 2250})
	// Primary rejection proof is public Read/Inspect and exact zero results.
	// SQL hashes/counts supplement this; no private storage is the primary oracle.
	rejectAll := func(label string, job foundation.Job) {
		t.Helper()
		before, e := f.jobs.Inspect(ctx, f.event)
		must(e)
		state := previewState(t, f)
		r, er := f.s.Execute(ctx, f.local, editPlan, job)
		d, ed := f.s.ExecuteDraft(ctx, f.local, draftPlan, job)
		p, ep := f.s.ExecuteSummaryPreview(ctx, f.local, previewPlan, job)
		for _, err := range []error{er, ed, ep} {
			if err != foundation.ErrStaleLease || !errors.Is(err, foundation.ErrStaleLease) {
				t.Fatalf("%s exact error %v", label, err)
			}
		}
		zero := reflect.DeepEqual(r, cart.Result{}) && reflect.DeepEqual(d, cart.DraftResult{}) && reflect.DeepEqual(p, cart.SummaryPreviewResult{})
		after, e := f.jobs.Inspect(ctx, f.event)
		must(e)
		read, e := f.s.Read(ctx, f.local, current.ID)
		must(e)
		invariant := reflect.DeepEqual(before, after) && reflect.DeepEqual(read, current) && state == previewState(t, f)
		if !zero || !invariant {
			t.Fatalf("%s zero=%v invariant=%v", label, zero, invariant)
		}
		trace = append(trace, map[string]any{"case": label, "execute_error": er.Error(), "draft_error": ed.Error(), "preview_error": ep.Error(), "identity": "foundation.ErrStaleLease", "all_zero": zero, "public_state_and_counters_unchanged": invariant})
	}
	for _, name := range []string{"generation", "attempt", "provider", "sender", "text", "noncanonical", "missing", "zero-claim"} {
		job := f.job
		switch name {
		case "generation":
			job.Generation++
		case "attempt":
			job.Attempt++
		case "provider":
			job.Event.ID = "tampered"
		case "sender":
			job.Event.Sender = "tampered"
		case "text":
			job.Event.Text = "tampered"
		case "noncanonical":
			job.ID = strings.ReplaceAll(job.ID, "-", "")
		case "missing":
			job.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		case "zero-claim":
			job = foundation.Job{}
		}
		rejectAll(name, job)
		replay(f.job)
	}
	// Only the owned fixture's stored lease clock is changed; no app clock override/wait.
	_, e = f.conn.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job.ID)
	must(e)
	rejectAll("expired-before-cache", f.job)
	reclaimed, ok, e := f.jobs.ClaimJob(ctx, time.Now(), time.Minute)
	must(e)
	if !ok || reclaimed.ID != f.job.ID || reclaimed.Generation <= f.job.Generation || reclaimed.Attempt <= f.job.Attempt {
		t.Fatal("fresh generation")
	}
	rejectAll("reclaimed-old-before-cache", f.job)
	replay(reclaimed)
	trace = append(trace, map[string]any{"fresh_reclaimed_replays_all_three": true, "generation_advanced": true, "attempt_advanced": true})
	// Whole-event conflict precedes sender mismatch; invalid input precedes both.
	sameSender, e := f.s.ProvisionLocal(ctx, "foreign-business", "fictional-sender")
	must(e)
	wrongSender, e := f.s.ProvisionLocal(ctx, "foreign-business", "foreign-sender")
	must(e)
	registrationReject := func(label string, local cart.LocalContext, event string, slot int64, call cart.Call, want error, identity string) {
		t.Helper()
		state := previewState(t, f)
		before, e := f.jobs.Inspect(ctx, f.event)
		must(e)
		got, err := f.s.RegisterExecution(ctx, local, event, slot, call)
		if err != want || !reflect.DeepEqual(got, cart.Execution{}) {
			t.Fatalf("%s: %+v %v", label, got, err)
		}
		after, e := f.jobs.Inspect(ctx, f.event)
		must(e)
		read, e := f.s.Read(ctx, f.local, current.ID)
		must(e)
		invariant := state == previewState(t, f) && reflect.DeepEqual(before, after) && reflect.DeepEqual(read, current)
		if !invariant {
			t.Fatal("registration mutation")
		}
		replay(reclaimed)
		trace = append(trace, map[string]any{"case": label, "error": err.Error(), "identity": identity, "zero": true, "public_replay_state_and_counters_unchanged": invariant})
	}
	registrationReject("same-slot-different-intent", f.local, f.event, createPlanSlot(), editCall, cart.ErrConflict, "cart.ErrConflict")
	registrationReject("foreign-whole-event-same-sender", sameSender, f.event, 99, cart.CreateCall(), cart.ErrConflict, "cart.ErrConflict")
	registrationReject("conflict-before-wrong-sender", wrongSender, f.event, 99, cart.CreateCall(), cart.ErrConflict, "cart.ErrConflict")
	registrationReject("invalid-slot-before-conflict", wrongSender, f.event, -1, cart.CreateCall(), cart.ErrInput, "cart.ErrInput")
	registrationReject("invalid-call-before-conflict", wrongSender, f.event, 99, cart.Call{}, cart.ErrInput, "cart.ErrInput")
	registrationReject("missing-event", f.local, "missing-event", 99, cart.CreateCall(), cart.ErrNotFound, "cart.ErrNotFound")
	_, e = f.jobs.Accept(ctx, foundation.Event{ID: "wrong-sender-unbound", Sender: "foreign-sender", Text: "fixture"}, time.Now())
	must(e)
	registrationReject("unbound-wrong-sender", f.local, "wrong-sender-unbound", 99, cart.CreateCall(), cart.ErrNotFound, "cart.ErrNotFound")
	final, e := f.jobs.Inspect(ctx, f.event)
	must(e)
	if final.JobCount != 1 || final.OutboxCount != 0 || final.JobState != "leased" {
		t.Fatal("unexpected public effects")
	}
	trace = append(trace, map[string]any{"job_count": final.JobCount, "outbox_count": final.OutboxCount, "job_state": final.JobState, "all_rejection_replays_unchanged": true})
	b, e := json.Marshal(trace)
	must(e)
	fmt.Println("LEGACY_AUTHORITY_TRACE " + string(b))
}

// draftFixture.plan starts slots at one, an existing fixture convention.
func createPlanSlot() int64 { return 1 }
