package cart_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

// Reuse actual public pickup/catalog/control fixtures, not a second cart model.
// Each turn retains the real MenuClaim rather than reconstructing it from IDs.
type ambiguousFixture struct {
	*pickupFixture
	claim cart.MenuClaim
	x     cart.PickupExecution
}

func ambiguousSetup(t *testing.T) *ambiguousFixture {
	t.Helper()
	return &ambiguousFixture{pickupFixture: pickupSetup(t)}
}

func (f *ambiguousFixture) turn(t *testing.T, request string) {
	t.Helper()
	f.next++
	event := foundation.Event{ID: fmt.Sprintf("ambiguity-%d", f.next), Sender: f.job.Event.Sender, Text: request}
	if _, fresh, err := f.s.AcceptMenu(t.Context(), f.local, event, time.Now()); err != nil || !fresh {
		t.Fatalf("stored source: fresh=%v error=%v", fresh, err)
	}
	claim, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
	if err != nil || !ok {
		t.Fatalf("real current claim: claimed=%v error=%v", ok, err)
	}
	f.claim = claim
	f.x = cart.PickupExecution{Local: f.local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: version(t, 0)}
}

func (f *ambiguousFixture) mark(t *testing.T, slot int64, kind cart.UnresolvedEditKind) cart.PendingEditRef {
	t.Helper()
	ref, err := f.s.MarkUnresolvedEdit(t.Context(), f.local, f.claim, slot, f.snapshot.ID, f.snapshot.Version, kind)
	if err != nil || ref == (cart.PendingEditRef{}) {
		t.Fatalf("MarkUnresolvedEdit must persist actual request without editing cart: ref=%+v error=%v", ref, err)
	}
	return ref
}

func (f *ambiguousFixture) view(t *testing.T) cart.PendingEditView {
	t.Helper()
	v, err := f.s.ReadPendingEdits(t.Context(), f.local, f.claim, f.snapshot.ID, f.snapshot.Version)
	if err != nil {
		t.Fatalf("authorized complete pending view: %v", err)
	}
	return v
}

