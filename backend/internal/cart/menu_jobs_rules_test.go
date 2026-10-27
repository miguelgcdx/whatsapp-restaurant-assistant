package cart_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// Every rule starts with real B admission, not an unimplemented claim as setup.
func claimAdmission(t *testing.T, f fixture, id string, at time.Time) cart.Admission {
	t.Helper()
	e := foundation.Event{ID: id, Sender: "fictional-sender", Text: " exact menu\né "}
	a, created, err := f.s.AcceptMenu(t.Context(), f.local, e, at)
	if err != nil || !created || a.JobID == "" || a.Source.EventUUID() == "" {
		t.Fatalf("B admission readiness: %+v %v %v", a, created, err)
	}
	return a
}

type claimView struct {
	a              cart.Admission
	s              foundation.Snapshot
	rows, counters string
	control        *conversation.Store
	token          string
	state          conversation.State
}

func claimRows(t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) string {
	t.Helper()
	var rows string
	if err := q.QueryRow(t.Context(), `SELECT json_build_array((SELECT json_agg(j ORDER BY id) FROM app.jobs j),(SELECT json_agg(e ORDER BY id) FROM app.inbound_events e),(SELECT json_agg(s ORDER BY event_id) FROM app.cart_execution_sources s))::text`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// Public receipts/source/control/snapshots are the primary oracle; a full row
// fingerprint additionally catches lease/generation changes hidden by Inspect.
func claimObserve(t *testing.T, f fixture, jobs *postgres.Store, a cart.Admission, control *conversation.Store, token string, state conversation.State) claimView {
	t.Helper()
	ctx := t.Context()
	got, err := f.s.ReadAdmission(ctx, f.local, a.Source.EventProviderID())
	if err != nil || got != a {
		t.Fatalf("public admission readiness: %+v %v want %+v", got, err, a)
	}
	if source, err := f.s.BindSource(ctx, f.local, a.Source.EventProviderID()); err != nil || source != a.Source {
		t.Fatalf("source readiness: %+v %v", source, err)
	}
	s, err := jobs.Inspect(ctx, a.Source.EventProviderID())
	if err != nil || s.Event.ID != a.Source.EventProviderID() || s.Event.Sender != a.Source.Sender() || s.Event.Text != " exact menu\né " || s.JobState != a.State || s.JobCount != 1 || s.OutboxCount != 0 || s.OutboxState != "" || s.ProviderID != "" {
		t.Fatalf("public snapshot readiness: %+v %v", s, err)
	}
	if got, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || got != state {
		t.Fatalf("authenticated current control readiness: %+v %v", got, err)
	}
	conn := sourceConn(t, f)
	defer conn.Close(context.Background())
	return claimView{a, s, claimRows(t, conn), menuCounters(t, f), control, token, state}
}

func claimUnchanged(t *testing.T, f fixture, jobs *postgres.Store, before claimView, control *conversation.Store, token string, state conversation.State) {
	t.Helper()
	if after := claimObserve(t, f, jobs, before.a, control, token, state); after != before {
		t.Errorf("claim changed durable public state/rows/counters: after=%+v before=%+v", after, before)
	}
}

func claimEmpty(t *testing.T, c cart.MenuClaim, ok bool, err, want error) {
	t.Helper()
	if c != (cart.MenuClaim{}) || ok || !errors.Is(err, want) {
		t.Errorf("TARGET empty claim: claim=%+v ok=%v error=%v want=%v", c, ok, err, want)
	}
}

func claimLeased(t *testing.T, f fixture, jobs *postgres.Store, a cart.Admission, c cart.MenuClaim, ok bool, err error, generation int64, attempt int32, before claimView) {
	t.Helper()
	if err != nil || !ok {
		t.Fatalf("TARGET must issue exact native claim: claim=%+v ok=%v error=%v", c, ok, err)
	}
	want := foundation.Job{ID: a.JobID, Event: foundation.Event{ID: a.Source.EventProviderID(), Sender: a.Source.Sender(), Text: " exact menu\né "}, Generation: generation, Attempt: attempt}
	if c.Source() != a.Source || c.Job() != want {
		t.Errorf("exact claim tuple: %+v %+v want %+v", c.Source(), c.Job(), want)
	}
	if s, err := jobs.Inspect(t.Context(), want.Event.ID); err != nil || s != (foundation.Snapshot{Event: want.Event, JobState: "leased", JobAttempt: attempt, JobCount: 1}) {
		t.Errorf("exact leased public effects: %+v %v", s, err)
	}
	a.State = "leased"
	if got, err := f.s.ReadAdmission(t.Context(), f.local, want.Event.ID); err != nil || got != a {
		t.Errorf("original admission retained: %+v %v", got, err)
	}
	if source, err := f.s.BindSource(t.Context(), f.local, want.Event.ID); err != nil || source != a.Source {
		t.Errorf("source changed: %+v %v", source, err)
	}
	if got := menuCounters(t, f); got != before.counters {
		t.Errorf("allocated identity/sequence/effect: %s want %s", got, before.counters)
	}
	if got, err := before.control.Read(t.Context(), before.token, f.local.ConversationID()); err != nil || got != before.state {
		t.Errorf("claim changed current authenticated control: %+v %v", got, err)
	}
	var storedGeneration int64
	var storedAttempt int32
	var deadline pgtype.Timestamptz
	var live bool
	conn := sourceConn(t, f)
	defer conn.Close(context.Background())
	if err := conn.QueryRow(t.Context(), `SELECT generation,attempt,lease_until,lease_until>clock_timestamp() FROM app.jobs WHERE id=$1 AND event_id=$2 AND route='menu_message' AND restaurant_id=$3 AND conversation_id=$4 AND admission_mode=$5 AND admission_version=$6`, a.JobID, a.Source.EventUUID(), restaurant, f.local.ConversationID().String(), a.Mode, a.Version).Scan(&storedGeneration, &storedAttempt, &deadline, &live); err != nil || storedGeneration != generation || storedAttempt != attempt || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite || !live {
		t.Errorf("stored finite live lease/provenance: %d %d %+v %v %v", storedGeneration, storedAttempt, deadline, live, err)
	}
}

func TestMenuClaimRulesInputs(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "inputs")
	jobs := menuJobs(t, f)
	a := claimAdmission(t, f, "inputs", time.Now())
	missing, err := f.s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := control.Read(t.Context(), token, missing.ConversationID()); !errors.Is(err, conversation.ErrNotFound) || got != (conversation.State{}) {
		t.Fatalf("missing control readiness: %+v %v", got, err)
	}
	other, err := cart.OpenStore(t.Context(), f.dsn, "a0000000-0000-4000-8000-000000000099")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, name := range []string{"zero-lease", "negative-lease", "nil-store", "zero-store", "nil-context", "zero-scope", "foreign-restaurant", "missing-control", "stale-ownership", "cancel", "deadline", "unpublished-deadline"} {
		t.Run(name, func(t *testing.T) {
			ctx, s, local, lease, want := t.Context(), f.s, f.local, time.Minute, cart.ErrInput
			switch name {
			case "zero-lease":
				lease = 0
			case "negative-lease":
				lease = -time.Nanosecond
			case "nil-store":
				s = nil
			case "zero-store":
				s = &cart.Store{}
			case "nil-context":
				ctx = nil
			case "zero-scope":
				local = cart.LocalContext{}
			case "foreign-restaurant":
				s = other
			case "missing-control":
				local, want = missing, cart.ErrNotFound
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = c, context.Canceled
			case "deadline":
				c, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx, want = c, context.DeadlineExceeded
			case "unpublished-deadline":
				ctx, want = sourceLateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}, context.DeadlineExceeded
			case "stale-ownership":
				want = cart.ErrNotFound
			}
			before := claimObserve(t, f, jobs, a, control, token, state)
			conn := sourceConn(t, f)
			if name == "stale-ownership" {
				if _, err := conn.Exec(t.Context(), `UPDATE app.cart_conversations SET business='changed' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.LoadLocal(t.Context(), f.local.ConversationID(), "fictional-business", "fictional-sender"); !errors.Is(err, cart.ErrNotFound) {
					t.Fatalf("stale ownership readiness: %v", err)
				}
			}
			t.Logf("READY %s: actual B admission, exact public state, current authenticated control or verified absence; TARGET error=%v", name, want)
			c, ok, err := s.ClaimMenu(ctx, local, lease)
			claimEmpty(t, c, ok, err, want)
			if name == "stale-ownership" {
				var business string
				if err := conn.QueryRow(t.Context(), `SELECT business FROM app.cart_conversations WHERE id=$1`, f.local.ConversationID().String()).Scan(&business); err != nil || business != "changed" {
					t.Errorf("claim changed stored ownership: %q %v", business, err)
				}
				if _, err := conn.Exec(t.Context(), `UPDATE app.cart_conversations SET business='fictional-business' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
					t.Fatal(err)
				}
			}
			claimUnchanged(t, f, jobs, before, control, token, state)
			if got, err := control.Read(t.Context(), token, missing.ConversationID()); !errors.Is(err, conversation.ErrNotFound) || got != (conversation.State{}) {
				t.Errorf("claim bootstrapped control: %+v %v", got, err)
			}
		})
	}
}

