package cart_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

// S1/S2/S3: real concurrent admissions converge to one immutable epoch and
// identity; subsequent conflicting attempts cannot leave partial allocations.
func TestMenuIntakeIndependentConcurrentDuplicate(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "independent-init")
	jobs := menuJobs(t, f)
	event := foundation.Event{ID: "concurrent-menu", Sender: "fictional-sender", Text: "  menu\né 🥪  "}
	if got, err := jobs.Inspect(ctx, event.ID); err != foundation.ErrNotFound || got != (foundation.Snapshot{}) {
		t.Fatalf("fresh readiness: %+v %v", got, err)
	}
	type outcome struct {
		receipt cart.Admission
		created bool
		err     error
	}
	const n = 6
	ready := make(chan struct{}, n)
	start := make(chan struct{})
	done := make(chan outcome, n)
	for range n {
		go func() {
			ready <- struct{}{}
			<-start
			got, created, err := f.s.AcceptMenu(ctx, f.local, event, time.Now().UTC())
			done <- outcome{got, created, err}
		}()
	}
	for range n {
		<-ready
	}
	close(start)
	var want cart.Admission
	wins := 0
	for i := range n {
		got := <-done
		if got.err != nil {
			t.Fatalf("concurrent admission %d: %+v", i, got)
		}
		if got.created {
			wins++
		}
		if i == 0 {
			want = got.receipt
		}
		if got.receipt != want {
			t.Fatalf("divergent identity/epoch: %+v want %+v", got.receipt, want)
		}
	}
	if wins != 1 || want.Mode != "ai" || want.Version != 0 || want.State != "queued" || want.NoReplyReason != "" || want.JobID == "" || want.OutboxID != "" || want.Reply != "" {
		t.Fatalf("single atomic admission: wins=%d receipt=%+v", wins, want)
	}
	before := foundation.Snapshot{Event: event, JobState: "queued", JobCount: 1}
	if got, err := jobs.Inspect(ctx, event.ID); err != nil || got != before {
		t.Fatalf("public exact effect: %+v %v", got, err)
	}
	if source, err := f.s.BindSource(ctx, f.local, event.ID); err != nil || source != want.Source {
		t.Fatalf("public source: %+v %v", source, err)
	}
	c := sourceConn(t, f)
	var sequence, next int64
	if err := c.QueryRow(ctx, `SELECT e.sequence,q.next_sequence FROM app.inbound_events e JOIN app.conversation_sequences q ON q.sender=e.sender WHERE e.provider_event_id=$1`, event.ID).Scan(&sequence, &next); err != nil || sequence != 1 || next != 1 {
		t.Fatalf("duplicate reserved sequence: %d %d %v", sequence, next, err)
	}
	// Canonical full-row fingerprints supplement (never replace) public oracles.
	fingerprint := func() [32]byte {
		t.Helper()
		var all string
		for _, table := range []string{"inbound_events", "conversation_sequences", "jobs", "outbox", "send_attempts", "cart_execution_sources", "cart_execution_calls", "cart_operations", "carts", "cart_conversations", "conversation_controls", "conversation_control_operations", "restaurants", "restaurant_settings", "staff_memberships"} {
			var rows string
			if err := c.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS v FROM app.`+table+` t) s`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			all += table + rows
		}
		return sha256.Sum256([]byte(all))
	}
	counters := menuCounters(t, f)
	hash := fingerprint()
	changed := event
	changed.Text += " changed"
	if got, created, err := f.s.AcceptMenu(ctx, f.local, changed, time.Now()); err != cart.ErrConflict || err.Error() != "cart operation conflicts with committed request" || created || got != (cart.Admission{}) {
		t.Fatalf("exact conflict: %+v %v %v", got, created, err)
	}
	menuPublicUnchanged(t, f, jobs, event, before, counters)
	if fingerprint() != hash {
		t.Fatal("conflict changed stored rows")
	}
	if created, err := jobs.Accept(ctx, event, time.Now()); created || err != foundation.ErrConflict || err.Error() != "event identity already has different content" {
		t.Fatalf("legacy route conflict: %v %v", created, err)
	}
	menuPublicUnchanged(t, f, jobs, event, before, counters)
	if fingerprint() != hash {
		t.Fatal("legacy conflict changed stored rows")
	}
	for i, mode := range []conversation.Mode{conversation.ModeStaff, conversation.ModeAI} {
		var err error
		state, err = control.SetMode(ctx, token, state.ConversationID, mode, state.Version, "independent-"+string(mode))
		if err != nil || state.Version != int64(i+1) || state.Mode != mode {
			t.Fatalf("authenticated transition: %+v %v", state, err)
		}
		if got, created, err := f.s.AcceptMenu(ctx, f.local, event, time.Now()); err != nil || created || got != want {
			t.Fatalf("historical replay after transition: %+v %v %v", got, created, err)
		}
	}
	reopened, err := cart.OpenStore(ctx, f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	local, err := reopened.LoadLocal(ctx, f.local.ConversationID(), "fictional-business", event.Sender)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.ReadAdmission(ctx, local, event.ID); err != nil || got != want {
		t.Fatalf("reopened historical receipt: %+v %v", got, err)
	}
	menuNextAllocation(t, f, jobs)
	t.Log("six concurrent native admissions: one commit, identical receipts, sequence 1; exact conflicts preserve row hashes/public effects; authenticated staff1/AI2 and reopen retain AI0")
}
