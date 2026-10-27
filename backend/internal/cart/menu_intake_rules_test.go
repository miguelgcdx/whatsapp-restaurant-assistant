package cart_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

func menuJobs(t *testing.T, f fixture) *postgres.Store {
	t.Helper()
	jobs, err := postgres.Open(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jobs.Close)
	return jobs
}

func menuReady(t *testing.T, f fixture, control *conversation.Store, token string, mode conversation.Mode, key string) conversation.State {
	t.Helper()
	want := conversation.State{ConversationID: f.local.ConversationID(), Mode: mode}
	if got, err := control.Initialize(t.Context(), token, want.ConversationID, mode, key); err != nil || got != want {
		t.Fatalf("authenticated initialize readiness: %+v %v", got, err)
	}
	if got, err := control.Read(t.Context(), token, want.ConversationID); err != nil || got != want {
		t.Fatalf("authenticated current control readiness: %+v %v", got, err)
	}
	if got, err := f.s.LoadLocal(t.Context(), want.ConversationID, "fictional-business", "fictional-sender"); err != nil || got != f.local {
		t.Fatalf("owned scope readiness: %+v %v", got, err)
	}
	return want
}

// Only fixture metadata is changed by SQL. Event/digest/sequence/source identity
// comes from public legacy Accept/BindSource; permission always comes from the
// real authenticated Initialize/Read path, never a SQL-inserted control row.
func menuSeed(t *testing.T, f fixture, jobs *postgres.Store, state conversation.State, id string) (foundation.Event, cart.Admission) {
	t.Helper()
	ctx := t.Context()
	event := foundation.Event{ID: id, Sender: "fictional-sender", Text: "original exact menu text"}
	if created, err := jobs.Accept(ctx, event, time.Now().UTC()); err != nil || !created {
		t.Fatalf("seed legacy allocation: %v %v", created, err)
	}
	source, err := f.s.BindSource(ctx, f.local, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := cart.Admission{Source: source, Mode: string(state.Mode), Version: state.Version, State: "queued"}
	if state.Mode == conversation.ModeStaff {
		want.State, want.NoReplyReason = "completed", "staff_at_admission"
	}
	c := sourceConn(t, f)
	if err := c.QueryRow(ctx, `UPDATE app.jobs SET route='menu_message',restaurant_id=$1,conversation_id=$2,admission_mode=$3,admission_version=$4,state=$5,no_reply_reason=NULLIF($6,'') WHERE event_id=$7 RETURNING id::text`, restaurant, f.local.ConversationID().String(), want.Mode, want.Version, want.State, want.NoReplyReason, source.EventUUID()).Scan(&want.JobID); err != nil {
		t.Fatal(err)
	}
	if got, err := jobs.Inspect(ctx, event.ID); err != nil || got != (foundation.Snapshot{Event: event, JobState: want.State, JobCount: 1}) {
		t.Fatalf("seed public persisted readiness: %+v %v", got, err)
	}
	if got, err := f.s.BindSource(ctx, f.local, event.ID); err != nil || got != source {
		t.Fatalf("seed public source readiness: %+v %v", got, err)
	}
	var mode, reason string
	var version, attempt, generation int64
	if err := c.QueryRow(ctx, `SELECT admission_mode,admission_version,COALESCE(no_reply_reason,''),attempt,generation FROM app.jobs WHERE id=$1`, want.JobID).Scan(&mode, &version, &reason, &attempt, &generation); err != nil || mode != want.Mode || version != want.Version || reason != want.NoReplyReason || attempt != 0 || generation != 0 {
		t.Fatalf("seed typed integrity: %s %d %s %d %d %v", mode, version, reason, attempt, generation, err)
	}
	return event, want
}

func menuCounters(t *testing.T, f fixture) string {
	t.Helper()
	var counters string
	if err := sourceConn(t, f).QueryRow(t.Context(), `SELECT json_build_array((SELECT count(*) FROM app.inbound_events),(SELECT count(*) FROM app.conversation_controls),(SELECT count(*) FROM app.conversation_control_operations),(SELECT COALESCE(json_agg(q ORDER BY sender)::text,'[]') FROM app.conversation_sequences q))::text`).Scan(&counters); err != nil {
		t.Fatal(err)
	}
	return sourceCounts(t, sourceConn(t, f)) + counters
}

func menuPublicUnchanged(t *testing.T, f fixture, jobs *postgres.Store, event foundation.Event, before foundation.Snapshot, counters string) {
	t.Helper()
	got, err := jobs.Inspect(t.Context(), event.ID)
	if before == (foundation.Snapshot{}) {
		if !errors.Is(err, foundation.ErrNotFound) || got != before {
			t.Fatalf("rejection persisted event: %+v %v", got, err)
		}
	} else if err != nil || got != before {
		t.Fatalf("changed public snapshot: %+v %v want %+v", got, err, before)
	}
	if after := menuCounters(t, f); after != counters {
		t.Fatalf("changed stored counters: %s want %s", after, counters)
	}
}

// A successful public next allocation witnesses that rejected/replayed input
// did not reserve an extra sequence. SQL supplements the public exact effect.
func menuNextAllocation(t *testing.T, f fixture, jobs *postgres.Store) {
	t.Helper()
	c := sourceConn(t, f)
	var previous int64
	if err := c.QueryRow(t.Context(), `SELECT COALESCE((SELECT next_sequence FROM app.conversation_sequences WHERE sender='fictional-sender'),0)`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	event := foundation.Event{ID: t.Name() + "-next", Sender: "fictional-sender", Text: "next allocation witness"}
	if created, err := jobs.Accept(t.Context(), event, time.Now()); err != nil || !created {
		t.Fatalf("public next allocation: created=%v error=%v", created, err)
	}
	if got, err := jobs.Inspect(t.Context(), event.ID); err != nil || got != (foundation.Snapshot{Event: event, JobState: "queued", JobCount: 1}) {
		t.Fatalf("next allocation public effect: %+v %v", got, err)
	}
	var sequence, next int64
	if err := c.QueryRow(t.Context(), `SELECT e.sequence,q.next_sequence FROM app.inbound_events e JOIN app.conversation_sequences q ON q.sender=e.sender WHERE e.provider_event_id=$1`, event.ID).Scan(&sequence, &next); err != nil || sequence != previous+1 || next != sequence {
		t.Fatalf("next sequence witness: previous=%d sequence=%d next=%d error=%v", previous, sequence, next, err)
	}
}

func TestMenuIntakeLegacyAllocationPreserved(t *testing.T) {
	f := setup(t)
	jobs := menuJobs(t, f)
	event := foundation.Event{ID: "legacy-preserved", Sender: "fictional-sender", Text: " exact legacy text "}
	if created, err := jobs.Accept(t.Context(), event, time.Now()); err != nil || !created {
		t.Fatal(created, err)
	}
	before, err := jobs.Inspect(t.Context(), event.ID)
	if err != nil || before != (foundation.Snapshot{Event: event, JobState: "queued", JobCount: 1}) {
		t.Fatalf("legacy allocation: %+v %v", before, err)
	}
	counters := menuCounters(t, f)
	if created, err := jobs.Accept(t.Context(), event, time.Now()); err != nil || created {
		t.Fatalf("legacy exact replay: %v %v", created, err)
	}
	changed := event
	changed.Text += " changed"
	if created, err := jobs.Accept(t.Context(), changed, time.Now()); !errors.Is(err, foundation.ErrConflict) || created {
		t.Fatalf("legacy conflicting digest: %v %v", created, err)
	}
	menuPublicUnchanged(t, f, jobs, event, before, counters)
	menuNextAllocation(t, f, jobs)
}

func TestMenuIntakeStaffAdmission(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeStaff, "staff-init")
	jobs := menuJobs(t, f)
	event := foundation.Event{ID: "fresh-staff", Sender: "fictional-sender", Text: "menu during takeover"}
	menuPublicUnchanged(t, f, jobs, event, foundation.Snapshot{}, menuCounters(t, f))
	t.Log("fixture ready: owned catalog/local, authenticated staff version 0, absent event; TARGET AcceptMenu must persist completed/staff_at_admission with no outbox")
	got, created, err := f.s.AcceptMenu(t.Context(), f.local, event, time.Now().UTC())
	if err != nil || !created || got.Mode != "staff" || got.Version != state.Version || got.State != "completed" || got.NoReplyReason != "staff_at_admission" || got.JobID == "" {
		t.Fatalf("fresh staff admission must commit terminal no-reply receipt: %+v created=%v error=%v", got, created, err)
	}
	if snapshot, err := jobs.Inspect(t.Context(), event.ID); err != nil || snapshot != (foundation.Snapshot{Event: event, JobState: "completed", JobCount: 1}) {
		t.Fatalf("staff exact public effects: %+v %v", snapshot, err)
	}
	if source, err := f.s.BindSource(t.Context(), f.local, event.ID); err != nil || source != got.Source || source.RestaurantID() != restaurant || source.ConversationID() != state.ConversationID || source.Sender() != event.Sender || source.Business() != "fictional-business" || source.EventProviderID() != event.ID || source.EventUUID() == "" {
		t.Fatalf("staff source identity: %+v %v", source, err)
	}
	var generation int64
	if err := sourceConn(t, f).QueryRow(t.Context(), `SELECT generation FROM app.jobs WHERE id=$1`, got.JobID).Scan(&generation); err != nil || generation != 0 || sourceCounts(t, sourceConn(t, f)) != "[1, 0, 0, 0, 1, 0, 0]" {
		t.Fatalf("staff allocation integrity: generation=%d error=%v", generation, err)
	}
}

func TestMenuIntakeOriginalEpochReplay(t *testing.T) {
	for _, initial := range []conversation.Mode{conversation.ModeAI, conversation.ModeStaff} {
		t.Run(string(initial), func(t *testing.T) {
			f := setup(t)
			control, token := menuControl(t, f)
			state := menuReady(t, f, control, token, initial, "history-init")
			jobs := menuJobs(t, f)
			event, want := menuSeed(t, f, jobs, state, "history-"+string(initial))
			for i, mode := range []conversation.Mode{initial, conversation.ModeStaff, conversation.ModeAI, conversation.ModeAI} {
				if i > 0 {
					var err error
					state, err = control.SetMode(t.Context(), token, state.ConversationID, mode, state.Version, fmt.Sprintf("history-%d", i))
					if err != nil || state.Mode != mode || state.Version != int64(i) {
						t.Fatalf("authenticated transition readiness: %+v %v", state, err)
					}
				}
				t.Run(fmt.Sprintf("epoch-%d-%s", i, mode), func(t *testing.T) {
					reopened, err := cart.OpenStore(t.Context(), f.dsn, restaurant)
					if err != nil {
						t.Fatal(err)
					}
					defer reopened.Close()
					local, err := reopened.LoadLocal(t.Context(), f.local.ConversationID(), "fictional-business", event.Sender)
					if err != nil || local != f.local {
						t.Fatalf("reopen readiness: %+v %v", local, err)
					}
					if current, err := control.Read(t.Context(), token, local.ConversationID()); err != nil || current != state {
						t.Fatalf("current authenticated readiness: %+v %v", current, err)
					}
					before, err := jobs.Inspect(t.Context(), event.ID)
					if err != nil || before != (foundation.Snapshot{Event: event, JobState: want.State, JobCount: 1}) {
						t.Fatalf("history readiness: %+v %v", before, err)
					}
					counters := menuCounters(t, f)
					t.Logf("fixture ready: seeded %s version 0 identity=%s, reopened ownership, authenticated current %s version %d; TARGET duplicate AcceptMenu must retain original eligibility/epoch", initial, want.JobID, state.Mode, state.Version)
					got, created, err := reopened.AcceptMenu(t.Context(), local, event, time.Now().UTC())
					if err != nil || created || got != want {
						t.Fatalf("duplicate must return original admission without new commit: %+v created=%v error=%v want=%+v", got, created, err, want)
					}
					menuPublicUnchanged(t, f, jobs, event, before, counters)
					if source, err := reopened.BindSource(t.Context(), local, event.ID); err != nil || source != want.Source {
						t.Fatalf("replay changed source: %+v %v", source, err)
					}
					menuNextAllocation(t, f, jobs)
				})
			}
		})
	}
}

func TestMenuIntakeIdentityConflicts(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "conflict-init")
	jobs := menuJobs(t, f)
	event, receipt := menuSeed(t, f, jobs, state, "menu-conflict")
	legacy := foundation.Event{ID: "legacy-conflict", Sender: event.Sender, Text: event.Text}
	if created, err := jobs.Accept(t.Context(), legacy, time.Now()); err != nil || !created {
		t.Fatal(created, err)
	}
	foreign := f
	var err error
	foreign.local, err = f.s.ProvisionLocal(t.Context(), "fictional-business", event.Sender)
	if err != nil {
		t.Fatal(err)
	}
	menuReady(t, foreign, control, token, conversation.ModeAI, "foreign-init")
	for _, name := range []string{"legacy-route", "foreign-scope", "digest"} {
		t.Run(name, func(t *testing.T) {
			input, local := event, f.local
			if name == "legacy-route" {
				input = legacy
			} else if name == "foreign-scope" {
				local = foreign.local
			} else {
				input.Text += " changed"
			}
			before, err := jobs.Inspect(t.Context(), input.ID)
			if err != nil || before.Event.ID != input.ID || before.JobCount != 1 || before.OutboxCount != 0 {
				t.Fatalf("conflict public readiness: %+v %v", before, err)
			}
			counters := menuCounters(t, f)
			t.Logf("fixture ready: persisted identity/source, authenticated owned controls; TARGET AcceptMenu %s must return cart.ErrConflict and zero effects", name)
			got, created, err := f.s.AcceptMenu(t.Context(), local, input, time.Now())
			if !errors.Is(err, cart.ErrConflict) || created || got != (cart.Admission{}) {
				t.Fatalf("identity conflict must reject without admission: %+v created=%v error=%v want=%v", got, created, err, cart.ErrConflict)
			}
			menuPublicUnchanged(t, f, jobs, before.Event, before, counters)
			if source, err := f.s.BindSource(t.Context(), f.local, event.ID); err != nil || source != receipt.Source {
				t.Fatalf("conflict changed source: %+v %v", source, err)
			}
			menuNextAllocation(t, f, jobs)
		})
	}
	// Already implemented in Unit A: preservation only, never a fabricated RED.
	t.Run("preserve-legacy-rejects-menu", func(t *testing.T) {
		before, err := jobs.Inspect(t.Context(), event.ID)
		if err != nil {
			t.Fatal(err)
		}
		counters := menuCounters(t, f)
		if created, err := jobs.Accept(t.Context(), event, time.Now()); created || !errors.Is(err, foundation.ErrConflict) {
			t.Fatalf("legacy route fence: %v %v", created, err)
		}
		menuPublicUnchanged(t, f, jobs, event, before, counters)
	})
}