func ambiguousCall(t *testing.T, s cart.Snapshot, a cart.Action) cart.Call {
	t.Helper()
	call, err := cart.EditCall(s.ID, s.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func ambiguousRemove(t *testing.T, unit cart.UnitID) cart.Action {
	t.Helper()
	a, err := cart.Remove([]cart.UnitID{unit})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ambiguousExecute(t *testing.T, f *ambiguousFixture, ref cart.NativeActionRef) cart.Result {
	t.Helper()
	lease, ok, err := f.s.RecoverNativeEdit(t.Context(), ref, time.Minute)
	if err != nil || !ok {
		t.Fatalf("native business lease: claimed=%v error=%v", ok, err)
	}
	out, err := f.s.ExecuteNativeEdit(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// These are public observable effects/counters, not internal SQL inspection.
// Slot-reuse assertions below separately detect leaked canonical registrations.
type ambiguousEffects struct {
	cart      cart.Snapshot
	orders    cart.PickupOrderPage
	intents   []cart.PickupLocalIntent
	job       foundation.Snapshot
	admission cart.Admission
}

func (f *ambiguousFixture) effects(t *testing.T) ambiguousEffects {
	t.Helper()
	s, err := f.s.Read(t.Context(), f.local, f.snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	orders, err := f.p.ListOrders(t.Context(), "menu-operator", cart.PickupPageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	intents, err := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.jobs.Inspect(t.Context(), f.claim.Source().EventProviderID())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := f.s.ReadAdmission(t.Context(), f.local, f.claim.Source().EventProviderID())
	if err != nil {
		t.Fatal(err)
	}
	return ambiguousEffects{s, orders, intents, job, admission}
}

func (f *ambiguousFixture) unchanged(t *testing.T, before ambiguousEffects) {
	t.Helper()
	if after := f.effects(t); !reflect.DeepEqual(after, before) {
		t.Fatalf("request changed public state/counters: before=%+v after=%+v", before, after)
	}
}

func TestAmbiguousEditMarkCanonicalReplayAndExactRequest(t *testing.T) {
	f := ambiguousSetup(t)
	request := "  remove that one 🥪\né  "
	f.turn(t, request)
	before := f.effects(t)
	ref := f.mark(t, 100, cart.UnresolvedRemove)
	f.unchanged(t, before)
	if again := f.mark(t, 100, cart.UnresolvedRemove); again != ref {
		t.Fatal("mark replay changed identity")
	}
	v := f.view(t)
	want := []cart.PendingEdit{{Ref: ref, Kind: cart.UnresolvedRemove, Request: request}}
	if !reflect.DeepEqual(v.Entries(), want) {
		t.Fatalf("view=%+v want=%+v", v.Entries(), want)
	}
	copy := v.Entries()
	copy[0].Request = "model rewrite"
	if !reflect.DeepEqual(v.Entries(), want) {
		t.Fatal("presentation copy changed retained binding")
	}
	if b, err := json.Marshal(ref); err != nil || string(b) != "{}" {
		t.Fatalf("opaque ref: %s %v", b, err)
	}
	if b, err := json.Marshal(v); err != nil || string(b) != "{}" {
		t.Fatalf("opaque view: %s %v", b, err)
	}
	call, err := cart.UnresolvedEditCall(f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := f.s.RegisterExecution(t.Context(), f.local, f.claim.Source().EventProviderID(), 100, call)
	if err != nil || execution.OperationID() == (cart.OperationID{}) {
		t.Fatalf("canonical mark registration: %+v %v", execution, err)
	}
	for _, kind := range []cart.UnresolvedEditKind{cart.UnresolvedAdd, cart.UnresolvedSetNote} {
		out, err := f.s.MarkUnresolvedEdit(t.Context(), f.local, f.claim, 100, f.snapshot.ID, f.snapshot.Version, kind)
		if !errors.Is(err, cart.ErrConflict) || out != (cart.PendingEditRef{}) {
			t.Fatalf("immutable mark: %+v %v", out, err)
		}
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t), v) {
		t.Fatal("replay/conflict changed pending view")
	}
}

func TestAmbiguousEditMultipleMarkersAndOrdinaryEdit(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one; change its note; and add one sandwich")
	remove := f.mark(t, 100, cart.UnresolvedRemove)
	note := f.mark(t, 101, cart.UnresolvedSetNote)
	v := f.view(t)
	if len(v.Entries()) != 2 || remove == note {
		t.Fatal("markers replaced or coalesced")
	}
	// A perfectly clear customer edit needs no extra confirmation and no link.
	original := f.snapshot
	ordinary := ambiguousCall(t, f.snapshot, add(t, 1))
	ref, err := f.s.AdmitNativeEdit(t.Context(), f.local, f.claim, 102, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = ambiguousExecute(t, f, ref).Cart
	if f.snapshot.Version.Int64() != original.Version.Int64()+1 || len(f.snapshot.Units) != len(original.Units)+1 {
		t.Fatalf("ordinary edit=%+v", f.snapshot)
	}
	if !reflect.DeepEqual(f.view(t).Entries(), v.Entries()) {
		t.Fatal("ordinary edit implicitly resolved ambiguity")
	}
	// An old view is stale even if its referenced records are still ACTIVE.
	before := f.effects(t)
	out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 103, v, remove, ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID)))
	if !errors.Is(err, cart.ErrStale) || out != (cart.NativeActionRef{}) {
		t.Fatalf("old version view: %+v %v", out, err)
	}
	f.unchanged(t, before)
}

func TestAmbiguousEditResolutionAdmissionApplyAndImmutableReplay(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	first := f.mark(t, 100, cart.UnresolvedRemove)
	second := f.mark(t, 101, cart.UnresolvedRemove)
	v := f.view(t)
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	before := f.effects(t)
	ref, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 102, v, first, call)
	if err != nil || ref == (cart.NativeActionRef{}) {
		t.Fatalf("linked admission: %+v %v", ref, err)
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t), v) {
		t.Fatal("admission cleared active marker")
	}
	again, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 102, v, first, call)
	if err != nil || again != ref {
		t.Fatalf("admission replay: %+v %v", again, err)
	}
	for _, linked := range []cart.PendingEditRef{second, {}} {
		out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 102, v, linked, call)
		if !errors.Is(err, cart.ErrConflict) || out != (cart.NativeActionRef{}) {
			t.Fatalf("changed link: %+v %v", out, err)
		}
	}
	if out, err := f.s.AdmitNativeEdit(t.Context(), f.local, f.claim, 102, call); !errors.Is(err, cart.ErrConflict) || out != (cart.NativeActionRef{}) {
		t.Fatalf("ordinary link stripping: %+v %v", out, err)
	}
	result := ambiguousExecute(t, f, ref)
	f.snapshot = result.Cart
	if len(result.Cart.Units) != 1 || result.Cart.Version.Int64() != before.cart.Version.Int64()+1 || result.OperationID != ref.OperationID() {
		t.Fatalf("applied result=%+v", result)
	}
	entries := f.view(t).Entries()
	if len(entries) != 1 || entries[0].Ref != second {
		t.Fatalf("must resolve only linked record: %+v", entries)
	}
	// The remaining request might have meant the same physical unit. Its
	// destructive target is gone; it must remain blocked, not silently coalesce.
	currentView := f.view(t)
	beforeRejected := f.effects(t)
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 103, currentView, second, ambiguousCall(t, f.snapshot, ambiguousRemove(t, before.cart.Units[0].ID))); !errors.Is(err, cart.ErrNotFound) || out != (cart.NativeActionRef{}) {
		t.Fatalf("removed duplicate target: %+v %v", out, err)
	}
	f.unchanged(t, beforeRejected)
	prepareCall, err := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	prepare := ambiguousRegister(t, f, 104, prepareCall)
	if out, err := f.p.Prepare(t.Context(), prepare, f.snapshot.ID, f.snapshot.Version); !errors.Is(err, cart.ErrUnresolvedEdit) || !reflect.DeepEqual(out, cart.PreparedPickupSummary{}) {
		t.Fatalf("other record must still block pickup: %+v %v", out, err)
	}
	// Replay of a resolved registration is not a fresh resolution attempt.
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 102, v, first, call); err != nil || out != ref {
		t.Fatalf("resolved admission replay: %+v %v", out, err)
	}
	if out := ambiguousExecute(t, f, ref); !reflect.DeepEqual(out, result) {
		t.Fatal("native historical replay changed result")
	}
	if out, err := f.s.MarkUnresolvedEdit(t.Context(), f.local, f.claim, 100, before.cart.ID, before.cart.Version, cart.UnresolvedRemove); err != nil || out != first {
		t.Fatalf("mark replay reopened/reallocated record: %+v %v", out, err)
	}
	if !reflect.DeepEqual(f.view(t).Entries(), entries) {
		t.Fatal("replay altered active records")
	}
}

