package menugate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/menugate"
)

const proposal = `{"action":"add","expected_version":"0","menu_id":"10000000-0000-4000-8000-000000000001","count":2}`

type addTurn struct {
	turn
	claim cart.MenuClaim
	cart  cart.Snapshot
}

func prepareAdd(t *testing.T, f *fixture, text string, withCart bool) addTurn {
	t.Helper()
	local, _ := nativeLocal(t, f)
	var snapshot cart.Snapshot
	if withCart {
		op, err := cart.NewOperationID()
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.cart.Create(t.Context(), local, op)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = result.Cart
	}
	name := fmt.Sprintf("native-%d", f.next)
	event := foundation.Event{ID: name, Sender: name, Text: text}
	admission, created, err := f.cart.AcceptMenu(t.Context(), local, event, time.Now().UTC())
	if err != nil || !created {
		t.Fatalf("accept: %+v %v %v", admission, created, err)
	}
	claim, ok, err := f.cart.ClaimMenu(t.Context(), local, time.Minute)
	if err != nil || !ok || claim.Source() != admission.Source {
		t.Fatalf("claim: %+v %v %v", claim, ok, err)
	}
	return addTurn{turn{local, claim.Source(), claim.Job()}, claim, snapshot}
}

func proposalResponse(args string) *http.Response {
	return response(cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{{ID: "add-call", Type: "function", Function: cc.FunctionCall{Name: "propose_add", Arguments: args}}}}, "tool_calls")
}

func openAdd(t *testing.T, f *fixture, fn transport) *menugate.AddCaller {
	t.Helper()
	c, err := menugate.OpenAddCaller(t.Context(), f.dsn, restaurant, client(t, fn), 321)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func assertAddUnchanged(t *testing.T, f *fixture, v addTurn, before foundation.Snapshot, admission cart.Admission, control conversation.State) {
	t.Helper()
	if v.cart.ID != (cart.CartID{}) {
		got, err := f.cart.Read(t.Context(), v.local, v.cart.ID)
		if err != nil || !reflect.DeepEqual(got, v.cart) {
			t.Fatalf("cart changed: %+v %v", got, err)
		}
	}
	if got := f.inspect(t, v.turn); got != before {
		t.Fatalf("job changed: %+v", got)
	}
	if got, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID); err != nil || got != admission {
		t.Fatalf("admission changed: %+v %v", got, err)
	}
	if got, err := f.controls.Read(t.Context(), f.token, v.local.ConversationID()); err != nil || got != control {
		t.Fatalf("control changed: %+v %v", got, err)
	}
	g, err := menugate.Open(t.Context(), f.dsn, restaurant, client(t, func(*http.Request) (*http.Response, error) { t.Error("unexpected model I/O"); return answer(), nil }), 321)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	queued, err := g.ReadQueued(t.Context(), v.source)
	if !errors.Is(err, menugate.ErrNotFound) || queued != (menugate.Receipt{}) {
		t.Fatalf("reply queue changed: %+v %v", queued, err)
	}
}

// A different public canonical intent can occupy slot zero only if the rejected
// attempt did not reserve it. This probe runs after all no-effect observations.
func assertAddSlotFree(t *testing.T, f *fixture, v addTurn) {
	t.Helper()
	if v.cart.ID == (cart.CartID{}) {
		op, err := cart.NewOperationID()
		if err != nil {
			t.Fatal(err)
		}
		created, err := f.cart.Create(t.Context(), v.local, op)
		if err != nil {
			t.Fatal(err)
		}
		v.cart = created.Cart
	}
	menu, err := cart.ParseMenuID(item)
	if err != nil {
		t.Fatal(err)
	}
	action, err := cart.Add(menu, 1)
	if err != nil {
		t.Fatal(err)
	}
	call, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.cart.RegisterExecution(t.Context(), v.local, v.job.Event.ID, 0, call)
	if err != nil || ref.OperationID() == (cart.OperationID{}) {
		t.Fatalf("rejection reserved slot: %+v %v", ref, err)
	}
}

