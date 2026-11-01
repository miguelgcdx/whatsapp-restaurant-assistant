package cart_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

func pickupEffects(t *testing.T, f *pickupFixture) (cart.PickupOrderPage, []cart.PickupLocalIntent, cart.Snapshot) {
	t.Helper()
	orders, e := f.p.ListOrders(t.Context(), "menu-operator", cart.PickupPageRequest{})
	if e != nil {
		t.Fatal(e)
	}
	intents, e := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.Read(t.Context(), f.local, f.snapshot.ID)
	if e != nil {
		t.Fatal(e)
	}
	return orders, intents, snapshot
}
func pickupUnchanged(t *testing.T, f *pickupFixture, orders cart.PickupOrderPage, intents []cart.PickupLocalIntent, snapshot cart.Snapshot) {
	t.Helper()
	a, b, c := pickupEffects(t, f)
	if !reflect.DeepEqual(a, orders) || !reflect.DeepEqual(b, intents) || !reflect.DeepEqual(c, snapshot) {
		t.Fatal("rejected attempt changed public orders, receiver intents, or cart")
	}
}
func TestPickupDomainCompletionRejectionsPreservePublicEffects(t *testing.T) {
	f := pickupSetup(t)
	s, first := f.prepare(t)
	f.present(t, s)
	f.finish(t, first)
	x := f.inbound(t, "Nothing else, thank you. Please place the order.")
	_, original := f.evidence(t, x)
	for _, tc := range []struct {
		name string
		edit func(*cart.PickupCompletionEvidence, *cart.PickupExecution)
		want error
	}{
		{"not-complete", func(e *cart.PickupCompletionEvidence, _ *cart.PickupExecution) { e.Decision = cart.PickupNotComplete }, cart.ErrPickupConfirmationRequired},
		{"policy", func(e *cart.PickupCompletionEvidence, _ *cart.PickupExecution) {
			e.AssessmentVersion = "model-supplied-policy"
		}, cart.ErrPickupConfirmationRequired},
		{"digest", func(e *cart.PickupCompletionEvidence, _ *cart.PickupExecution) {
			e.ContextDigest = sha256.Sum256([]byte("detached changed text"))
		}, cart.ErrPickupConfirmationRequired},
		{"provider-id-not-stored-id", func(e *cart.PickupCompletionEvidence, _ *cart.PickupExecution) {
			e.InboundEventID = first.Source.EventUUID()
		}, cart.ErrPickupConfirmationRequired},
		{"claim-text", func(_ *cart.PickupCompletionEvidence, x *cart.PickupExecution) { x.Claim.Event.Text = "invented" }, foundation.ErrStaleLease},
		{"claim-generation", func(_ *cart.PickupCompletionEvidence, x *cart.PickupExecution) { x.Claim.Generation++ }, foundation.ErrStaleLease},
		{"control", func(_ *cart.PickupCompletionEvidence, x *cart.PickupExecution) { x.ControlVersion = version(t, 99) }, cart.ErrStale},
		{"source", func(_ *cart.PickupCompletionEvidence, x *cart.PickupExecution) { x.Source = first.Source }, foundation.ErrStaleLease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := original
			execution := x
			tc.edit(&in, &execution)
			f.next++
			execution = f.register(t, execution, pickupSubmitCall(t, in))
			before, intents, snapshot := pickupEffects(t, f)
			got, e := f.p.Submit(t.Context(), execution, in)
			if !errors.Is(e, tc.want) || !reflect.DeepEqual(got, cart.PickupSubmission{}) {
				t.Fatalf("%+v %v want %v", got, e, tc.want)
			}
			pickupUnchanged(t, f, before, intents, snapshot)
		})
	}
	// The original valid intent remains usable after all rejected attempts.
	f.next++
	x = f.register(t, x, pickupSubmitCall(t, original))
	out, e := f.p.Submit(t.Context(), x, original)
	if e != nil || out.Order.Status != cart.PickupSubmitted {
		t.Fatal(out, e)
	}
}