func TestMenuClaimRulesNoWork(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "no-work")
	jobs := menuJobs(t, f)
	t.Run("empty", func(t *testing.T) {
		before := menuCounters(t, f)
		var rows string
		if err := sourceConn(t, f).QueryRow(t.Context(), `SELECT count(*)::text FROM app.jobs`).Scan(&rows); err != nil || rows != "0" {
			t.Fatalf("empty readiness: %s %v", rows, err)
		}
		if got, err := jobs.Inspect(t.Context(), "absent"); !errors.Is(err, foundation.ErrNotFound) || got != (foundation.Snapshot{}) {
			t.Fatalf("empty public event readiness: %+v %v", got, err)
		}
		t.Log("READY empty: authenticated owned scope, zero jobs; TARGET no work")
		c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
		claimEmpty(t, c, ok, err, nil)
		if got := menuCounters(t, f); got != before {
			t.Errorf("empty allocated: %s", got)
		}
		if got, err := jobs.Inspect(t.Context(), "absent"); !errors.Is(err, foundation.ErrNotFound) || got != (foundation.Snapshot{}) {
			t.Errorf("empty persisted event: %+v %v", got, err)
		}
		if got, err := control.Read(t.Context(), token, f.local.ConversationID()); err != nil || got != state {
			t.Errorf("empty changed control: %+v %v", got, err)
		}
	})
	a := claimAdmission(t, f, "excluded", time.Now().Add(time.Hour))
	for _, name := range []string{"not-due", "live-lease", "terminal", "staff-admitted"} {
		t.Run(name, func(t *testing.T) {
			local, wantState := f, state
			if name == "staff-admitted" {
				var err error
				local.local, err = f.s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
				if err != nil {
					t.Fatal(err)
				}
				wantState = menuReady(t, local, control, token, conversation.ModeStaff, "staff")
				a = claimAdmission(t, local, "staff-excluded", time.Now())
			} else if name == "live-lease" {
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='leased',due_at=clock_timestamp()-interval '1 second',attempt=1,generation=1,lease_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
				a.State = "leased"
			} else if name == "terminal" {
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed',no_reply_reason='control_changed',lease_until=NULL WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
				a.State, a.NoReplyReason = "completed", "control_changed"
			}
			before := claimObserve(t, local, jobs, a, control, token, wantState)
			t.Logf("READY %s: genuine admission, exact public state/source/control; TARGET no work", name)
			c, ok, err := f.s.ClaimMenu(t.Context(), local.local, time.Minute)
			claimEmpty(t, c, ok, err, nil)
			claimUnchanged(t, local, jobs, before, control, token, wantState)
		})
	}
}