func TestAmbiguousEditWrongScopeKindAndStaleSetRejectWithoutEffects(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	first := f.mark(t, 100, cart.UnresolvedRemove)
	v := f.view(t)
	other, err := f.s.ProvisionLocal(t.Context(), "other-business", "other-sender")
	if err != nil {
		t.Fatal(err)
	}
	remove := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	addCall := ambiguousCall(t, f.snapshot, add(t, 1))
	cases := []struct {
		name  string
		local cart.LocalContext
		view  cart.PendingEditView
		ref   cart.PendingEditRef
		call  cart.Call
		want  error
	}{
		{"scope", other, v, first, remove, foundation.ErrStaleLease},
		{"kind", f.local, v, first, addCall, cart.ErrInput},
		{"zero-view", f.local, cart.PendingEditView{}, first, remove, cart.ErrInput},
		{"zero-ref", f.local, v, cart.PendingEditRef{}, remove, cart.ErrInput},
		{"non-edit", f.local, v, first, cart.CreateCall(), cart.ErrInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.effects(t)
			out, err := f.s.AdmitResolvedNativeEdit(t.Context(), tc.local, f.claim, 110, tc.view, tc.ref, tc.call)
			if !errors.Is(err, tc.want) || out != (cart.NativeActionRef{}) {
				t.Fatalf("out=%+v error=%v want=%v", out, err, tc.want)
			}
			f.unchanged(t, before)
			if !reflect.DeepEqual(f.view(t), v) {
				t.Fatal("rejection changed records")
			}
		})
	}
	second := f.mark(t, 101, cart.UnresolvedRemove)
	before := f.effects(t)
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 110, v, first, remove); !errors.Is(err, cart.ErrStale) || out != (cart.NativeActionRef{}) {
		t.Fatalf("unchanged cart but changed active set: %+v %v", out, err)
	}
	f.unchanged(t, before)
	fresh := f.view(t)
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 110, fresh, second, remove); err != nil || out == (cart.NativeActionRef{}) {
		t.Fatalf("rejections reserved canonical/native slot: %+v %v", out, err)
	}
}

func TestAmbiguousEditFailedNativeApplicationKeepsMarker(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	ref := f.mark(t, 100, cart.UnresolvedRemove)
	v := f.view(t)
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	work, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 101, v, ref, call)
	if err != nil {
		t.Fatal(err)
	}
	// Actual unrelated edit changes target version after admission, not a fake ID.
	f.snapshot = apply(t, f.fixture, cart.Result{Cart: f.snapshot}, add(t, 1)).Cart
	lease, ok, err := f.s.RecoverNativeEdit(t.Context(), work, time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	before := f.effects(t)
	out, err := f.s.ExecuteNativeEdit(t.Context(), lease)
	if !errors.Is(err, cart.ErrStale) || !reflect.DeepEqual(out, cart.Result{}) {
		t.Fatalf("stale application: %+v %v", out, err)
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t).Entries(), v.Entries()) {
		t.Fatal("failed application cleared guard")
	}
}