func TestMenuIntakeFailClosed(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	menuReady(t, f, control, token, conversation.ModeAI, "reject-init")
	jobs := menuJobs(t, f)
	missing, err := f.s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := control.Read(t.Context(), token, missing.ConversationID()); !errors.Is(err, conversation.ErrNotFound) || got != (conversation.State{}) {
		t.Fatalf("missing control readiness: %+v %v", got, err)
	}
	for _, name := range []string{"missing-control", "zero-local", "nil-context", "nil-store", "zero-store", "stale-local", "sender", "empty-id", "id-over-bound", "empty-text", "text-over-bound", "cancel", "deadline"} {
		t.Run(name, func(t *testing.T) {
			ctx, local, store := t.Context(), f.local, f.s
			event := foundation.Event{ID: "reject-" + name, Sender: "fictional-sender", Text: "menu"}
			want := cart.ErrInput
			switch name {
			case "missing-control":
				local, want = missing, cart.ErrNotFound
			case "zero-local":
				local = cart.LocalContext{}
			case "nil-context":
				ctx = nil
			case "nil-store":
				store = nil
			case "zero-store":
				store = &cart.Store{}
			case "stale-local":
				c := sourceConn(t, f)
				if _, err := c.Exec(t.Context(), `UPDATE app.cart_conversations SET business='changed' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, err := c.Exec(context.Background(), `UPDATE app.cart_conversations SET business='fictional-business' WHERE id=$1`, f.local.ConversationID().String())
					if err != nil {
						t.Error(err)
					}
				})
				if _, err := f.s.LoadLocal(t.Context(), f.local.ConversationID(), "fictional-business", "fictional-sender"); !errors.Is(err, cart.ErrNotFound) {
					t.Fatalf("stale scope readiness: %v", err)
				}
				want = cart.ErrNotFound
			case "sender":
				event.Sender, want = "different-sender", cart.ErrNotFound
			case "empty-id":
				event.ID = ""
			case "id-over-bound":
				event.ID = strings.Repeat("x", 129)
			case "empty-text":
				event.Text = " "
			case "text-over-bound":
				event.Text = strings.Repeat("x", 4001)
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = cancelled, context.Canceled
			case "deadline":
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx, want = expired, context.DeadlineExceeded
			}
			counters := menuCounters(t, f)
			menuPublicUnchanged(t, f, jobs, event, foundation.Snapshot{}, counters)
			t.Logf("fixture ready: owned catalog/membership and explicit control or verified absence; absent event; TARGET AcceptMenu %s must return %v with no allocation/autoseed", name, want)
			got, created, err := store.AcceptMenu(ctx, local, event, time.Now())
			if !errors.Is(err, want) || created || got != (cart.Admission{}) {
				t.Fatalf("fail-closed admission must return domain error: %+v created=%v error=%v want=%v", got, created, err, want)
			}
			menuPublicUnchanged(t, f, jobs, event, foundation.Snapshot{}, counters)
			if _, err := control.Read(t.Context(), token, missing.ConversationID()); !errors.Is(err, conversation.ErrNotFound) {
				t.Fatalf("rejection bootstrapped permission: %v", err)
			}
			menuNextAllocation(t, f, jobs)
		})
	}
}

func TestMenuIntakeLockedReadAdmission(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "read-init")
	jobs := menuJobs(t, f)
	event, want := menuSeed(t, f, jobs, state, "locked-read")
	counters := menuCounters(t, f)
	before, err := jobs.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := sourceConn(t, f).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `UPDATE app.jobs SET state='completed',no_reply_reason='control_changed' WHERE id=$1`, want.JobID); err != nil {
		t.Fatal(err)
	}
	menuPublicUnchanged(t, f, jobs, event, before, counters)
	t.Log("fixture ready: valid bound typed AI admission, authenticated control, public queued snapshot; owned job transaction holds uncommitted terminal change; TARGET ReadAdmission must wait then observe commit")
	type outcome struct {
		receipt cart.Admission
		err     error
	}
	done := make(chan outcome, 1)
	go func() { got, err := f.s.ReadAdmission(t.Context(), f.local, event.ID); done <- outcome{got, err} }()
	select {
	case got := <-done:
		t.Fatalf("locked ReadAdmission must resolve prior transaction before receipt: premature=%+v error=%v", got.receipt, got.err)
	case <-time.After(100 * time.Millisecond):
		// The exact receipt check below must observe the committed change, not
		// an earlier queued observation or absent outbox replay permission.
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	want.State, want.NoReplyReason = "completed", "control_changed"
	select {
	case got := <-done:
		if got.err != nil || got.receipt != want {
			t.Fatalf("read exact committed receipt: %+v %v want %+v", got.receipt, got.err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not finish after owned transaction commit")
	}
	before.JobState = "completed"
	menuPublicUnchanged(t, f, jobs, event, before, counters)
	if source, err := f.s.BindSource(t.Context(), f.local, event.ID); err != nil || source != want.Source {
		t.Fatalf("read changed source: %+v %v", source, err)
	}
}