func TestMenuClaimRulesEligibility(t *testing.T) {
	for _, name := range []string{"due", "expired-reopen"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			control, token := menuControl(t, f)
			state := menuReady(t, f, control, token, conversation.ModeAI, "eligibility")
			jobs := menuJobs(t, f)
			a := claimAdmission(t, f, name, time.Now().Add(-time.Second))
			generation, attempt := int64(1), int32(1)
			if name == "expired-reopen" {
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='leased',generation=3,attempt=2,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
				a.State, generation, attempt = "leased", 4, 3
				reopened, err := cart.OpenStore(t.Context(), f.dsn, restaurant)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				f.s = reopened
				if got, err := f.s.LoadLocal(t.Context(), f.local.ConversationID(), "fictional-business", "fictional-sender"); err != nil || got != f.local {
					t.Fatalf("reopen ownership: %+v %v", got, err)
				}
			}
			before := claimObserve(t, f, jobs, a, control, token, state)
			if before.s.JobAttempt != attempt-1 {
				t.Fatalf("exact pre-claim attempt readiness: %d want %d", before.s.JobAttempt, attempt-1)
			}
			t.Logf("READY %s: actual admission/current control/exact identity and public state; TARGET claim tuple %d/%d", name, generation, attempt)
			c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
			claimLeased(t, f, jobs, a, c, ok, err, generation, attempt, before)
		})
	}
}

