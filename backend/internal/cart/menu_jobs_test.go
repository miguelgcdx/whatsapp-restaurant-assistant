package cart_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

// S1/S3: public authenticated admission must precede the first native claim.
// A claim preserves the exact source/event/job, allocating no reply or identity.
func TestMenuClaimFreshAI(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "claim-init")
	jobs := menuJobs(t, f)
	event := foundation.Event{ID: "menu-claim-fresh", Sender: "fictional-sender", Text: "  Show the menu\né  "}
	admission, created, err := f.s.AcceptMenu(ctx, f.local, event, time.Now().UTC())
	if err != nil || !created {
		t.Fatalf("actual public admission readiness: %+v created=%v error=%v", admission, created, err)
	}
	source := admission.Source
	if source.RestaurantID() != restaurant || source.ConversationID() != f.local.ConversationID() || source.EventProviderID() != event.ID || source.EventUUID() == "" || source.Business() != "fictional-business" || source.Sender() != event.Sender || admission.JobID == "" || admission.Mode != "ai" || admission.Version != 0 || admission.State != "queued" || admission.NoReplyReason != "" || admission.OutboxID != "" || admission.Reply != "" {
		t.Fatalf("exact admitted AI0/source/job readiness: %+v source=%+v", admission, source)
	}
	before := foundation.Snapshot{Event: event, JobState: "queued", JobCount: 1}
	if got, err := jobs.Inspect(ctx, event.ID); err != nil || got != before {
		t.Fatalf("public queued attempt0/no-effect readiness: %+v %v", got, err)
	}
	if got, err := f.s.ReadAdmission(ctx, f.local, event.ID); err != nil || got != admission {
		t.Fatalf("locked persisted admission readiness: %+v %v", got, err)
	}
	if got, err := f.s.BindSource(ctx, f.local, event.ID); err != nil || got != source {
		t.Fatalf("public bound source readiness: %+v %v", got, err)
	}
	if got, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || got != state {
		t.Fatalf("authenticated current membership/control readiness: %+v %v", got, err)
	}
	c := sourceConn(t, f)
	var generation int64
	if err := c.QueryRow(ctx, `SELECT generation FROM app.jobs WHERE id=$1`, admission.JobID).Scan(&generation); err != nil || generation != 0 {
		t.Fatalf("unattempted generation readiness: %d %v", generation, err)
	}
	counters := menuCounters(t, f)
	if got := sourceCounts(t, c); got != "[1, 0, 0, 0, 1, 0, 0]" {
		t.Fatalf("single bound queued job/no-effect readiness: %s", got)
	}
	t.Log("READY: actual AcceptMenu committed bound queued AI0, exact public event/job/source, attempt0/generation0, no effects, authenticated current membership/control; TARGET ClaimMenu")
	claim, ok, err := f.s.ClaimMenu(ctx, f.local, time.Minute)
	if err != nil || !ok {
		t.Fatalf("fresh native menu claim must lease admitted AI work: claimed=%v claim=%+v error=%v", ok, claim, err)
	}
	wantJob := foundation.Job{ID: admission.JobID, Event: event, Generation: 1, Attempt: 1}
	if claim.Source() != source || claim.Job() != wantJob {
		t.Fatalf("claim exact persisted identity: source=%+v job=%+v want=%+v", claim.Source(), claim.Job(), wantJob)
	}
	before.JobState, before.JobAttempt = "leased", 1
	if got, err := jobs.Inspect(ctx, event.ID); err != nil || got != before {
		t.Fatalf("public leased attempt1/zero effects: %+v %v", got, err)
	}
	admission.State = "leased"
	if got, err := f.s.ReadAdmission(ctx, f.local, event.ID); err != nil || got != admission {
		t.Fatalf("public exact leased admission: %+v %v", got, err)
	}
	if got, err := f.s.BindSource(ctx, f.local, event.ID); err != nil || got != source {
		t.Fatalf("claim changed source identity: %+v %v", got, err)
	}
	if got := menuCounters(t, f); got != counters {
		t.Fatalf("claim allocated event/sequence/source/effects: %s want %s", got, counters)
	}
	var deadline pgtype.Timestamptz
	var live bool
	if err := c.QueryRow(ctx, `SELECT generation,lease_until,lease_until>clock_timestamp() FROM app.jobs WHERE id=$1 AND route='menu_message' AND event_id=$2 AND restaurant_id=$3 AND conversation_id=$4 AND admission_mode='ai' AND admission_version=0`, admission.JobID, source.EventUUID(), restaurant, f.local.ConversationID().String()).Scan(&generation, &deadline, &live); err != nil || generation != 1 || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite || !live {
		t.Fatalf("supplemental finite live lease/provenance: generation=%d deadline=%+v live=%v error=%v", generation, deadline, live, err)
	}
	if got, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || got != state {
		t.Fatalf("claim changed control: %+v %v", got, err)
	}
	if claim == (cart.MenuClaim{}) {
		t.Fatal("successful issuance returned zero claim")
	}
}
