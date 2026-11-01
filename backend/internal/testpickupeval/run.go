package testpickupeval

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

func validText(text string) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" &&
		!strings.ContainsRune(text, 0) && utf8.RuneCountInString(text) <= 4000
}

func validExtension(s Scenario) bool {
	historyTurns := 2*len(s.History) + len(s.Edits)
	if s.PostSummaryQuestion != "" {
		if !validText(s.PostSummaryQuestion) {
			return false
		}
		historyTurns++
	}
	if historyTurns > cart.PickupCompletionHistoryLimit || len(s.Edits) > 2 ||
		s.MissingPresentation && (s.PostSummaryQuestion != "" || len(s.Edits) != 0 || s.RefreshSummary) ||
		s.RefreshSummary && len(s.Edits) == 0 ||
		s.SecondInbound != nil && (s.Inbound == nil || !validText(*s.SecondInbound)) {
		return false
	}
	for i, edit := range s.Edits {
		if edit.Kind != AddSandwich && edit.Kind != NoteFirstUnit ||
			edit.Kind == AddSandwich && edit.Note != "" ||
			edit.Pending && (i != len(s.Edits)-1 || s.RefreshSummary) {
			return false
		}
	}
	if s.Interleave != "" {
		if s.Interleave != TakeStaffControl || s.Inbound == nil || s.MissingPresentation ||
			len(s.Edits) != 0 || s.RefreshSummary || s.SecondInbound != nil || s.Replay || s.ProcessOrder ||
			s.Model.HTTPStatus != 0 && s.Model.HTTPStatus != 200 {
			return false
		}
	}
	return true
}