func TestMenuClaimRulesScopeOrdering(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "scope")
	jobs := menuJobs(t, f)
	foreign := f
	var err error
	foreign.local, err = f.s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	foreignState := menuReady(t, foreign, control, token, conversation.ModeAI, "foreign")
	earlier := claimAdmission(t, foreign, "foreign-earlier", time.Now())
	t.Run("foreign-only", func(t *testing.T) {
		before := claimObserve(t, foreign, jobs, earlier, control, token, foreignState)
		t.Log("READY foreign-only: actual other-conversation admission and empty target scope; TARGET no work")
		c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
		claimEmpty(t, c, ok, err, nil)
		claimUnchanged(t, foreign, jobs, before, control, token, foreignState)
		if got, err := control.Read(t.Context(), token, f.local.ConversationID()); err != nil || got != state {
			t.Errorf("scoped no-work changed target control: %+v %v", got, err)
		}
	})
	legacy := foundation.Event{ID: "legacy-before", Sender: "fictional-sender", Text: "legacy"}
	if created, err := jobs.Accept(t.Context(), legacy, time.Now()); err != nil || !created {
		t.Fatal(created, err)
	}
	if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed' WHERE event_id=(SELECT id FROM app.inbound_events WHERE provider_event_id=$1)`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	a := claimAdmission(t, f, "scope-later", time.Now())
	for _, name := range []string{"menu-predecessor", "legacy-predecessor", "released-predecessor"} {
		t.Run(name, func(t *testing.T) {
			if name != "menu-predecessor" {
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed',no_reply_reason='control_changed' WHERE id=$1`, earlier.JobID); err != nil {
					t.Fatal(err)
				}
				earlier.State, earlier.NoReplyReason = "completed", "control_changed"
			}
			if name == "legacy-predecessor" {
				// Genuine public legacy allocation precedes the target; only owned
				// job state changes, never event sequence/counter rewriting.
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='queued' WHERE event_id=(SELECT id FROM app.inbound_events WHERE provider_event_id=$1)`, legacy.ID); err != nil {
					t.Fatal(err)
				}
				if got, err := jobs.Inspect(t.Context(), "legacy-before"); err != nil || got.JobState != "queued" || got.JobCount != 1 || got.OutboxCount != 0 {
					t.Fatalf("legacy predecessor readiness: %+v %v", got, err)
				}
			}
			if name == "released-predecessor" {
				if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed' WHERE event_id=(SELECT id FROM app.inbound_events WHERE provider_event_id='legacy-before')`); err != nil {
					t.Fatal(err)
				}
			}
			before := claimObserve(t, f, jobs, a, control, token, state)
			other := claimObserve(t, foreign, jobs, earlier, control, token, foreignState)
			t.Logf("READY %s: exact scoped B admission plus earlier global-sender predecessor; TARGET scoped ordering", name)
			c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
			if name == "released-predecessor" {
				claimLeased(t, f, jobs, a, c, ok, err, 1, 1, before)
			} else {
				claimEmpty(t, c, ok, err, nil)
				claimUnchanged(t, f, jobs, before, control, token, state)
				claimUnchanged(t, foreign, jobs, other, control, token, foreignState)
			}
		})
	}
}

