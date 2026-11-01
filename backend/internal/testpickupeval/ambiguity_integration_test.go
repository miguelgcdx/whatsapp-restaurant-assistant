package testpickupeval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/carteditflow"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/editintent"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

func TestAIAmbiguityHostMarkerQueuedClarificationNativeResolutionAndFreshSummary(t *testing.T) {
	f := newFixture(t.Context(), t)
	receiver := testpickupreceiver.New(t)
	old, _, _, summaryTurn := f.prepareSummary(receiver, false)
	f.finishSummary(receiver, summaryTurn, "")
	request := "  remove that one 🥪\né  "
	claim := f.claimInbound(request)
	x := f.execution(claim)
	before := f.effects(&x)
	interpreter, requests := ambiguityModel(t, func(editintent.Input) string { return `{"proposal":"unresolved_target","kind":"Remove"}` })
	h := ambiguityHost(t, f, interpreter, f.gate)
	out, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	if err != nil || out.Marked() == (cart.PendingEditRef{}) || out.Native() != (cart.NativeActionRef{}) || out.Question() != "Which item would you like to remove?" {
		t.Fatalf("mark/queue: %+v %v", out, err)
	}
	receipt, err := f.gate.ReadQueued(f.ctx, claim.Source())
	f.check(err)
	if receipt != out.Receipt() || receipt.State != "pending" || receipt.OutboxID == "" || receipt.ProviderID != "" || receipt.Attempt != 0 {
		t.Fatal("not an actual queued-only source-bound question")
	}
	after := f.effects(&x)
	if !reflect.DeepEqual(after.Cart, before.Cart) || !reflect.DeepEqual(after.Orders, before.Orders) || !reflect.DeepEqual(after.Intents, before.Intents) || after.Processing.JobState != "completed" || after.Processing.OutboxCount != 1 || after.Processing.OutboxState != "pending" {
		t.Fatal("clarification changed cart/order or claimed delivery")
	}
	if got := requests(); len(got) != 1 || got[0].InboundText != request || !reflect.DeepEqual(got[0].Cart, before.Cart) || len(got[0].Pending) != 0 {
		t.Fatalf("actual stored model facts: %+v", got)
	}

	// Ordinary concrete Add remains ordinary and cannot clear the earlier marker.
	claim = f.claimInbound("add one fictional sandwich")
	x = f.execution(claim)
	entries := f.ambiguityView(claim)
	if !reflect.DeepEqual(entries, []cart.PendingEdit{{Ref: out.Marked(), Kind: cart.UnresolvedRemove, Request: request}}) {
		t.Fatal(entries)
	}
	blockedBefore := f.effects(&x)
	if _, err := f.pickup.CompletionContext(f.ctx, x); !errors.Is(err, cart.ErrUnresolvedEdit) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.effects(&x), blockedBefore) {
		t.Fatal("blocked completion changed effects")
	}
	interpreter, _ = ambiguityModel(t, func(editintent.Input) string { return ambiguityAdd(f.snapshot) })
	h = ambiguityHost(t, f, interpreter, f.gate)
	ordinary, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	f.check(err)
	if ordinary.Marked() != (cart.PendingEditRef{}) || ordinary.Receipt() != (menugate.Receipt{}) || ordinary.Native() == (cart.NativeActionRef{}) {
		t.Fatal("concrete edit was treated as ambiguity")
	}
	result := f.ambiguityRun(h, ordinary)
	if len(result.Cart.Units) != 3 || !reflect.DeepEqual(f.ambiguityView(claim), entries) {
		t.Fatal("ordinary concrete edit cleared marker")
	}
	f.finish(f.execution(claim), "Ordinary edit recorded (not presented)")

	// Explicit resolution is admission only until the independent native execution.
	claim = f.claimInbound("remove the first sandwich")
	x = f.execution(claim)
	interpreter, _ = ambiguityModel(t, func(in editintent.Input) string {
		if len(in.Pending) != 1 || in.Pending[0].Request != request || in.Pending[0].Selector != "p1" {
			t.Error("wrong exact pending binding")
		}
		return `{"proposal":"resolve_target","pending_selector":"p1","edit":` + ambiguityRemove(f.snapshot) + `}`
	})
	h = ambiguityHost(t, f, interpreter, f.gate)
	before = f.effects(&x)
	resolved, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	f.check(err)
	if resolved.Native() == (cart.NativeActionRef{}) || !reflect.DeepEqual(f.ambiguityView(claim), entries) || !reflect.DeepEqual(f.effects(&x), before) {
		t.Fatal("admission resolved/applied or created reply/order")
	}
	// The ordinary Add already invalidated the presented summary. Existing
	// completion checks reject that stale summary before inspecting markers.
	if _, err := f.pickup.CompletionContext(f.ctx, x); !errors.Is(err, cart.ErrPickupSummaryRequired) {
		t.Fatal(err)
	}
	result = f.ambiguityRun(h, resolved)
	if len(result.Cart.Units) != 2 || len(f.ambiguityView(claim)) != 0 {
		t.Fatal("actual execution did not resolve linked marker")
	}
	if _, err := f.pickup.CompletionContext(f.ctx, x); !errors.Is(err, cart.ErrPickupSummaryRequired) {
		t.Fatal("old summary reused", err)
	}
	if current := f.effects(&x); len(current.Orders.Items) != 0 || len(current.Intents) != len(before.Intents) {
		t.Fatal("resolution submitted an order")
	}
	f.finish(x, "Resolution applied (not presented)")
	fresh, _, _, sx := f.prepareSummary(receiver, false)
	f.finishSummary(receiver, sx, "")
	if fresh.CandidateID == old.CandidateID || fresh.CartVersion != f.snapshot.Version {
		t.Fatal("fresh summary not tied to applied cart")
	}
	completion := f.inbound("Nothing else; please place the order")
	assessor, _ := modelServer(t, ModelResponse{ToolArguments: `{"decision":"complete"}`}, nil)
	submission, err := f.pickup.AssessAndSubmit(f.ctx, completion, 4, assessor)
	f.check(err)
	effects := f.effects(&completion)
	if len(effects.Orders.Items) != 1 || submission.Order.Confirmed.ID != fresh.CandidateID || submission.Order.Confirmed.UnitCount != 2 || submission.Receipt.ID.String() == "" {
		t.Fatal("fresh presented summary failed normal completion")
	}
}

