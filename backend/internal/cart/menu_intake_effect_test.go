package cart_test

import (
	"errors"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

// S1/S3: completed AI history without a no-reply reason must have an exact
// durable effect before a historical read can classify it as completed reply.
func TestMenuIntakeCompletedAdmissionRequiresEffect(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "effect-read-init")
	jobs := menuJobs(t, f)
	event, receipt := menuSeed(t, f, jobs, state, "completed-effect-read")
	// The route shape permits this row, but a missing corresponding effect is
	// not a legitimate completed-with-exact-reply result. Never invent one.
	if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed',attempt=1,generation=1 WHERE id=$1`, receipt.JobID); err != nil {
		t.Fatal(err)
	}
	before := foundation.Snapshot{Event: event, JobState: "completed", JobAttempt: 1, JobCount: 1}
	if got, err := jobs.Inspect(t.Context(), event.ID); err != nil || got != before {
		t.Fatalf("completed typed history readiness: %+v %v", got, err)
	}
	if source, err := f.s.BindSource(t.Context(), f.local, event.ID); err != nil || source != receipt.Source {
		t.Fatalf("completed source readiness: %+v %v", source, err)
	}
	counters := menuCounters(t, f)
	t.Log("fixture ready: authenticated AI control, bound typed completed job with attempt/generation 1 and no no-reply reason; public Inspect confirms no outbox; TARGET ReadAdmission must reject missing exact effect")
	got, err := f.s.ReadAdmission(t.Context(), f.local, event.ID)
	if !errors.Is(err, cart.ErrStoredData) || got != (cart.Admission{}) {
		t.Fatalf("completed reply admission must resolve an exact effect or fail closed: %+v error=%v want=%v", got, err, cart.ErrStoredData)
	}
	menuPublicUnchanged(t, f, jobs, event, before, counters)
}

func TestMenuIntakeCompletedEffectHistoryPreserved(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "positive-effect-init")
	jobs := menuJobs(t, f)
	event, want := menuSeed(t, f, jobs, state, "positive-completed-effect")
	want.State = "completed"
	want.OutboxID = "b0000000-0000-4000-8000-000000000001"
	want.Reply = "  exact durable reply\né\n "
	c := sourceConn(t, f)
	if _, err := c.Exec(t.Context(), `UPDATE app.jobs SET state='completed',attempt=1,generation=1 WHERE id=$1`, want.JobID); err != nil {
		t.Fatal(err)
	}
	// D's menu queue producer is not available yet. Seed its typed durable
	// effect explicitly, preserving the actual source/admission/claim tuple.
	if _, err := c.Exec(t.Context(), `INSERT INTO app.outbox(id,event_id,content,state,due_at,kind,restaurant_id,conversation_id,control_version,creation_generation,creation_attempt) VALUES($1,$2,$3,'pending',clock_timestamp(),'ai_reply',$4,$5,$6,1,1)`, want.OutboxID, want.Source.EventUUID(), want.Reply, restaurant, f.local.ConversationID().String(), want.Version); err != nil {
		t.Fatal(err)
	}
	before := foundation.Snapshot{Event: event, JobState: "completed", JobAttempt: 1, JobCount: 1, OutboxState: "pending", OutboxCount: 1}
	if got, err := jobs.Inspect(t.Context(), event.ID); err != nil || got != before {
		t.Fatalf("valid completed-effect readiness: %+v %v", got, err)
	}
	for i, mode := range []conversation.Mode{conversation.ModeStaff, conversation.ModeAI} {
		var err error
		state, err = control.SetMode(t.Context(), token, state.ConversationID, mode, state.Version, "effect-"+string(mode))
		if err != nil || state.Mode != mode || state.Version != int64(i+1) {
			t.Fatalf("authenticated current-control readiness: %+v %v", state, err)
		}
		counters := menuCounters(t, f)
		if got, err := f.s.ReadAdmission(t.Context(), f.local, event.ID); err != nil || got != want {
			t.Fatalf("historical exact-effect receipt: %+v %v want %+v", got, err, want)
		}
		if got, created, err := f.s.AcceptMenu(t.Context(), f.local, event, time.Now()); err != nil || created || got != want {
			t.Fatalf("completed exact-effect replay: %+v created=%v error=%v", got, created, err)
		}
		menuPublicUnchanged(t, f, jobs, event, before, counters)
	}
}