func TestAmbiguousEditCurrentClaimAndControlFence(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	ref := f.mark(t, 100, cart.UnresolvedRemove)
	v := f.view(t)
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	if _, err := f.control.SetMode(t.Context(), f.token, f.local.ConversationID(), conversation.ModeStaff, 0, "ambiguity-takeover"); err != nil {
		t.Fatal(err)
	}
	before := f.effects(t)
	if out, err := f.s.MarkUnresolvedEdit(t.Context(), f.local, f.claim, 100, f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove); !errors.Is(err, cart.ErrNativeAdmissionClosed) || out != (cart.PendingEditRef{}) {
		t.Fatalf("cached mark/control fence: %+v %v", out, err)
	}
	if out, err := f.s.ReadPendingEdits(t.Context(), f.local, f.claim, f.snapshot.ID, f.snapshot.Version); !errors.Is(err, cart.ErrNativeAdmissionClosed) || !reflect.DeepEqual(out, cart.PendingEditView{}) {
		t.Fatalf("view/control fence: %+v %v", out, err)
	}
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 101, v, ref, call); !errors.Is(err, cart.ErrNativeAdmissionClosed) || out != (cart.NativeActionRef{}) {
		t.Fatalf("resolution/control fence: %+v %v", out, err)
	}
	f.unchanged(t, before)
}

func TestAmbiguousEditRetiredClaimAndExactReadingSourceFence(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "remove that one")
	ref := f.mark(t, 100, cart.UnresolvedRemove)
	view := f.view(t)
	oldClaim := f.claim
	f.finish(t, f.x)
	f.turn(t, "I mean the first sandwich")
	before := f.effects(t)
	call := ambiguousCall(t, f.snapshot, ambiguousRemove(t, f.snapshot.Units[0].ID))
	if out, err := f.s.MarkUnresolvedEdit(t.Context(), f.local, oldClaim, 100, f.snapshot.ID, f.snapshot.Version, cart.UnresolvedRemove); !errors.Is(err, foundation.ErrStaleLease) || out != (cart.PendingEditRef{}) {
		t.Fatalf("mark replay with retired claim: %+v %v", out, err)
	}
	if out, err := f.s.ReadPendingEdits(t.Context(), f.local, oldClaim, f.snapshot.ID, f.snapshot.Version); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(out, cart.PendingEditView{}) {
		t.Fatalf("view with retired claim: %+v %v", out, err)
	}
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, oldClaim, 101, view, ref, call); !errors.Is(err, foundation.ErrStaleLease) || out != (cart.NativeActionRef{}) {
		t.Fatalf("resolution with retired claim: %+v %v", out, err)
	}
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 101, view, ref, call); !errors.Is(err, cart.ErrStale) || out != (cart.NativeActionRef{}) {
		t.Fatalf("old model-call/source view reused in new turn: %+v %v", out, err)
	}
	f.unchanged(t, before)
	current := f.view(t)
	if !reflect.DeepEqual(current.Entries(), view.Entries()) {
		t.Fatal("new inbound lost exact original request/ref")
	}
	if out, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 101, current, ref, call); err != nil || out == (cart.NativeActionRef{}) {
		t.Fatalf("fresh exact view/rejected slot reuse: %+v %v", out, err)
	}
}

func TestAmbiguousEditDomainFailureKeepsMarker(t *testing.T) {
	f := ambiguousSetup(t)
	f.turn(t, "add that one")
	ref := f.mark(t, 100, cart.UnresolvedAdd)
	view := f.view(t)
	work, err := f.s.AdmitResolvedNativeEdit(t.Context(), f.local, f.claim, 101, view, ref, ambiguousCall(t, f.snapshot, add(t, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.cat.SetAvailability(t.Context(), menu, 0, false); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := f.s.RecoverNativeEdit(t.Context(), work, time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	before := f.effects(t)
	out, err := f.s.ExecuteNativeEdit(t.Context(), lease)
	if !errors.Is(err, cart.ErrUnavailableItem) || !reflect.DeepEqual(out, cart.Result{}) {
		t.Fatalf("domain failure: %+v %v", out, err)
	}
	f.unchanged(t, before)
	if !reflect.DeepEqual(f.view(t), view) {
		t.Fatal("domain failure resolved marker")
	}
}