func TestAIAmbiguityHostNoEditMalformedAndUnknownSelectorLeaveNoEffects(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"no-edit", `{"proposal":"no_edit"}`, nil},
		{"malformed", `{"proposal":"no_edit","kind":"Remove"}`, editintent.ErrInput},
		{"protocol-error", `not-json`, commandcode.ErrProtocol},
		{"unknown-selector", `{"proposal":"resolve_target","pending_selector":"p999","edit":%s}`, editintent.ErrInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t.Context(), t)
			claim := f.claimInbound("a new utterance")
			marker, err := f.store.MarkUnresolvedEdit(f.ctx, f.local, claim, 10, f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove)
			f.check(err)
			view := f.ambiguityView(claim)
			x := f.execution(claim)
			before := f.effects(&x)
			admission, err := f.store.ReadAdmission(f.ctx, f.local, claim.Source().EventProviderID())
			f.check(err)
			raw := tc.raw
			if tc.name == "unknown-selector" {
				raw = fmt.Sprintf(raw, ambiguityRemove(f.snapshot))
			}
			interpreter, _ := ambiguityModel(t, func(editintent.Input) string { return raw })
			h := ambiguityHost(t, f, interpreter, f.gate)
			out, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) || !reflect.DeepEqual(out, carteditflow.Outcome{}) {
				t.Fatal(out, err)
			}
			if !reflect.DeepEqual(f.effects(&x), before) || !reflect.DeepEqual(f.ambiguityView(claim), view) || view[0].Ref != marker {
				t.Fatal("rejected/no-edit input changed stored effects")
			}
			after, err := f.store.ReadAdmission(f.ctx, f.local, claim.Source().EventProviderID())
			f.check(err)
			if after != admission {
				t.Fatal("admission counters changed")
			}
			// Successful reuse with a DIFFERENT concrete intent proves no leaked slot.
			interpreter, _ = ambiguityModel(t, func(editintent.Input) string { return ambiguityAdd(f.snapshot) })
			h = ambiguityHost(t, f, interpreter, f.gate)
			ordinary, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
			f.check(err)
			if ordinary.Native() == (cart.NativeActionRef{}) || !reflect.DeepEqual(f.ambiguityView(claim), view) {
				t.Fatal("slot leaked or marker cleared")
			}
		})
	}
}

