package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

func TestNativeActionAdmission(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "native-init")
	event := foundation.Event{ID: "native-edit", Sender: "fictional-sender", Text: "trusted edit proposal"}
	if _, created, err := f.s.AcceptMenu(ctx, f.local, event, time.Now().UTC()); err != nil || !created {
		t.Fatalf("admit source: %v %v", created, err)
	}
	claim, ok, err := f.s.ClaimMenu(ctx, f.local, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim source: %v %v", ok, err)
	}
	original := create(t, f)
	makeCall := func(id cart.CartID, v cart.Version, action cart.Action) cart.Call {
		t.Helper()
		call, err := cart.EditCall(id, v, action)
		if err != nil {
			t.Fatal(err)
		}
		return call
	}
	call := makeCall(original.Cart.ID, original.Cart.Version, add(t, 1))
	jobs := menuJobs(t, f)
	c := sourceConn(t, f)
	// Supplemental storage checks accompany, never replace, public readback.
	workState := func() string {
		t.Helper()
		var body string
		if err := c.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(n) ORDER BY event_id,runtime_slot),'[]'::jsonb)::text FROM app.native_cart_actions n`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	jobBefore, err := jobs.Inspect(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	admissionBefore, err := f.s.ReadAdmission(ctx, f.local, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(t *testing.T) {
		t.Helper()
		got, err := f.s.Read(ctx, f.local, original.Cart.ID)
		if err != nil || !reflect.DeepEqual(got, original.Cart) {
			t.Fatalf("admission mutated cart: %+v %v", got, err)
		}
		job, err := jobs.Inspect(ctx, event.ID)
		if err != nil || job != jobBefore {
			t.Fatalf("reply job/counters changed: %+v %v", job, err)
		}
		admission, err := f.s.ReadAdmission(ctx, f.local, event.ID)
		if err != nil || admission != admissionBefore {
			t.Fatalf("source admission changed: %+v %v", admission, err)
		}
	}
	// Full-result budget, not the rough count bound, must reject this fresh edit.
	large := add(t, 5000)
	beforeCalls, beforeWork := sourceCounts(t, c), workState()
	if got, err := f.s.Apply(ctx, f.local, operation(t), original.Cart.ID, original.Cart.Version, large); !errors.Is(err, cart.ErrLimit) || !reflect.DeepEqual(got, cart.Result{}) {
		t.Fatalf("public Apply budget oracle: result=%+v error=%v", got, err)
	}
	budgetCall := makeCall(original.Cart.ID, original.Cart.Version, large)
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 10, budgetCall); !errors.Is(err, cart.ErrLimit) || got != (cart.NativeActionRef{}) {
		t.Fatalf("complete Add result budget: ref=%+v error=%v want=ErrLimit/zeroRef", got, err)
	}
	unchanged(t)
	if sourceCounts(t, c) != beforeCalls || workState() != beforeWork {
		t.Fatal("over-budget admission persisted canonical/native work or changed counters")
	}
	// Existing tests below also prove rejected slot 10 remains unreserved.
	original = apply(t, f, original, add(t, 1))
	call = makeCall(original.Cart.ID, original.Cart.Version, add(t, 1))
	ref, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 0, call)
	if err != nil || ref == (cart.NativeActionRef{}) || ref.OperationID() == (cart.OperationID{}) {
		t.Fatalf("fresh native admission must return server ref without mutation: ref=%+v error=%v", ref, err)
	}
	unchanged(t)
	if raw, err := json.Marshal(ref); err != nil || string(raw) != "{}" {
		t.Fatalf("reference exposed authority metadata: %s %v", raw, err)
	}
	workBefore := workState()
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 0, call); err != nil || got != ref {
		t.Fatalf("identical admission: %+v %v", got, err)
	}
	if workState() != workBefore {
		t.Fatal("identical admission changed durable work/counters/provenance")
	}
	// Public canonical registration observes exactly the same operation namespace.
	legacy, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, call)
	if err != nil || legacy.OperationID() != ref.OperationID() {
		t.Fatalf("canonical identity: %+v %v", legacy, err)
	}
	changed := makeCall(original.Cart.ID, original.Cart.Version, add(t, 2))
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 0, changed); !errors.Is(err, cart.ErrConflict) || got != (cart.NativeActionRef{}) {
		t.Fatalf("changed intent: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("changed intent changed durable work")
	}
	remove, err := cart.Remove([]cart.UnitID{original.Cart.Units[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	note, err := cart.SetNote([]cart.UnitID{original.Cart.Units[0].ID}, "  exact note\né ")
	if err != nil {
		t.Fatal(err)
	}
	for i, action := range []cart.Action{remove, note, add(t, 1)} {
		slot := []int64{1, 2, math.MaxInt64}[i]
		edit := makeCall(original.Cart.ID, original.Cart.Version, action)
		got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, slot, edit)
		if err != nil || got == (cart.NativeActionRef{}) || got.OperationID() == ref.OperationID() {
			t.Fatalf("supported edit %d: %+v %v", i, got, err)
		}
		if again, err := f.s.AdmitNativeEdit(ctx, f.local, claim, slot, edit); err != nil || again != got {
			t.Fatalf("stable edit %d: %+v %v", i, again, err)
		}
		unchanged(t)
	}
	occupied, err := f.s.RegisterExecution(ctx, f.local, event.ID, 3, call)
	if err != nil {
		t.Fatal(err)
	}
	workBefore = workState()
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 3, call); !errors.Is(err, cart.ErrConflict) || got != (cart.NativeActionRef{}) {
		t.Fatalf("legacy promotion: %+v %v", got, err)
	}
	if got, err := f.s.RegisterExecution(ctx, f.local, event.ID, 3, call); err != nil || got != occupied {
		t.Fatalf("legacy identity changed: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("legacy plan acquired native admission")
	}
	// Concurrent callers must resolve a single canonical operation/ref.
	type outcome struct {
		ref cart.NativeActionRef
		err error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 6)
	for range 6 {
		go func() {
			<-start
			ref, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 4, call)
			outcomes <- outcome{ref, err}
		}()
	}
	close(start)
	var concurrent cart.NativeActionRef
	for range 6 {
		got := <-outcomes
		if got.err != nil || got.ref == (cart.NativeActionRef{}) {
			t.Fatalf("concurrent admission: %+v", got)
		}
		if concurrent != (cart.NativeActionRef{}) && got.ref != concurrent {
			t.Fatal("concurrent callers allocated different work")
		}
		concurrent = got.ref
	}
	unchanged(t)
	other, err := f.s.ProvisionLocal(ctx, "fictional-business", "other-sender")
	if err != nil {
		t.Fatal(err)
	}
	otherCart, err := f.s.Create(ctx, other, operation(t))
	if err != nil {
		t.Fatal(err)
	}
	read, err := cart.ReadCall(original.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := cart.QuoteCall(original.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	fulfillment, err := cart.SetFulfillmentCall(original.Cart.ID, original.Cart.Version, cart.PickupFulfillment())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := cart.CaptureSummaryPreviewCall(original.Cart.ID, original.Cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	missingUnit, err := cart.ParseUnitID("b0000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	missingTarget, err := cart.Remove([]cart.UnitID{missingUnit})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	var nilStore *cart.Store
	cases := []struct {
		name   string
		store  *cart.Store
		parent context.Context
		local  cart.LocalContext
		claim  cart.MenuClaim
		slot   int64
		call   cart.Call
		want   error
	}{
		{"nil-context", f.s, nil, f.local, claim, 10, call, cart.ErrInput},
		{"nil-store", nilStore, ctx, f.local, claim, 10, call, cart.ErrInput},
		{"zero-store", &cart.Store{}, ctx, f.local, claim, 10, call, cart.ErrInput},
		{"zero-local", f.s, ctx, cart.LocalContext{}, claim, 10, call, cart.ErrInput},
		{"zero-call", f.s, ctx, f.local, claim, 10, cart.Call{}, cart.ErrInput},
		{"create", f.s, ctx, f.local, claim, 10, cart.CreateCall(), cart.ErrInput},
		{"read", f.s, ctx, f.local, claim, 10, read, cart.ErrInput},
		{"quote", f.s, ctx, f.local, claim, 10, quote, cart.ErrInput},
		{"fulfillment", f.s, ctx, f.local, claim, 10, fulfillment, cart.ErrInput},
		{"preview", f.s, ctx, f.local, claim, 10, preview, cart.ErrInput},
		{"missing-target", f.s, ctx, f.local, claim, 10, makeCall(original.Cart.ID, original.Cart.Version, missingTarget), cart.ErrNotFound},
		{"negative-slot", f.s, ctx, f.local, claim, -1, call, cart.ErrInput},
		{"zero-claim", f.s, ctx, f.local, cart.MenuClaim{}, 10, call, foundation.ErrStaleLease},
		{"foreign-claim", f.s, ctx, other, claim, 10, call, foundation.ErrStaleLease},
		{"foreign-cart", f.s, ctx, f.local, claim, 10, makeCall(otherCart.Cart.ID, otherCart.Cart.Version, add(t, 1)), cart.ErrNotFound},
		{"stale-version", f.s, ctx, f.local, claim, 10, makeCall(original.Cart.ID, version(t, 0), add(t, 1)), cart.ErrStale},
		{"cancelled", f.s, cancelled, f.local, claim, 10, call, context.Canceled},
		{"deadline", f.s, deadline, f.local, claim, 10, call, context.DeadlineExceeded},
		{"domain-limit", f.s, ctx, f.local, claim, 10, makeCall(original.Cart.ID, original.Cart.Version, add(t, math.MaxInt64)), cart.ErrLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := workState()
			got, err := tc.store.AdmitNativeEdit(tc.parent, tc.local, tc.claim, tc.slot, tc.call)
			if !errors.Is(err, tc.want) || got != (cart.NativeActionRef{}) {
				t.Fatalf("ref=%+v error=%v want=%v", got, err, tc.want)
			}
			unchanged(t)
			if workState() != before {
				t.Fatal("rejected input changed durable work/counters")
			}
		})
	}
	// A rejected slot remains free for a different valid public intent.
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 10, changed); err != nil || got == (cart.NativeActionRef{}) {
		t.Fatalf("rejections reserved slot: %+v %v", got, err)
	}
	if _, err := f.cat.SetAvailability(ctx, menu, 0, false); err != nil {
		t.Fatal(err)
	}
	workBefore = workState()
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 12, call); !errors.Is(err, cart.ErrUnavailableItem) || got != (cart.NativeActionRef{}) {
		t.Fatalf("unavailable item: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("unavailable item created work")
	}
	if _, err := f.cat.SetAvailability(ctx, menu, 1, true); err != nil {
		t.Fatal(err)
	}
	// Fixture fault after canonical registration proves its transaction rolls back.
	if _, err := c.Exec(ctx, `ALTER TABLE app.native_cart_actions ADD CONSTRAINT fixture_reject_slot CHECK (runtime_slot <> 50)`); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 50, call); !errors.Is(err, cart.ErrUnavailable) || got != (cart.NativeActionRef{}) {
		t.Fatalf("atomic insert failure: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("failed transaction changed native work")
	}
	if got, err := f.s.RegisterExecution(ctx, f.local, event.ID, 50, changed); err != nil || got.OperationID() == (cart.OperationID{}) {
		t.Fatalf("failed admission left canonical plan: %+v %v", got, err)
	}
	// Fixture-only arrangement makes the actual persisted lease expired, without sleeps.
	if _, err := c.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.Job().ID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 11, call); !errors.Is(err, foundation.ErrStaleLease) || got != (cart.NativeActionRef{}) {
		t.Fatalf("expired: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("expired claim changed work")
	}
	fresh, ok, err := f.s.ClaimMenu(ctx, f.local, time.Minute)
	if err != nil || !ok || fresh.Job().Generation != claim.Job().Generation+1 {
		t.Fatalf("reclaim: %+v %v %v", fresh, ok, err)
	}
	jobBefore, err = jobs.Inspect(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, claim, 11, call); !errors.Is(err, foundation.ErrStaleLease) || got != (cart.NativeActionRef{}) {
		t.Fatalf("stale generation/attempt: %+v %v", got, err)
	}
	unchanged(t)
	if workState() != workBefore {
		t.Fatal("stale generation changed work")
	}
	if got, err := f.s.AdmitNativeEdit(ctx, f.local, fresh, 11, changed); err != nil || got == (cart.NativeActionRef{}) {
		t.Fatalf("stale claims reserved slot: %+v %v", got, err)
	}
	workBefore = workState()
	for i, mode := range []conversation.Mode{conversation.ModeStaff, conversation.ModeAI} {
		state, err = control.SetMode(ctx, token, f.local.ConversationID(), mode, state.Version, []string{"takeover", "resume"}[i])
		if err != nil {
			t.Fatal(err)
		}
		if got, err := f.s.AdmitNativeEdit(ctx, f.local, fresh, 0, call); !errors.Is(err, cart.ErrNativeAdmissionClosed) || got != (cart.NativeActionRef{}) {
			t.Fatalf("mode/exact epoch: %+v %v", got, err)
		}
		unchanged(t)
		if workState() != workBefore {
			t.Fatal("mode/epoch rejection changed admitted work")
		}
	}
	if cart.ErrNativeAdmissionClosed.Error() != "native cart admission is no longer permitted" {
		t.Fatal("sentinel message changed")
	}
}