func TestMenuClaimRulesHistory(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "history")
	jobs := menuJobs(t, f)
	old := claimAdmission(t, f, "old-ai0", time.Now())
	for i, mode := range []conversation.Mode{conversation.ModeStaff, conversation.ModeAI} {
		var err error
		state, err = control.SetMode(t.Context(), token, state.ConversationID, mode, state.Version, fmt.Sprintf("transition-%d", i))
		if err != nil || state.Version != int64(i+1) {
			t.Fatalf("authenticated transition: %+v %v", state, err)
		}
		t.Run(string(mode), func(t *testing.T) {
			before := claimObserve(t, f, jobs, old, control, token, state)
			t.Logf("READY history %s%d: original public queued AI0, changed authenticated control; TARGET still claim original work, never model permission/completion", mode, state.Version)
			c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
			claimLeased(t, f, jobs, old, c, ok, err, 1, 1, before)
		})
		// Independent target readiness without an unimplemented ClaimMenu setup.
		if i == 0 {
			if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='queued',generation=0,attempt=0,lease_until=NULL WHERE id=$1`, old.JobID); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("fresh-return-ai2", func(t *testing.T) {
		// Old work stays persisted; setup its explicit terminal outcome only to
		// witness fresh post-return admission, not to test/implement Finish.
		if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed',no_reply_reason='control_changed',lease_until=NULL WHERE id=$1`, old.JobID); err != nil {
			t.Fatal(err)
		}
		old.State, old.NoReplyReason = "completed", "control_changed"
		fresh := claimAdmission(t, f, "fresh-ai2", time.Now())
		if fresh.Mode != "ai" || fresh.Version != 2 {
			t.Fatalf("fresh admission epoch: %+v", fresh)
		}
		claimObserve(t, f, jobs, old, control, token, state)
		before := claimObserve(t, f, jobs, fresh, control, token, state)
		t.Log("READY fresh-return AI2: retained original AI0 history and genuine fresh AI2 admission; TARGET fresh claim without effect")
		c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
		claimLeased(t, f, jobs, fresh, c, ok, err, 1, 1, before)
	})
}

func TestMenuClaimRulesConcurrent(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "concurrent")
	jobs := menuJobs(t, f)
	a := claimAdmission(t, f, "concurrent", time.Now())
	before := claimObserve(t, f, jobs, a, control, token, state)
	t.Log("READY concurrent: real unattempted queued admission; TARGET exactly one native issuance and one no-work")
	type result struct {
		c   cart.MenuClaim
		ok  bool
		err error
	}
	start, done := make(chan struct{}), make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			c, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
			done <- result{c, ok, err}
		}()
	}
	close(start)
	// Observe final durable effects only after both native calls have returned.
	results := []result{<-done, <-done}
	wins := 0
	for _, got := range results {
		if got.ok {
			wins++
		} else {
			claimEmpty(t, got.c, got.ok, got.err, nil)
		}
	}
	if wins != 1 {
		t.Errorf("TARGET concurrent single winner: wins=%d want1", wins)
	}
	if wins == 0 {
		claimUnchanged(t, f, jobs, before, control, token, state)
	}
	for _, got := range results {
		if got.ok {
			claimLeased(t, f, jobs, a, got.c, got.ok, got.err, 1, 1, before)
		}
	}
}

// Observe either the real target's premature return or its owned database lock
// wait. A missing barrier is not a readiness/setup failure mislabeled as RED.
type claimResult struct {
	c   cart.MenuClaim
	ok  bool
	err error
}

func claimBlocked(t *testing.T, conn *pgx.Conn, done chan claimResult) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		select {
		case got := <-done:
			t.Errorf("TARGET must wait for owned transaction: premature claim=%+v ok=%v error=%v", got.c, got.ok, got.err)
			done <- got
			return false
		default:
		}
		var waiting bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatalf("owned lock observation unavailable: %v", err)
		}
		if waiting {
			return true
		}
	}
}