func TestAIAmbiguityHostRetainsExactViewAcrossHTTPMutation(t *testing.T) {
	f := newFixture(t.Context(), t)
	claim := f.claimInbound("resolve first request")
	first, err := f.store.MarkUnresolvedEdit(f.ctx, f.local, claim, 10, f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove)
	f.check(err)
	var afterMutation Effects
	var active []cart.PendingEdit
	interpreter, _ := ambiguityModel(t, func(in editintent.Input) string {
		if len(in.Pending) != 1 || in.Pending[0].Selector != "p1" {
			t.Error("not the original per-call view")
		}
		_, err := f.store.MarkUnresolvedEdit(f.ctx, f.local, claim, 11, f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove)
		f.check(err)
		afterMutation = f.effects(nil)
		active = f.ambiguityView(claim)
		return `{"proposal":"resolve_target","pending_selector":"p1","edit":` + ambiguityRemove(f.snapshot) + `}`
	})
	h := ambiguityHost(t, f, interpreter, f.gate)
	out, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	if !errors.Is(err, cart.ErrStale) || !reflect.DeepEqual(out, carteditflow.Outcome{}) || !reflect.DeepEqual(f.effects(nil), afterMutation) || !reflect.DeepEqual(f.ambiguityView(claim), active) || len(active) != 2 {
		t.Fatal("rebuilt later view or changed effects", out, err)
	}
	found := false
	for _, e := range active {
		found = found || e.Ref == first
	}
	if !found {
		t.Fatal("original marker lost")
	}
	// A fresh invocation must resolve its explicit p1, not the newest same-kind marker.
	var selected, remaining cart.PendingEditRef
	interpreter, _ = ambiguityModel(t, func(in editintent.Input) string {
		if len(in.Pending) != 2 || in.Pending[0].Selector != "p1" || in.Pending[1].Selector != "p2" {
			t.Error("missing ordered two-marker view")
		}
		current := f.ambiguityView(claim)
		if current[0].Ref != first {
			t.Fatal("first displayed marker is not the original canonical marker")
		}
		selected = first
		remaining = current[1].Ref
		return `{"proposal":"resolve_target","pending_selector":"p1","edit":` + ambiguityRemove(f.snapshot) + `}`
	})
	h = ambiguityHost(t, f, interpreter, f.gate)
	out, err = h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	f.check(err)
	if len(f.ambiguityView(claim)) != 2 {
		t.Fatal("admission cleared marker")
	}
	f.ambiguityRun(h, out)
	left := f.ambiguityView(claim)
	if len(left) != 1 || left[0].Ref != remaining || left[0].Ref == selected {
		t.Fatal("resolution selected by kind/newest rather than exact selector", left)
	}
}

func TestAIAmbiguityHostQueueFailureReportsCommittedMarker(t *testing.T) {
	f := newFixture(t.Context(), t)
	// Establish a current, actually presented summary on a separate prior source
	// so the committed marker, not a missing summary, blocks completion below.
	receiver := testpickupreceiver.New(t)
	_, _, _, summaryTurn := f.prepareSummary(receiver, false)
	f.finishSummary(receiver, summaryTurn, "")
	claim := f.claimInbound("remove that one")
	interpreter, _ := ambiguityModel(t, func(editintent.Input) string { return `{"proposal":"unresolved_target","kind":"Remove"}` })
	h := ambiguityHost(t, f, interpreter, canceledClarificationGate{f.gate})
	x := f.execution(claim)
	before := f.effects(&x)
	out, err := h.Admit(f.ctx, f.ambiguityTurn(claim, 4))
	if !errors.Is(err, context.Canceled) || out.Marked() == (cart.PendingEditRef{}) || out.Receipt() != (menugate.Receipt{}) || out.Native() != (cart.NativeActionRef{}) {
		t.Fatalf("must report known partial commit: %+v %v", out, err)
	}
	entries := f.ambiguityView(claim)
	if len(entries) != 1 || entries[0].Ref != out.Marked() || !reflect.DeepEqual(f.effects(&x), before) {
		t.Fatal("queue failure undid marker or changed cart/job/order")
	}
	if _, err := f.pickup.CompletionContext(f.ctx, x); !errors.Is(err, cart.ErrUnresolvedEdit) {
		t.Fatal(err)
	}
	if r, err := f.gate.ReadQueued(f.ctx, claim.Source()); !errors.Is(err, menugate.ErrNotFound) || r != (menugate.Receipt{}) {
		t.Fatal(r, err)
	}
	// Public recovery queues from stored marker, without another model invocation.
	receipt, err := f.gate.QueueClarification(f.ctx, claim, 4)
	f.check(err)
	if receipt.Content != "Which item would you like to remove?" || receipt.State != "pending" {
		t.Fatal(receipt)
	}
}