// These semantic decisions are synthetic fixtures, NOT Go interpretation of text.
func TestPickupDomainSyntheticNonCompletionCasesAndHistory(t *testing.T) {
	f := pickupSetup(t)
	s, first := f.prepare(t)
	f.present(t, s)
	f.finish(t, first)
	for _, text := range []string{"Yes, add a drink", "no onions", "maybe", "Make it one burger"} {
		x := f.inbound(t, text)
		c, e := f.evidence(t, x)
		if c.InboundText != text {
			t.Fatal(c)
		}
		e.Decision = cart.PickupNotComplete
		x = f.register(t, x, pickupSubmitCall(t, e))
		before, intents, snapshot := pickupEffects(t, f)
		if _, err := f.p.Submit(t.Context(), x, e); !errors.Is(err, cart.ErrPickupConfirmationRequired) {
			t.Fatal(err)
		}
		pickupUnchanged(t, f, before, intents, snapshot)
		f.finish(t, x)
	}
	*f.reply = "Pickup address: exact fictional restaurant"
	clarification := f.inbound(t, "What is the pickup address?")
	answer, e := f.gate.RunAndQueue(t.Context(), clarification.Source, clarification.Claim)
	if e != nil {
		t.Fatal(e)
	}
	// Only the actual receiver acknowledgment permits restaurant text into input.
	if e = f.p.RecordLocalReply(t.Context(), f.local, answer.OutboxID, "received:"+answer.OutboxID, sha256.Sum256([]byte(answer.Content))); e != nil {
		t.Fatal(e)
	}
	unrelated := f.inbound(t, "Do you need utensils?")
	c, ev := f.evidence(t, unrelated)
	if len(c.History) != 6 || c.History[4].Text != "What is the pickup address?" || c.History[5] != (cart.PickupContextMessage{SourceID: answer.OutboxID, Speaker: cart.PickupSpeakerRestaurant, Text: answer.Content}) {
		t.Fatalf("all exact intervening turns: %+v", c.History)
	}
	*f.reply = "Do you need utensils?"
	question := f.finish(t, unrelated)
	if e = f.p.RecordLocalReply(t.Context(), f.local, question.OutboxID, "received:"+question.OutboxID, sha256.Sum256([]byte(question.Content))); e != nil {
		t.Fatal(e)
	}
	yes := f.inbound(t, "yes")
	c, ev = f.evidence(t, yes)
	if len(c.History) != 8 || c.History[7].Text != "Do you need utensils?" || c.History[7].Speaker != cart.PickupSpeakerRestaurant {
		t.Fatal(c.History)
	}
	ev.Decision = cart.PickupNotComplete
	yes = f.register(t, yes, pickupSubmitCall(t, ev))
	before, intents, snapshot := pickupEffects(t, f)
	if _, e = f.p.Submit(t.Context(), yes, ev); !errors.Is(e, cart.ErrPickupConfirmationRequired) {
		t.Fatal(e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	f.finish(t, yes)
	done := f.inbound(t, "That’s all, please place it.")
	c, ev = f.evidence(t, done)
	if c.CandidateID != s.CandidateID || c.SummaryText != s.Intent.Text || len(c.History) != 9 {
		t.Fatal(c)
	}
	done = f.register(t, done, pickupSubmitCall(t, ev))
	out, e := f.p.Submit(t.Context(), done, ev)
	if e != nil || out.Order.Confirmed.ID != s.CandidateID {
		t.Fatal(out, e)
	}
}

func TestPickupDomainMaterialFreshnessAndDetachedCopies(t *testing.T) {
	for _, name := range []string{"cart", "availability", "missing-presentation", "cross-context", "detached", "newer-inbound"} {
		t.Run(name, func(t *testing.T) {
			f := pickupSetup(t)
			s, first := f.prepare(t)
			if name != "missing-presentation" {
				f.present(t, s)
			}
			f.finish(t, first)
			x := f.inbound(t, "No, that’s all")
			if name == "missing-presentation" {
				before, intents, snapshot := pickupEffects(t, f)
				if _, e := f.p.CompletionContext(t.Context(), x); !errors.Is(e, cart.ErrPickupSummaryRequired) {
					t.Fatal(e)
				}
				pickupUnchanged(t, f, before, intents, snapshot)
				return
			}
			c, ev := f.evidence(t, x)
			switch name {
			case "cart":
				f.snapshot = apply(t, f.fixture, cart.Result{Cart: f.snapshot}, add(t, 1)).Cart
			case "availability":
				if _, e := f.cat.SetAvailability(t.Context(), menu, 0, false); e != nil {
					t.Fatal(e)
				}
			case "cross-context":
				other, e := f.s.ProvisionLocal(t.Context(), "other", "fictional-sender")
				if e != nil {
					t.Fatal(e)
				}
				x.Local = other
			case "detached":
				c.SummaryText = "forged"
				c.History = append(c.History, cart.PickupContextMessage{SourceID: "invented", Speaker: cart.PickupSpeakerRestaurant, Text: "Do you need utensils?"})
				ev.ContextDigest = sha256.Sum256([]byte(c.SummaryText))
			case "newer-inbound":
				if _, _, e := f.s.AcceptMenu(t.Context(), f.local, foundation.Event{ID: "later-actual", Sender: "fictional-sender", Text: "Actually, add a drink"}, time.Now()); e != nil {
					t.Fatal(e)
				}
			}
			f.next++
			// Wrong-context registration itself must not rebind the source; use the valid
			// original registration and vary only the supplied execution local context.
			local := x.Local
			x.Local = f.local
			x = f.register(t, x, pickupSubmitCall(t, ev))
			x.Local = local
			before, intents, snapshot := pickupEffects(t, f)
			out, e := f.p.Submit(t.Context(), x, ev)
			want := cart.ErrPickupSummaryRequired
			if name == "detached" {
				want = cart.ErrPickupConfirmationRequired
			}
			if name == "cross-context" {
				want = cart.ErrNotFound
			}
			if name == "newer-inbound" {
				want = cart.ErrStale
			}
			if !errors.Is(e, want) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
				t.Fatalf("%+v %v want %v", out, e, want)
			}
			pickupUnchanged(t, f, before, intents, snapshot)
		})
	}
}

func TestPickupDomainControlAndEditRace(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-staff-confirmation", true: "edit-submit-race"}[race], func(t *testing.T) {
			f := pickupSetup(t)
			s, first := f.prepare(t)
			f.present(t, s)
			f.finish(t, first)
			x := f.inbound(t, "Please place it")
			_, ev := f.evidence(t, x)
			x = f.register(t, x, pickupSubmitCall(t, ev))
			if !race {
				if _, e := f.control.SetMode(t.Context(), f.token, f.local.ConversationID(), conversation.ModeStaff, 0, "takeover"); e != nil {
					t.Fatal(e)
				}
				before, intents, snapshot := pickupEffects(t, f)
				if _, e := f.p.Submit(t.Context(), x, ev); !errors.Is(e, cart.ErrStale) {
					t.Fatal(e)
				}
				pickupUnchanged(t, f, before, intents, snapshot)
				x.ControlVersion = version(t, 1)
				_, ev = f.evidence(t, x)
				f.next++
				x = f.register(t, x, pickupSubmitCall(t, ev))
				if _, e := f.p.Submit(t.Context(), x, ev); e != nil {
					t.Fatal(e)
				}
				state, e := f.control.Read(t.Context(), f.token, f.local.ConversationID())
				if e != nil || state.Mode != conversation.ModeStaff || state.Version != 1 {
					t.Fatal(state, e)
				}
				return
			}
			var out cart.PickupSubmission
			var submitErr, editErr error
			var edited cart.Result
			var wg sync.WaitGroup
			wg.Go(func() { out, submitErr = f.p.Submit(t.Context(), x, ev) })
			wg.Go(func() {
				edited, editErr = f.s.Apply(t.Context(), f.local, operation(t), f.snapshot.ID, f.snapshot.Version, add(t, 1))
			})
			wg.Wait()
			if editErr != nil {
				t.Fatal(editErr)
			}
			orders, _, _ := pickupEffects(t, f)
			if submitErr == nil {
				if len(orders.Items) != 1 || out.Order.Confirmed.UnitCount != 2 || out.Order.Total.AmountMinor != 1300 {
					t.Fatal(out, orders)
				}
			} else if !errors.Is(submitErr, cart.ErrPickupSummaryRequired) || len(orders.Items) != 0 {
				t.Fatal(submitErr, orders)
			}
			f.snapshot = edited.Cart
			// A later draft version never creates a second initial order.
			if submitErr == nil {
				call, _ := cart.PreparePickupCall(edited.Cart.ID, edited.Cart.Version)
				f.next++
				x = f.register(t, x, call)
				if _, e := f.p.Prepare(t.Context(), x, edited.Cart.ID, edited.Cart.Version); !errors.Is(e, cart.ErrPickupPolicyUnresolved) {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestPickupDomainFullHistoryIsBoundedNotTruncated(t *testing.T) {
	f := pickupSetup(t)
	s, first := f.prepare(t)
	f.present(t, s)
	f.finish(t, first)
	// Full exact texts exceed the assessment byte budget; no last-N fallback.
	for range 17 {
		x := f.inbound(t, strings.Repeat("é", 2000))
		f.finish(t, x)
	}
	x := f.inbound(t, "Please place it")
	before, intents, snapshot := pickupEffects(t, f)
	c, e := f.p.CompletionContext(t.Context(), x)
	if !errors.Is(e, cart.ErrLimit) || !reflect.DeepEqual(c, cart.PickupCompletionContext{}) {
		t.Fatal(c, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
}

func TestPickupDomainGapExactHistoryBoundary(t *testing.T) {
	f := pickupSetup(t)
	summary, first := f.prepare(t)
	f.present(t, summary)
	f.finish(t, first)
	want := []cart.PickupContextMessage{}
	for i := 0; i < 31; i++ {
		text := fmt.Sprintf("Exact customer turn %02d: é", i)
		x := f.inbound(t, text)
		*f.reply = fmt.Sprintf("Exact restaurant turn %02d: utensils?", i)
		reply := f.finish(t, x)
		if e := f.p.RecordLocalReply(t.Context(), f.local, reply.OutboxID, "boundary:"+reply.OutboxID, sha256.Sum256([]byte(reply.Content))); e != nil {
			t.Fatal(e)
		}
		want = append(want, cart.PickupContextMessage{SourceID: x.Source.EventUUID(), Speaker: cart.PickupSpeakerCustomer, Text: text}, cart.PickupContextMessage{SourceID: reply.OutboxID, Speaker: cart.PickupSpeakerRestaurant, Text: reply.Content})
	}
	x := f.inbound(t, "Nothing else, please place it")
	before, intents, snapshot := pickupEffects(t, f)
	context, evidence := f.evidence(t, x)
	if len(context.History) != 62 || !reflect.DeepEqual(context.History, want) || context.InboundEventID != x.Source.EventUUID() || context.InboundText != x.Claim.Event.Text || context.CandidateID != summary.CandidateID || context.SummaryText != summary.Intent.Text || context.Digest == [32]byte{} {
		t.Fatal(context)
	}
	again, e := f.p.CompletionContext(t.Context(), x)
	if e != nil || !reflect.DeepEqual(again, context) {
		t.Fatal(again, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	x = f.register(t, x, pickupSubmitCall(t, evidence))
	out, e := f.p.Submit(t.Context(), x, evidence)
	if e != nil || out.Order.Status != cart.PickupSubmitted || out.Order.Payment.State != cart.PickupUnpaid || out.Order.Total != summary.Total || out.Order.Confirmed.UnitCount != 2 {
		t.Fatal(out, e)
	}
	f.finish(t, x) // Its queued but unpresented reply is not a history turn.
	// The previous current inbound is now the 63rd intervening customer turn.
	tooMany := f.inbound(t, "Please place it again")
	before, intents, snapshot = pickupEffects(t, f)
	if len(before.Items) != 1 || len(intents) != 2 {
		t.Fatal(before, intents)
	}
	got, e := f.p.CompletionContext(t.Context(), tooMany)
	if !errors.Is(e, cart.ErrLimit) || !reflect.DeepEqual(got, cart.PickupCompletionContext{}) {
		t.Fatal(got, e)
	}
	evidence.InboundEventID = tooMany.Source.EventUUID()
	tooMany = f.register(t, tooMany, pickupSubmitCall(t, evidence))
	attempt, e := f.p.Submit(t.Context(), tooMany, evidence)
	if !errors.Is(e, cart.ErrLimit) || !reflect.DeepEqual(attempt, cart.PickupSubmission{}) {
		t.Fatal(attempt, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
}