func TestAddCaller(t *testing.T) {
	f := setup(t)
	// Fixture preparation makes the pre-existing stored menu item eligible.
	f.exec(t, `UPDATE app.menu_items SET available=true WHERE restaurant_id=$1 AND id=$2`, restaurant, item)
	observe := func(t *testing.T, v addTurn) (foundation.Snapshot, cart.Admission, conversation.State) {
		t.Helper()
		admission, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID)
		if err != nil {
			t.Fatal(err)
		}
		control, err := f.controls.Read(t.Context(), f.token, v.local.ConversationID())
		if err != nil {
			t.Fatal(err)
		}
		return f.inspect(t, v.turn), admission, control
	}
	t.Run("Admission", func(t *testing.T) {
		t.Run("pending-reference", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			var calls atomic.Int32
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls.Add(1); return proposalResponse(proposal), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if err != nil || ref == (cart.NativeActionRef{}) || ref.OperationID() == (cart.OperationID{}) || calls.Load() != 1 {
				t.Fatalf("admit: %+v %v calls=%d", ref, err, calls.Load())
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			wire, err := json.Marshal(ref)
			if err != nil || string(wire) != "{}" {
				t.Fatalf("reference is not opaque: %s %v", wire, err)
			}
			menu, err := cart.ParseMenuID(item)
			if err != nil {
				t.Fatal(err)
			}
			action, err := cart.Add(menu, 2)
			if err != nil {
				t.Fatal(err)
			}
			call, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			registered, err := f.cart.RegisterExecution(t.Context(), v.local, v.job.Event.ID, 0, call)
			if err != nil || registered.OperationID() != ref.OperationID() {
				t.Fatalf("registered identity: %+v %v", registered, err)
			}
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { t.Error("unexpected model call"); return answer(), nil }))
			queued, err := g.ReadQueued(t.Context(), v.source)
			if !errors.Is(err, menugate.ErrNotFound) || queued != (menugate.Receipt{}) {
				t.Fatalf("unexpected queued reply: %+v %v", queued, err)
			}
		})
		for _, tc := range []struct{ name, args string }{
			{"malformed", `{"action":"add"}`},
			{"foreign-cart", `{"action":"add","expected_version":"0","menu_id":"10000000-0000-4000-8000-000000000001","count":2,"cart_id":"20000000-0000-4000-8000-000000000002"}`},
			{"remove", `{"action":"remove","expected_version":"0","unit_ids":["30000000-0000-4000-8000-000000000001"]}`},
			{"note", `{"action":"set_note","expected_version":"0","unit_ids":["30000000-0000-4000-8000-000000000001"],"note":"hello"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				v := prepareAdd(t, f, "add two stored closed menus", true)
				calls := 0
				c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(tc.args), nil })
				before, admission, control := observe(t, v)
				ref, err := c.AdmitAdd(t.Context(), v.claim)
				if !errors.Is(err, menuengine.ErrProtocol) || ref != (cart.NativeActionRef{}) || calls != 1 {
					t.Fatalf("reject: %+v %v calls=%d", ref, err, calls)
				}
				assertAddUnchanged(t, f, v, before, admission, control)
				menu, err := cart.ParseMenuID(item)
				if err != nil {
					t.Fatal(err)
				}
				action, err := cart.Add(menu, 1)
				if err != nil {
					t.Fatal(err)
				}
				call, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
				if err != nil {
					t.Fatal(err)
				}
				probe, err := f.cart.AdmitNativeEdit(t.Context(), v.local, v.claim, 0, call)
				if err != nil || probe == (cart.NativeActionRef{}) {
					t.Fatalf("rejection reserved slot: %+v %v", probe, err)
				}
			})
		}
	})
	t.Run("Consent", func(t *testing.T) {
		if menugate.ErrClarificationRequired.Error() != "cart edit requires clarification" {
			t.Fatal("clarification error changed")
		}
		for _, tc := range []struct {
			name, text string
			withCart   bool
		}{{"question", "can you add two stored closed menus?", true}, {"absent-cart", "add two stored closed menus", false}} {
			t.Run(tc.name, func(t *testing.T) {
				v := prepareAdd(t, f, tc.text, tc.withCart)
				calls := 0
				c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
				before, admission, control := observe(t, v)
				ref, err := c.AdmitAdd(t.Context(), v.claim)
				if !errors.Is(err, menugate.ErrClarificationRequired) || ref != (cart.NativeActionRef{}) || calls != 0 {
					t.Fatalf("clarify: %+v %v calls=%d", ref, err, calls)
				}
				assertAddUnchanged(t, f, v, before, admission, control)
				assertAddSlotFree(t, f, v)
			})
		}
	})
	t.Run("Replay", func(t *testing.T) {
		v := prepareAdd(t, f, "add two stored closed menus", true)
		calls := 0
		c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
		before, admission, control := observe(t, v)
		first, err := c.AdmitAdd(t.Context(), v.claim)
		if err != nil || first == (cart.NativeActionRef{}) {
			t.Fatal(first, err)
		}
		again, err := c.AdmitAdd(t.Context(), v.claim)
		if err != nil || again != first || calls != 2 {
			t.Fatalf("replay: %+v %v calls=%d", again, err, calls)
		}
		assertAddUnchanged(t, f, v, before, admission, control)
		menu, err := cart.ParseMenuID(item)
		if err != nil {
			t.Fatal(err)
		}
		action, err := cart.Add(menu, 3)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := f.cart.RegisterExecution(t.Context(), v.local, v.job.Event.ID, 0, changed); !errors.Is(err, cart.ErrConflict) || got != (cart.Execution{}) {
			t.Fatalf("changed intent: %+v %v", got, err)
		}
		assertAddUnchanged(t, f, v, before, admission, control)
		t.Run("existing-slot-conflict", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			menu, err := cart.ParseMenuID(item)
			if err != nil {
				t.Fatal(err)
			}
			action, err := cart.Add(menu, 1)
			if err != nil {
				t.Fatal(err)
			}
			call, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := f.cart.RegisterExecution(t.Context(), v.local, v.job.Event.ID, 0, call)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, cart.ErrConflict) || ref != (cart.NativeActionRef{}) || calls != 1 {
				t.Fatalf("occupied slot: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			again, err := f.cart.RegisterExecution(t.Context(), v.local, v.job.Event.ID, 0, call)
			if err != nil || again != prior {
				t.Fatalf("occupied identity changed: %+v %v", again, err)
			}
		})
		t.Run("changed-model-proposal", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			args := `{"action":"add","expected_version":"0","menu_id":"10000000-0000-4000-8000-000000000001","count":3}`
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(args), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, menuengine.ErrProtocol) || ref != (cart.NativeActionRef{}) || calls != 1 {
				t.Fatalf("changed proposal: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			menu, err := cart.ParseMenuID(item)
			if err != nil {
				t.Fatal(err)
			}
			action, err := cart.Add(menu, 1)
			if err != nil {
				t.Fatal(err)
			}
			call, err := cart.EditCall(v.cart.ID, v.cart.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := f.cart.AdmitNativeEdit(t.Context(), v.local, v.claim, 0, call)
			if err != nil || probe == (cart.NativeActionRef{}) {
				t.Fatalf("changed proposal occupied slot: %+v %v", probe, err)
			}
		})
		t.Run("stale-during-model", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			entered, release := make(chan struct{}), make(chan struct{})
			c := openAdd(t, f, func(r *http.Request) (*http.Response, error) {
				close(entered)
				select {
				case <-release:
					return proposalResponse(proposal), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})
			type result struct {
				ref cart.NativeActionRef
				err error
			}
			done := make(chan result, 1)
			finished := make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer func() { cancel(); <-finished }()
			go func() { defer close(finished); ref, err := c.AdmitAdd(ctx, v.claim); done <- result{ref, err} }()
			await(t, entered)
			menu, err := cart.ParseMenuID(item)
			if err != nil {
				t.Fatal(err)
			}
			action, err := cart.Add(menu, 1)
			if err != nil {
				t.Fatal(err)
			}
			op, err := cart.NewOperationID()
			if err != nil {
				t.Fatal(err)
			}
			updated, err := f.cart.Apply(t.Context(), v.local, op, v.cart.ID, v.cart.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			v.cart = updated.Cart
			before, admission, control := observe(t, v)
			close(release)
			got := await(t, done)
			if !errors.Is(got.err, cart.ErrStale) || got.ref != (cart.NativeActionRef{}) {
				t.Fatalf("stale: %+v %v", got.ref, got.err)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			freshAction, err := cart.Add(menu, 1)
			if err != nil {
				t.Fatal(err)
			}
			freshCall, err := cart.EditCall(v.cart.ID, v.cart.Version, freshAction)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := f.cart.AdmitNativeEdit(t.Context(), v.local, v.claim, 0, freshCall)
			if err != nil || probe == (cart.NativeActionRef{}) {
				t.Fatalf("stale attempt occupied slot: %+v %v", probe, err)
			}
		})
	})
	t.Run("Authority", func(t *testing.T) {
		v := prepareAdd(t, f, "add two stored closed menus", true)
		calls := 0
		c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
		before, admission, control := observe(t, v)
		var nilCaller *menugate.AddCaller
		for _, invalid := range []struct {
			caller *menugate.AddCaller
			ctx    context.Context
			claim  cart.MenuClaim
		}{{nilCaller, t.Context(), v.claim}, {new(menugate.AddCaller), t.Context(), v.claim}, {c, nil, v.claim}} {
			ref, err := invalid.caller.AdmitAdd(invalid.ctx, invalid.claim)
			if !errors.Is(err, menugate.ErrInput) || ref != (cart.NativeActionRef{}) || calls != 0 {
				t.Fatalf("invalid input: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
		}
		ref, err := c.AdmitAdd(t.Context(), cart.MenuClaim{})
		if !errors.Is(err, menugate.ErrInput) || ref != (cart.NativeActionRef{}) || calls != 0 {
			t.Fatalf("zero claim: %+v %v", ref, err)
		}
		assertAddUnchanged(t, f, v, before, admission, control)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		ref, err = c.AdmitAdd(ctx, v.claim)
		if !errors.Is(err, context.Canceled) || ref != (cart.NativeActionRef{}) || calls != 0 {
			t.Fatalf("cancellation: %+v %v", ref, err)
		}
		assertAddUnchanged(t, f, v, before, admission, control)
		f.mode(t, v.turn, conversation.ModeStaff)
		before, admission, control = observe(t, v)
		ref, err = c.AdmitAdd(t.Context(), v.claim)
		if !errors.Is(err, menugate.ErrControlChanged) || ref != (cart.NativeActionRef{}) || calls != 0 {
			t.Fatalf("stale authority: %+v %v", ref, err)
		}
		assertAddUnchanged(t, f, v, before, admission, control)
		assertAddSlotFree(t, f, v)
		t.Run("expired-claim", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, v.job.ID)
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, foundation.ErrStaleLease) || ref != (cart.NativeActionRef{}) || calls != 0 {
				t.Fatalf("expired: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			assertAddSlotFree(t, f, v)
		})
		t.Run("stale-epoch", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			f.mode(t, v.turn, conversation.ModeStaff)
			f.mode(t, v.turn, conversation.ModeAI)
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, menugate.ErrControlChanged) || ref != (cart.NativeActionRef{}) || calls != 0 {
				t.Fatalf("stale epoch: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			assertAddSlotFree(t, f, v)
		})
		t.Run("foreign-restaurant", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			calls := 0
			c, err := menugate.OpenAddCaller(t.Context(), f.dsn, foreignRestaurant, client(t, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil }), 321)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, menugate.ErrNotFound) || ref != (cart.NativeActionRef{}) || calls != 0 {
				t.Fatalf("foreign: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			assertAddSlotFree(t, f, v)
		})
		t.Run("takeover-during-model", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			calls := 0
			c := openAdd(t, f, func(r *http.Request) (*http.Response, error) {
				calls++
				close(entered)
				select {
				case <-release:
					return proposalResponse(proposal), nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})
			type outcome struct {
				ref cart.NativeActionRef
				err error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer func() { cancel(); <-finished }()
			go func() { defer close(finished); ref, err := c.AdmitAdd(ctx, v.claim); done <- outcome{ref, err} }()
			await(t, entered)
			f.mode(t, v.turn, conversation.ModeStaff)
			before, admission, control := observe(t, v)
			close(release)
			got := await(t, done)
			if !errors.Is(got.err, cart.ErrNativeAdmissionClosed) || got.ref != (cart.NativeActionRef{}) || calls != 1 {
				t.Fatalf("takeover: %+v %v calls=%d", got.ref, got.err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			assertAddSlotFree(t, f, v)
		})
		t.Run("unavailable-item", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			f.exec(t, `UPDATE app.menu_items SET available=false WHERE restaurant_id=$1 AND id=$2`, restaurant, item)
			defer f.exec(t, `UPDATE app.menu_items SET available=true WHERE restaurant_id=$1 AND id=$2`, restaurant, item)
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
			before, admission, control := observe(t, v)
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if !errors.Is(err, cart.ErrUnavailableItem) || ref != (cart.NativeActionRef{}) || calls != 1 {
				t.Fatalf("unavailable: %+v %v calls=%d", ref, err, calls)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
			assertAddSlotFree(t, f, v)
		})
	})
	t.Run("Takeover", func(t *testing.T) {
		t.Run("admitted-work-survives", func(t *testing.T) {
			v := prepareAdd(t, f, "add two stored closed menus", true)
			calls := 0
			c := openAdd(t, f, func(*http.Request) (*http.Response, error) { calls++; return proposalResponse(proposal), nil })
			ref, err := c.AdmitAdd(t.Context(), v.claim)
			if err != nil || ref == (cart.NativeActionRef{}) {
				t.Fatal(ref, err)
			}
			f.mode(t, v.turn, conversation.ModeStaff)
			before, admission, control := observe(t, v)
			business, ok, err := f.cart.RecoverNativeEdit(t.Context(), ref, time.Minute)
			if err != nil || !ok || business.Ref() != ref {
				t.Fatalf("work lost on takeover: %+v %v %v", business, ok, err)
			}
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { calls++; return answer(), nil }))
			receipt, err := g.RunAndQueue(t.Context(), v.source, v.job)
			if !errors.Is(err, menugate.ErrControlChanged) || receipt != (menugate.Receipt{}) || calls != 1 {
				t.Fatalf("stale reply: %+v %v calls=%d", receipt, err, calls)
			}
			queued, err := g.ReadQueued(t.Context(), v.source)
			if !errors.Is(err, menugate.ErrNotFound) || queued != (menugate.Receipt{}) {
				t.Fatalf("takeover queued reply: %+v %v", queued, err)
			}
			assertAddUnchanged(t, f, v, before, admission, control)
		})
	})
	t.Run("Legacy", func(t *testing.T) {
		t.Run("get-menu-and-queue", func(t *testing.T) {
			v := f.fresh(t, conversation.ModeAI)
			calls := 0
			g := openGate(t, f, client(t, func(req *http.Request) (*http.Response, error) {
				calls++
				var wire struct {
					Tools []struct {
						Function cc.ToolDefinition `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				if len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "get_menu" {
					t.Error("legacy tool surface changed")
				}
				if calls == 1 || calls == 3 {
					return menuCall(), nil
				}
				return answer(), nil
			}))
			result, err := g.Run(t.Context(), v.source, v.job)
			if err != nil || result.Reply != "ephemeral proposal" || result.Menu == nil || result.Menu.Open || calls != 2 {
				t.Fatalf("legacy run: %+v %v", result, err)
			}
			receipt, err := g.RunAndQueue(t.Context(), v.source, v.job)
			if err != nil || receipt.OutboxID == "" || receipt.Content != "ephemeral proposal" || receipt.State != "pending" || calls != 4 {
				t.Fatalf("legacy queue: %+v %v", receipt, err)
			}
			queued, err := g.ReadQueued(t.Context(), v.source)
			if err != nil || queued != receipt {
				t.Fatalf("legacy queue readback: %+v %v", queued, err)
			}
		})
	})
}
