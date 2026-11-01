package testpickupeval

import (
	"context"
	"sync"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/pickupintent"
)

// modelBarrier is fixture-owned, not a caller callback or provider decision.
// The HTTP handler signals only after decoding/capturing the actual request.
type modelBarrier struct {
	arrived     chan struct{}
	release     chan struct{}
	arrivalOnce sync.Once
	releaseOnce sync.Once
}

func newModelBarrier() *modelBarrier {
	return &modelBarrier{arrived: make(chan struct{}), release: make(chan struct{})}
}

func (b *modelBarrier) unblock() { b.releaseOnce.Do(func() { close(b.release) }) }

func (b *modelBarrier) pause(ctx context.Context) bool {
	b.arrivalOnce.Do(func() { close(b.arrived) })
	select {
	case <-b.release:
		return true
	case <-ctx.Done():
		return false
	}
}

func (f *fixture) assessWithTakeover(x cart.PickupExecution, assessor pickupintent.Assessor, barrier *modelBarrier) (cart.PickupSubmission, error, *InterleaveObservation) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	type result struct {
		submission cart.PickupSubmission
		err        error
	}
	done := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		submission, err := f.pickup.AssessAndSubmit(ctx, x, 1, assessor)
		done <- result{submission, err}
	}()
	// This defer also runs if a setup/readback Fatal calls Goexit. Release the
	// owned handler and cancel before joining; never close servers over live work.
	defer func() {
		barrier.unblock()
		cancel()
		timer := time.NewTimer(16 * time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			f.t.Error("owned assessment goroutine did not stop within concrete client/domain bounds")
		}
	}()
	select {
	case <-barrier.arrived:
	case early := <-done:
		f.t.Fatalf("assessment ended before actual HTTP barrier: %v", early.err)
	case <-ctx.Done():
		return cart.PickupSubmission{}, ctx.Err(), nil
	}
	observation := &InterleaveObservation{BeforeMutation: f.effects(&x)}
	var err error
	observation.ControlBefore, err = f.control.Read(ctx, f.token, f.local.ConversationID())
	f.check(err)
	observation.ControlAfter, err = f.control.SetMode(ctx, f.token, f.local.ConversationID(), conversation.ModeStaff, observation.ControlBefore.Version, "eval-http-takeover")
	f.check(err)
	observation.AfterMutation = f.effects(&x)
	barrier.unblock()
	select {
	case completed := <-done:
		return completed.submission, completed.err, observation
	case <-ctx.Done():
		return cart.PickupSubmission{}, ctx.Err(), observation
	}
}

func (f *fixture) secondCompletion(first cart.PickupExecution, text string, script ModelResponse) *CompletionAttempt {
	f.t.Helper()
	// Retire the actual first source through the gate before admitting another;
	// ClaimMenu cannot manufacture two authorized distinct-source live claims.
	f.finish(first, "Local first completion handled (not presented)")
	retired, err := f.jobs.Inspect(f.ctx, first.Source.EventProviderID())
	f.check(err)
	x := f.inbound(text)
	out := &CompletionAttempt{RetiredFirstProcessing: retired, InboundEventID: x.Source.EventUUID(), Before: f.effects(&x)}
	out.Context, out.ContextError = f.pickup.CompletionContext(f.ctx, x)
	assessor, requests := modelServer(f.t, script, nil)
	out.Submission, out.Error = f.pickup.AssessAndSubmit(f.ctx, x, 1, assessor)
	out.ModelRequests = requests()
	if f.ctx.Err() == nil {
		out.After = f.effects(&x)
	}
	return out
}
