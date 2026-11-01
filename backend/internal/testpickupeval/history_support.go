package testpickupeval

import (
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

// prepareSummary reuses public registration/preparation for both the original
// and refreshed anchor. It reads the actual queue; it never assembles summary text.
func (f *fixture) prepareSummary(receiver *testpickupreceiver.Receiver, missing bool) (cart.PreparedPickupSummary, cart.PickupPresentation, []Delivery, cart.PickupExecution) {
	f.t.Helper()
	x := f.inbound("Show the final pickup summary")
	call, err := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	f.check(err)
	x.Execution, err = f.store.RegisterExecution(f.ctx, f.local, x.Source.EventProviderID(), 1, call)
	f.check(err)
	summary, err := f.pickup.Prepare(f.ctx, x, f.snapshot.ID, f.snapshot.Version)
	f.check(err)
	queued, err := f.pickup.ReadLocalIntents(f.ctx, f.local, cart.PickupIntentID{}, 100)
	f.check(err)
	var intent cart.PickupLocalIntent
	found := false
	for _, candidate := range queued {
		if candidate.ID == summary.Intent.ID {
			if found {
				f.t.Fatal("duplicate prepared intent in public queue")
			}
			intent, found = candidate, true
		}
	}
	if !found || len(queued) == 100 || intent.Kind != "summary" || intent.CandidateID != summary.CandidateID || intent.Text != summary.Intent.Text {
		f.t.Fatal("prepared summary did not match actual complete durable queue")
	}
	if missing {
		return summary, cart.PickupPresentation{}, nil, x
	}
	delivery, digest := receive(f.ctx, f.t, receiver, intent.ID.String(), intent.Text)
	presentation, err := f.pickup.RecordLocalPresentation(f.ctx, f.local, cart.PickupLocalPresentation{
		IntentID: intent.ID, CandidateID: intent.CandidateID,
		ReceiverReference: delivery.Acknowledgment.ReceiverReference, ReceivedTextDigest: digest,
	})
	f.check(err)
	return summary, presentation, []Delivery{delivery}, x
}

func (f *fixture) presentReply(receiver *testpickupreceiver.Receiver, reply menugate.Receipt) Delivery {
	f.t.Helper()
	delivery, digest := receive(f.ctx, f.t, receiver, reply.OutboxID, reply.Content)
	f.check(f.pickup.RecordLocalReply(f.ctx, f.local, reply.OutboxID, delivery.Acknowledgment.ReceiverReference, digest))
	return delivery
}

// finishSummary uses the EXISTING summary request's one real source-bound reply.
// Receiving that reply after summary ACK creates a restaurant-only intervening
// turn at the anchor sequence; no fabricated preceding customer is admitted.
func (f *fixture) finishSummary(receiver *testpickupreceiver.Receiver, x cart.PickupExecution, question string) []Delivery {
	f.t.Helper()
	if question == "" {
		f.finish(x, "Local placeholder (not presented)")
		return nil
	}
	return []Delivery{f.presentReply(receiver, f.finish(x, question))}
}
