package postgres

import (
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
)

func TestConversationOrderingAndBlockedDeadlines(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	accept(t, s, "first", now)
	// Arrival order must not depend on service clock monotonicity or UUID order.
	accept(t, s, "second", now.Add(-time.Hour))
	first, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok || first.Event.ID != "first" {
		t.Fatalf("first = %+v, %v, %v", first, ok, err)
	}
	if _, ok, err := s.ClaimJob(t.Context(), now, time.Minute); err != nil || ok {
		t.Fatalf("same conversation overlapped = %v, %v", ok, err)
	}
	other := foundation.Event{ID: "other", Sender: "other-customer", Text: "hello"}
	if created, err := s.Accept(t.Context(), other, now); err != nil || !created {
		t.Fatalf("other accept = %v, %v", created, err)
	}
	parallel, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok || parallel.Event.ID != "other" {
		t.Fatalf("independent conversation = %+v, %v, %v", parallel, ok, err)
	}
	if due, ok, err := s.NextDue(t.Context()); err != nil || !ok || !due.Equal(now.Add(time.Minute)) {
		t.Fatalf("blocked job causes hot deadline: %v, %v, %v", due, ok, err)
	}
	if err := s.CompleteJob(t.Context(), first, "first response", now); err != nil {
		t.Fatal(err)
	}
	second, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok || second.Event.ID != "second" {
		t.Fatalf("second = %+v, %v, %v", second, ok, err)
	}
	if err := s.CompleteJob(t.Context(), second, "second response", now); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob(t.Context(), parallel, now); err != nil {
		t.Fatal(err)
	}
	send, ok, err := s.ClaimDelivery(t.Context(), now, time.Minute)
	if err != nil || !ok || send.EventID != "first" {
		t.Fatalf("ordered send = %+v, %v, %v", send, ok, err)
	}
	if _, ok, err := s.ClaimDelivery(t.Context(), now, time.Minute); err != nil || ok {
		t.Fatalf("same conversation sends overlapped = %v, %v", ok, err)
	}
	if due, ok, err := s.NextDue(t.Context()); err != nil || !ok || !due.Equal(now.Add(time.Minute)) {
		t.Fatalf("blocked send causes hot deadline: %v, %v, %v", due, ok, err)
	}
	if err := s.FinishDelivery(t.Context(), send, foundation.SendResult{State: foundation.SendUnknown}, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimDelivery(t.Context(), now, time.Minute); err != nil || ok {
		t.Fatalf("unknown predecessor was bypassed = %v, %v", ok, err)
	}
	if due, ok, err := s.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("unknown predecessor causes hot deadline: %v, %v, %v", due, ok, err)
	}
}

func TestAcceptanceUnblocksNextConversationSend(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, id := range []string{"first", "second"} {
		accept(t, s, id, now.Add(time.Duration(i)*time.Microsecond))
	}
	now = now.Add(time.Second)
	for range 2 {
		job, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim = %v, %v", ok, err)
		}
		if err := s.CompleteJob(t.Context(), job, "response", now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"first", "second"} {
		delivery, ok, err := s.ClaimDelivery(t.Context(), now, time.Minute)
		if err != nil || !ok || delivery.EventID != id {
			t.Fatalf("ordered delivery = %+v, %v, %v", delivery, ok, err)
		}
		if err := s.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "fake-" + id}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.NextDue(t.Context()); err != nil || ok {
		t.Fatalf("completed conversations = %v, %v", ok, err)
	}
}