func TestMenuClaimRulesBlocking(t *testing.T) {
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "blocking")
	jobs := menuJobs(t, f)
	legacy := foundation.Event{ID: "contended-earlier", Sender: "fictional-sender", Text: "legacy"}
	if created, err := jobs.Accept(t.Context(), legacy, time.Now()); err != nil || !created {
		t.Fatal(created, err)
	}
	if _, err := sourceConn(t, f).Exec(t.Context(), `UPDATE app.jobs SET state='completed' WHERE event_id=(SELECT id FROM app.inbound_events WHERE provider_event_id=$1)`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	a := claimAdmission(t, f, "blocking", time.Now())
	for _, name := range []string{"ownership-clock", "source-clock", "control-clock", "job-clock", "cancel", "deadline", "eligibility-recheck", "lease-recheck", "ordering-recheck"} {
		t.Run(name, func(t *testing.T) {
			conn := sourceConn(t, f)
			// Each target is independently queued even after a future GREEN run.
			if _, err := conn.Exec(t.Context(), `UPDATE app.jobs SET state='queued',generation=0,attempt=0,lease_until=NULL,due_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.JobID); err != nil {
				t.Fatal(err)
			}
			a.State = "queued"
			if name == "lease-recheck" {
				if _, err := conn.Exec(t.Context(), `UPDATE app.jobs SET state='leased',generation=1,attempt=1,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
				a.State = "leased"
			}
			before := claimObserve(t, f, jobs, a, control, token, state)
			tx, err := conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			query, id := `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant
			switch name {
			case "source-clock":
				query, id = `SELECT event_id FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`, a.Source.EventUUID()
			case "control-clock":
				query, id = `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=$1 FOR UPDATE`, f.local.ConversationID().String()
			case "job-clock":
				query, id = `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`, a.JobID
			}
			if _, err := tx.Exec(t.Context(), query, id); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if name == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(t.Context(), 150*time.Millisecond)
				defer stop()
			}
			done := make(chan claimResult, 1)
			t.Logf("READY %s: real public queued admission/control/source plus held owned row lock; TARGET must serialize and recheck", name)
			go func() { c, ok, err := f.s.ClaimMenu(ctx, f.local, time.Second); done <- claimResult{c, ok, err} }()
			observer := sourceConn(t, f)
			blocked := claimBlocked(t, observer, done)
			if name == "cancel" {
				cancel()
			}
			if name == "deadline" {
				<-ctx.Done()
			}
			if name == "eligibility-recheck" {
				if _, err := tx.Exec(t.Context(), `UPDATE app.jobs SET due_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
			}
			if name == "lease-recheck" {
				if _, err := tx.Exec(t.Context(), `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
			}
			if name == "ordering-recheck" {
				if _, err := tx.Exec(t.Context(), `UPDATE app.jobs SET state='queued' WHERE event_id=(SELECT id FROM app.inbound_events WHERE provider_event_id='contended-earlier')`); err != nil {
					t.Fatal(err)
				}
			}
			var release time.Time
			if blocked && (name == "ownership-clock" || name == "source-clock" || name == "control-clock" || name == "job-clock") {
				// Advance against the authoritative owned DB clock, not a sleep or
				// guessed scheduler delay: lease1s must start AFTER >1s lock wait.
				var end time.Time
				if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()+interval '1100 milliseconds'`).Scan(&end); err != nil {
					t.Fatal(err)
				}
				for {
					if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&release); err != nil {
						t.Fatal(err)
					}
					if !release.Before(end) {
						break
					}
				}
			}
			expectedRows := claimRows(t, tx)
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := <-done
			switch name {
			case "cancel", "deadline":
				want := context.Canceled
				if name == "deadline" {
					want = context.DeadlineExceeded
				}
				claimEmpty(t, got.c, got.ok, got.err, want)
				claimUnchanged(t, f, jobs, before, control, token, state)
			case "eligibility-recheck", "lease-recheck", "ordering-recheck":
				afterSetup := claimObserve(t, f, jobs, a, control, token, state)
				claimEmpty(t, got.c, got.ok, got.err, nil)
				if afterSetup.s != before.s || afterSetup.a != before.a || afterSetup.counters != before.counters || afterSetup.rows != expectedRows {
					t.Errorf("recheck changed public input: %+v", afterSetup)
				}
			default:
				claimLeased(t, f, jobs, a, got.c, got.ok, got.err, 1, 1, before)
				var deadline time.Time
				if err := observer.QueryRow(t.Context(), `SELECT lease_until FROM app.jobs WHERE id=$1`, a.JobID).Scan(&deadline); err != nil || !deadline.After(release.Add(900*time.Millisecond)) {
					t.Errorf("TARGET fresh lease clock after blocking: deadline=%s release=%s error=%v", deadline, release, err)
				}
			}
		})
	}
}