func runScenario(parent context.Context, t testing.TB, s Scenario) (Observation, error) {
	t.Helper()
	if parent == nil || s.Inbound != nil && !validText(*s.Inbound) ||
		len(s.History) > cart.PickupCompletionHistoryLimit/2 ||
		s.MissingPresentation && len(s.History) != 0 ||
		s.Model.HTTPStatus != 0 && s.Model.HTTPStatus != 200 && (s.Model.HTTPStatus < 400 || s.Model.HTTPStatus > 599) ||
		len(s.Model.ToolArguments) > cart.PickupCompletionInputByteLimit || !utf8.ValidString(s.Model.ToolArguments) {
		return Observation{}, cart.ErrInput
	}
	for _, exchange := range s.History {
		if !validText(exchange.CustomerText) || !validText(exchange.RestaurantText) {
			return Observation{}, cart.ErrInput
		}
	}
	if !validExtension(s) {
		return Observation{}, cart.ErrInput
	}
	if err := parent.Err(); err != nil {
		return Observation{}, err
	}
	if deadline, ok := parent.Deadline(); ok && !time.Now().Before(deadline) {
		return Observation{}, context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	f := newFixture(ctx, t)
	receiver := testpickupreceiver.New(t)
	var out Observation

	var x cart.PickupExecution
	out.Summary, out.Presentation, out.Deliveries, x = f.prepareSummary(receiver, s.MissingPresentation)
	out.OriginalSummary = out.Summary
	out.Deliveries = append(out.Deliveries, f.finishSummary(receiver, x, s.PostSummaryQuestion)...)
	for _, exchange := range s.History {
		turn := f.inbound(exchange.CustomerText)
		out.Deliveries = append(out.Deliveries, f.presentReply(receiver, f.finish(turn, exchange.RestaurantText)))
	}
	for _, edit := range s.Edits {
		out.Edits = append(out.Edits, f.edit(edit))
	}
	if s.RefreshSummary {
		summary, presentation, deliveries, source := f.prepareSummary(receiver, false)
		out.Summary, out.Presentation = summary, presentation
		out.Deliveries = append(out.Deliveries, deliveries...)
		f.finishSummary(receiver, source, "")
	}

	// Silence has no current event, claim, model server or bridge invocation.
	if s.Inbound == nil {
		out.Before = f.effects(nil)
		out.After = f.effects(nil)
		out.BeforeReplay = f.effects(nil)
		out.Final = f.effects(nil)
		return out, nil
	}
	x = f.inbound(*s.Inbound)
	out.Before = f.effects(&x)
	out.InboundEventID = x.Source.EventUUID()
	// Stale anchors and pending VALID native work are real domain refusals.
	// Preserve preflight error and still invoke the actual bridge independently.
	out.Context, out.ContextError = f.pickup.CompletionContext(ctx, x)
	var barrier *modelBarrier
	if s.Interleave == TakeStaffControl {
		barrier = newModelBarrier()
	}
	assessor, requests := modelServer(t, s.Model, barrier)
	var err error
	if barrier == nil {
		out.Submission, err = f.pickup.AssessAndSubmit(ctx, x, 1, assessor)
	} else {
		out.Submission, err, out.Interleaving = f.assessWithTakeover(x, assessor, barrier)
	}
	out.ModelRequests = requests()
	if ctx.Err() != nil {
		// Do not turn cancellation into a readback/setup failure or claim complete
		// effects from a canceled read. Before and wire capture remain available.
		return out, ctx.Err()
	}
	out.After = f.effects(&x)
	if err == nil && s.ProcessOrder {
		out.StaffRead, out.StaffUnits, out.StaffProcessed = f.process(out.Submission.Order)
	}
	out.BeforeReplay = f.effects(&x)
	if err == nil && s.Replay {
		out.Replay, out.ReplayError = f.pickup.AssessAndSubmit(ctx, x, 1, assessor)
	}
	out.ModelRequests = requests()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	out.Final = f.effects(&x)
	if err == nil && s.SecondInbound != nil {
		out.Second = f.secondCompletion(x, *s.SecondInbound, s.Model)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, err
}

func (f *fixture) inbound(text string) cart.PickupExecution {
	f.t.Helper()
	return f.execution(f.claimInbound(text))
}

func (f *fixture) execution(claim cart.MenuClaim) cart.PickupExecution {
	return cart.PickupExecution{Local: f.local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: f.controlVersion}
}

func (f *fixture) claimInbound(text string) cart.MenuClaim {
	f.t.Helper()
	event := foundation.Event{ID: "pickup-eval-" + newOperation(f.t).String(), Sender: "fictional-sender", Text: text}
	_, fresh, err := f.store.AcceptMenu(f.ctx, f.local, event, time.Now().UTC())
	f.check(err)
	if !fresh {
		f.t.Fatal("synthetic event was not freshly admitted")
	}
	claim, ok, err := f.store.ClaimMenu(f.ctx, f.local, time.Minute)
	f.check(err)
	if !ok || claim.Source().EventProviderID() != event.ID || claim.Job().Event != event {
		f.t.Fatal("synthetic admitted event did not yield its exact scoped claim")
	}
	return claim
}

func (f *fixture) effects(x *cart.PickupExecution) Effects {
	f.t.Helper()
	var result Effects
	var err error
	result.Cart, err = f.store.Read(f.ctx, f.local, f.snapshot.ID)
	f.check(err)
	result.Orders, err = f.pickup.ListOrders(f.ctx, staffSubject, cart.PickupPageRequest{})
	f.check(err)
	if result.Orders.NextCursor != nil {
		f.t.Fatal("fixture observation unexpectedly requires another order page")
	}
	result.Intents, err = f.pickup.ReadLocalIntents(f.ctx, f.local, cart.PickupIntentID{}, 100)
	f.check(err)
	if len(result.Intents) == 100 {
		f.t.Fatal("fixture observation may have truncated intents")
	}
	if x != nil {
		processing, err := f.jobs.Inspect(f.ctx, x.Source.EventProviderID())
		f.check(err)
		result.Processing = &processing
	}
	return result
}

func (f *fixture) process(order cart.PickupOrder) (cart.PickupOrder, cart.PickupOrderUnitPage, cart.PickupOrder) {
	f.t.Helper()
	read, err := f.pickup.ReadOrder(f.ctx, staffSubject, order.ID)
	f.check(err)
	units, err := f.pickup.ReadUnits(f.ctx, staffSubject, order.ID, order.Confirmed.ID, cart.PickupPageRequest{})
	f.check(err)
	if units.NextCursor != nil {
		f.t.Fatal("fixture observation unexpectedly requires another unit page")
	}
	for _, action := range []cart.PickupStaffAction{cart.PickupAccept, cart.PickupStartPreparing} {
		order, err = f.pickup.ApplyAction(f.ctx, staffSubject, order.ID, cart.PickupStaffActionRequest{
			Action: action, ExpectedVersion: order.Version, IdempotencyKey: "eval-" + string(action),
		})
		f.check(err)
	}
	// Return actual readback as well as public action results.
	processed, err := f.pickup.ReadOrder(f.ctx, staffSubject, order.ID)
	f.check(err)
	return read, units, processed
}
