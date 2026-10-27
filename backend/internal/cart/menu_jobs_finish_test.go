package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// Readiness uses actual authenticated control, admission and opaque issuance.
// SQL fingerprints supplement, rather than replace, the public state oracle.
func menuExecutionReady(t *testing.T, id string) (fixture, *postgres.Store, cart.MenuClaim, claimView) {
	t.Helper()
	f := setup(t)
	control, token := menuControl(t, f)
	state := menuReady(t, f, control, token, conversation.ModeAI, "execution-"+id)
	jobs := menuJobs(t, f)
	a := claimAdmission(t, f, id, time.Now())
	queued := claimObserve(t, f, jobs, a, control, token, state)
	claim, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
	claimLeased(t, f, jobs, a, claim, ok, err, 1, 1, queued)
	if t.Failed() {
		t.Fatal("genuine issued-claim readiness failed before new target")
	}
	a.State = "leased"
	before := claimObserve(t, f, jobs, a, control, token, state)
	if claim == (cart.MenuClaim{}) || a.Mode != "ai" || a.Version != 0 || before.s.JobAttempt != 1 {
		t.Fatalf("genuine AI0 exact tuple readiness: claim=%+v admission=%+v snapshot=%+v", claim, a, before.s)
	}
	t.Log("READY: actual authenticated AI0/B admission/native ClaimMenu issuance; exact public leased event/source/job/generation1/attempt1, finite live lease, no effects, immutable epoch and counters verified")
	return f, jobs, claim, before
}

// S1/S3: a valid menu-route tuple must supply a usable lock-scoped clock guard,
// without completing work, changing history or allocating an effect.
func TestMenuGuardFreshClaim(t *testing.T) {
	f, jobs, claim, before := menuExecutionReady(t, "guard-fresh")
	source := claim.Source()
	binding := postgres.MenuExecutionBinding{
		EventUUID: source.EventUUID(), RestaurantID: source.RestaurantID(),
		ConversationID: source.ConversationID().String(), Business: source.Business(),
		Sender: source.Sender(), AdmissionMode: before.a.Mode, AdmissionVersion: before.a.Version,
	}
	tx, err := sourceConn(t, f).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	})
	t.Log("TARGET: valid persisted menu tuple must return a usable transaction-owned fresh-clock guard")
	guard, err := postgres.LockMenuExecutionJob(t.Context(), tx, binding, claim.Job())
	if err != nil || guard == nil {
		t.Errorf("TARGET valid menu claim: guard_present=%v error=%v; want nonnil guard and nil error", guard != nil, err)
	} else if err := guard(); err != nil {
		t.Errorf("TARGET valid live-lease closure: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
	t.Log("OBSERVED: rollback released target transaction; public receipt/source/control/snapshot, complete stored rows and counters unchanged")
}

// S1/S3: unchanged current AI authority is not a no-reply completion reason.
// The whole issued claim and its historical admission must remain untouched.
func TestMenuFinishCurrentEpoch(t *testing.T) {
	f, jobs, claim, before := menuExecutionReady(t, "finish-current")
	t.Log("TARGET: same current AI0 epoch must return false,nil and leave work leased")
	finished, err := f.s.FinishMenuWithoutReply(t.Context(), f.local, claim)
	if finished || err != nil {
		t.Errorf("TARGET unchanged current AI epoch: finished=%v error=%v; want false,nil", finished, err)
	}
	claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
	t.Log("OBSERVED: public receipt/source/control/snapshot, complete stored rows and counters unchanged; no reply/outbox allocation")
}

// Remaining L49 authority boundaries reuse genuine issuance and public oracles.
func menuBinding(c cart.MenuClaim, a cart.Admission) postgres.MenuExecutionBinding {
	s := c.Source()
	return postgres.MenuExecutionBinding{EventUUID: s.EventUUID(), RestaurantID: s.RestaurantID(), ConversationID: s.ConversationID().String(), Business: s.Business(), Sender: s.Sender(), AdmissionMode: a.Mode, AdmissionVersion: a.Version}
}

func menuGuardTx(t *testing.T, f fixture) pgx.Tx {
	t.Helper()
	tx, err := sourceConn(t, f).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	})
	return tx
}

func menuGuardWant(t *testing.T, f fixture, binding postgres.MenuExecutionBinding, job foundation.Job, want error) {
	t.Helper()
	tx := menuGuardTx(t, f)
	guard, err := postgres.LockMenuExecutionJob(t.Context(), tx, binding, job)
	if err != nil && guard != nil {
		t.Error("TARGET rejection returned a closure")
	}
	if err == nil && guard != nil {
		err = guard()
	} else if err == nil {
		t.Error("TARGET returned nil guard without error")
	}
	if !errors.Is(err, want) {
		t.Errorf("TARGET guard: %v want %v", err, want)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// One isolated fixture per family; each subcase restores only owned tampering.
// The reclaimed case obtains its replacement through the actual public issuer.
func menuStaleRules(t *testing.T, check func(*testing.T, fixture, cart.MenuClaim, claimView)) {
	f, jobs, issued, original := menuExecutionReady(t, "stale")
	conn := sourceConn(t, f)
	for _, name := range []string{"zero", "expired", "generation", "attempt", "admission-epoch", "completed-retry", "reclaimed"} {
		t.Run(name, func(t *testing.T) {
			if _, err := conn.Exec(t.Context(), `UPDATE app.jobs SET state='leased',no_reply_reason=NULL,generation=1,attempt=1,admission_version=0,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, original.a.JobID); err != nil {
				t.Fatal(err)
			}
			a, c, change := original.a, issued, ""
			switch name {
			case "zero":
				c = cart.MenuClaim{}
			case "expired", "reclaimed":
				change = "lease_until=clock_timestamp()-interval '1 second'"
			case "generation":
				change = "generation=2"
			case "attempt":
				change = "attempt=2"
			case "admission-epoch":
				change, a.Version = "admission_version=1", 1
			case "completed-retry":
				change = "state='completed',no_reply_reason='control_changed',lease_until=NULL"
				a.State, a.NoReplyReason = "completed", "control_changed"
			}
			if change != "" {
				if _, err := conn.Exec(t.Context(), `UPDATE app.jobs SET `+change+` WHERE id=$1`, a.JobID); err != nil {
					t.Fatal(err)
				}
			}
			before := claimObserve(t, f, jobs, a, original.control, original.token, original.state)
			if name == "reclaimed" {
				fresh, ok, err := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
				claimLeased(t, f, jobs, a, fresh, ok, err, 2, 2, before)
				if fresh == issued || t.Failed() {
					t.Fatal("replacement issued-claim readiness failed")
				}
				before = claimObserve(t, f, jobs, a, original.control, original.token, original.state)
			}
			t.Logf("READY %s: genuine original issuance plus owned stored/public boundary; TARGET stale rejection", name)
			check(t, f, c, before)
			claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
			t.Log("OBSERVED: stale rejection public receipt/source/control/snapshot/full rows/counters invariant")
		})
	}
}

func TestMenuGuardRules(t *testing.T) {
	t.Run("tuple-dependencies-control", func(t *testing.T) {
		f, jobs, c, before := menuExecutionReady(t, "guard-rules")
		for _, name := range []string{"event-uuid", "restaurant", "conversation", "business", "sender", "admission-mode", "admission-version", "job-id", "provider-event", "event-sender", "text", "generation", "attempt"} {
			t.Run(name, func(t *testing.T) {
				b, job := menuBinding(c, before.a), c.Job()
				switch name {
				case "event-uuid":
					b.EventUUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				case "restaurant":
					b.RestaurantID = "a0000000-0000-4000-8000-000000000099"
				case "conversation":
					b.ConversationID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				case "business":
					b.Business += "changed"
				case "sender":
					b.Sender += "changed"
				case "admission-mode":
					b.AdmissionMode = "staff"
				case "admission-version":
					b.AdmissionVersion++
				case "job-id":
					job.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				case "provider-event":
					job.Event.ID += "changed"
				case "event-sender":
					job.Event.Sender += "changed"
				case "text":
					job.Event.Text += "changed"
				case "generation":
					job.Generation++
				case "attempt":
					job.Attempt++
				}
				t.Logf("READY exact issued tuple; TARGET one expected/full-job mismatch: %s", name)
				menuGuardWant(t, f, b, job, foundation.ErrStaleLease)
				claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
			})
		}
		for _, name := range []string{"nil-context", "nil-tx", "closed-tx", "canceled", "elapsed"} {
			t.Run(name, func(t *testing.T) {
				ctx, tx := t.Context(), menuGuardTx(t, f)
				switch name {
				case "nil-context":
					ctx = nil
				case "nil-tx":
					tx = nil
				case "closed-tx":
					if err := tx.Rollback(ctx); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "elapsed":
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				t.Logf("READY valid tuple; TARGET opaque dependency rejection %s", name)
				g, err := postgres.LockMenuExecutionJob(ctx, tx, menuBinding(c, before.a), c.Job())
				if g != nil || err != postgres.ErrClaimUnavailable || err.Error() != "claim verification temporarily unavailable" {
					t.Errorf("TARGET dependency: guard=%v error=%v", g != nil, err)
				}
				if tx != nil {
					_ = tx.Rollback(t.Context())
				}
				claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
			})
		}
		t.Run("closed-closure", func(t *testing.T) {
			tx := menuGuardTx(t, f)
			g, err := postgres.LockMenuExecutionJob(t.Context(), tx, menuBinding(c, before.a), c.Job())
			if err != nil || g == nil {
				t.Errorf("TARGET usable closure prerequisite: %v; UNREACHED closed closure", err)
			} else if err := g(); err != nil {
				t.Errorf("TARGET live closure: %v", err)
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if g != nil {
				if err := g(); err != postgres.ErrClaimUnavailable || err.Error() != "claim verification temporarily unavailable" {
					t.Errorf("TARGET closed closure: %v", err)
				}
			}
			claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
		})
		t.Run("legacy-route", func(t *testing.T) {
			event := foundation.Event{ID: "legacy-guard", Sender: "other-sender", Text: "legacy"}
			if ok, err := jobs.Accept(t.Context(), event, time.Now()); err != nil || !ok {
				t.Fatal(ok, err)
			}
			job, ok, err := jobs.ClaimJob(t.Context(), time.Now(), time.Minute)
			if err != nil || !ok || job.Event != event {
				t.Fatal("legacy issuance readiness", job, ok, err)
			}
			b := menuBinding(c, before.a)
			if err := sourceConn(t, f).QueryRow(t.Context(), `SELECT id::text FROM app.inbound_events WHERE provider_event_id=$1`, event.ID).Scan(&b.EventUUID); err != nil {
				t.Fatal(err)
			}
			b.Sender = event.Sender
			before = claimObserve(t, f, jobs, before.a, before.control, before.token, before.state)
			t.Log("READY genuine legacy leased job; TARGET menu-route isolation, not legacy completion")
			menuGuardWant(t, f, b, job, foundation.ErrStaleLease)
			claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
		})
		for i, mode := range []conversation.Mode{conversation.ModeStaff, conversation.ModeAI, conversation.ModeAI} {
			t.Run([]string{"current-staff", "returned-ai", "same-mode-new-version"}[i], func(t *testing.T) {
				state, err := before.control.SetMode(t.Context(), before.token, f.local.ConversationID(), mode, before.state.Version, []string{"guard-staff", "guard-return", "guard-same"}[i])
				if err != nil || state.Mode != mode || state.Version != int64(i+1) {
					t.Fatal("authenticated transition readiness", state, err)
				}
				before = claimObserve(t, f, jobs, before.a, before.control, before.token, state)
				t.Log("READY changed authenticated current control; TARGET structural claim remains valid, never model permission")
				menuGuardWant(t, f, menuBinding(c, before.a), c.Job(), nil)
				claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
			})
		}
	})
	t.Run("stale", func(t *testing.T) {
		menuStaleRules(t, func(t *testing.T, f fixture, c cart.MenuClaim, before claimView) {
			original := before.a
			original.Version = 0 // Expected immutable issuance, not tampered storage.
			menuGuardWant(t, f, menuBinding(c, original), c.Job(), foundation.ErrStaleLease)
		})
	})
	t.Run("blocking-clock", func(t *testing.T) { menuBlockingRules(t, false) })
}

func TestMenuFinishRules(t *testing.T) {
	for _, name := range []string{"changed-staff", "ai-return", "same-mode-version"} {
		t.Run(name, func(t *testing.T) {
			f, jobs, c, before := menuExecutionReady(t, name)
			modes := []conversation.Mode{conversation.ModeStaff}
			if name == "ai-return" {
				modes = append(modes, conversation.ModeAI)
			}
			if name == "same-mode-version" {
				modes = []conversation.Mode{conversation.ModeAI}
			}
			state := before.state
			for _, mode := range modes {
				var err error
				state, err = before.control.SetMode(t.Context(), before.token, f.local.ConversationID(), mode, state.Version, string(mode))
				if err != nil {
					t.Fatal("authenticated changed-control readiness", err)
				}
			}
			before = claimObserve(t, f, jobs, before.a, before.control, before.token, state)
			t.Logf("READY original leased AI0/current authenticated %s%d; TARGET committed no-reply terminal control_changed", state.Mode, state.Version)
			finished, err := f.s.FinishMenuWithoutReply(t.Context(), f.local, c)
			if err != nil || !finished {
				t.Errorf("TARGET changed epoch: finished=%v error=%v; UNREACHED terminal-effects/history/retry assertions", finished, err)
				claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
				return
			}
			a := before.a
			a.State, a.NoReplyReason = "completed", "control_changed"
			after := claimObserve(t, f, jobs, a, before.control, before.token, state)
			var expectedRows, actualRows []any
			if err := json.Unmarshal([]byte(before.rows), &expectedRows); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(after.rows), &actualRows); err != nil {
				t.Fatal(err)
			}
			job := expectedRows[0].([]any)[0].(map[string]any)
			job["state"], job["no_reply_reason"], job["lease_until"] = "completed", "control_changed", nil
			if after.counters != before.counters || after.s.JobAttempt != before.s.JobAttempt || !reflect.DeepEqual(actualRows, expectedRows) {
				t.Errorf("TARGET exact retained rows/counters/attempt/generation/history: after=%+v before=%+v", after, before)
			}
			finished, err = f.s.FinishMenuWithoutReply(t.Context(), f.local, c)
			if finished || !errors.Is(err, foundation.ErrStaleLease) {
				t.Errorf("TARGET completed retry: %v %v", finished, err)
			}
			claimUnchanged(t, f, jobs, after, after.control, after.token, state)
			t.Log("OBSERVED: exact terminal history, unchanged generation/attempt/event/source/counters, no reply/effects; completed retry stale")
		})
	}
	t.Run("stale", func(t *testing.T) {
		menuStaleRules(t, func(t *testing.T, f fixture, c cart.MenuClaim, before claimView) {
			finished, err := f.s.FinishMenuWithoutReply(t.Context(), f.local, c)
			if finished || !errors.Is(err, foundation.ErrStaleLease) {
				t.Errorf("TARGET stale Finish: %v %v", finished, err)
			}
		})
	})
	t.Run("inputs", func(t *testing.T) {
		f, jobs, c, before := menuExecutionReady(t, "finish-inputs")
		other, err := f.s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
		if err != nil {
			t.Fatal(err)
		}
		foreign := f
		foreign.local = other
		otherState := menuReady(t, foreign, before.control, before.token, conversation.ModeAI, "other-control")
		wrongRestaurant, err := cart.OpenStore(t.Context(), f.dsn, "a0000000-0000-4000-8000-000000000099")
		if err != nil {
			t.Fatal(err)
		}
		defer wrongRestaurant.Close()
		before = claimObserve(t, f, jobs, before.a, before.control, before.token, before.state)
		for _, name := range []string{"nil-context", "nil-store", "zero-store", "zero-local", "foreign-restaurant", "wrong-scope", "canceled", "elapsed", "unpublished-deadline"} {
			t.Run(name, func(t *testing.T) {
				ctx, store, local, want := t.Context(), f.s, f.local, cart.ErrInput
				switch name {
				case "nil-context":
					ctx = nil
				case "nil-store":
					store = nil
				case "zero-store":
					store = &cart.Store{}
				case "zero-local":
					local = cart.LocalContext{}
				case "foreign-restaurant":
					store = wrongRestaurant
				case "wrong-scope":
					local, want = other, foundation.ErrStaleLease
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					want = context.Canceled
				case "elapsed":
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
					want = context.DeadlineExceeded
				case "unpublished-deadline":
					ctx, want = sourceLateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}, context.DeadlineExceeded
				}
				t.Logf("READY genuine claim and authenticated scopes; TARGET %s precise error=%v", name, want)
				finished, err := store.FinishMenuWithoutReply(ctx, local, c)
				if finished || !errors.Is(err, want) {
					t.Errorf("TARGET Finish input: %v %v want %v", finished, err, want)
				}
				claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
				if got, err := before.control.Read(t.Context(), before.token, other.ConversationID()); err != nil || got != otherState {
					t.Errorf("other current control changed: %+v %v", got, err)
				}
			})
		}
		t.Run("missing-current-control", func(t *testing.T) {
			tx := menuGuardTx(t, f)
			defer tx.Rollback(context.Background())
			for _, table := range []string{"conversation_control_operations", "conversation_controls"} {
				affected, err := tx.Exec(t.Context(), `DELETE FROM app.`+table+` WHERE restaurant_id=$1 AND conversation_id=$2`, restaurant, f.local.ConversationID().String())
				if err != nil || affected.RowsAffected() != 1 {
					t.Fatalf("owned missing-control setup %s: affected=%d error=%v", table, affected.RowsAffected(), err)
				}
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got, err := before.control.Read(t.Context(), before.token, other.ConversationID()); err != nil || got != otherState {
				t.Fatalf("setup changed other authenticated control: %+v %v", got, err)
			}
			if claimRows(t, sourceConn(t, f)) != before.rows {
				t.Fatal("setup changed job/event/source rows")
			}
			counters, rows := menuCounters(t, f), claimRows(t, sourceConn(t, f))
			assertMissing := func() {
				if got, err := before.control.Read(t.Context(), before.token, f.local.ConversationID()); got != (conversation.State{}) || !errors.Is(err, conversation.ErrNotFound) {
					t.Fatalf("missing current control: %+v %v", got, err)
				}
				if got, err := f.s.ReadAdmission(t.Context(), f.local, before.a.Source.EventProviderID()); got != (cart.Admission{}) || !errors.Is(err, cart.ErrNotFound) {
					t.Fatalf("fail-closed admission: %+v %v", got, err)
				}
			}
			assertMissing()
			t.Log("READY genuine issuance then narrowly removed current control; TARGET fail closed without authority bootstrap")
			finished, err := f.s.FinishMenuWithoutReply(t.Context(), f.local, c)
			if finished || !errors.Is(err, cart.ErrNotFound) {
				t.Errorf("TARGET missing control: %v %v", finished, err)
			}
			assertMissing()
			if got, err := jobs.Inspect(t.Context(), before.a.Source.EventProviderID()); err != nil || got != before.s {
				t.Errorf("missing control changed snapshot: %+v %v", got, err)
			}
			if got, err := f.s.BindSource(t.Context(), f.local, before.a.Source.EventProviderID()); err != nil || got != before.a.Source {
				t.Errorf("missing control changed source: %+v %v", got, err)
			}
			if menuCounters(t, f) != counters || claimRows(t, sourceConn(t, f)) != rows {
				t.Error("missing-control rejection changed rows/counters")
			}
		})
	})
	t.Run("blocking-clock", func(t *testing.T) { menuBlockingRules(t, true) })
}

type menuBoundaryResult struct {
	finished bool
	err      error
}

// Detect a premature target result separately from missing fixture readiness.
func menuBoundaryWait(t *testing.T, observer *pgx.Conn, done chan menuBoundaryResult) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		select {
		case result := <-done:
			t.Errorf("TARGET premature return before owned lock release: %+v; UNREACHED post-wait clock/context boundary", result)
			done <- result
			return false
		default:
		}
		var blocked bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			executionWait(t, observer, "")
			return true
		}
	}
}

func menuBlockingRules(t *testing.T, finish bool) {
	f, jobs, c, original := menuExecutionReady(t, "blocking")
	names := []string{"initial-expiry", "later-closure-expiry"}
	if finish {
		names = []string{"expiry", "cancel", "deadline"}
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			observer := sourceConn(t, f)
			if _, err := observer.Exec(t.Context(), `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '600 milliseconds' WHERE id=$1`, original.a.JobID); err != nil {
				t.Fatal(err)
			}
			before := claimObserve(t, f, jobs, original.a, original.control, original.token, original.state)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if name == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(t.Context(), 150*time.Millisecond)
				defer stop()
			}
			reader, gate := menuGuardTx(t, f), menuGuardTx(t, f)
			var guard func() error
			if name == "later-closure-expiry" {
				var err error
				guard, err = postgres.LockMenuExecutionJob(ctx, reader, menuBinding(c, before.a), c.Job())
				if err != nil || guard == nil {
					t.Errorf("TARGET later-statement guard acquisition: %v; UNREACHED later blocking/closure", err)
					_ = reader.Rollback(t.Context())
					_ = gate.Rollback(t.Context())
					claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
					return
				}
				if err := guard(); err != nil {
					t.Fatal("initial live closure readiness", err)
				}
				if _, err := gate.Exec(t.Context(), `LOCK TABLE app.outbox IN ACCESS EXCLUSIVE MODE`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := gate.Exec(t.Context(), `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`, c.Job().ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan menuBoundaryResult, 1)
			joined := false
			defer func() {
				cancel()
				_ = gate.Rollback(context.Background())
				if !joined {
					<-done
				}
			}()
			t.Logf("READY %s: genuine issuance, public leased state and owned database lock; TARGET wait then fresh clock/context", name)
			go func() {
				if finish {
					ok, err := f.s.FinishMenuWithoutReply(ctx, f.local, c)
					done <- menuBoundaryResult{ok, err}
					return
				}
				var err error
				if guard != nil {
					_, err = reader.Exec(ctx, `SELECT count(*) FROM app.outbox`)
				} else {
					guard, err = postgres.LockMenuExecutionJob(ctx, reader, menuBinding(c, before.a), c.Job())
				}
				if err == nil && guard != nil {
					err = guard()
				}
				done <- menuBoundaryResult{false, err}
			}()
			blocked := menuBoundaryWait(t, observer, done)
			if blocked {
				switch name {
				case "cancel":
					cancel()
				case "deadline":
					<-ctx.Done()
				default:
					executionExpired(t, observer, c.Job())
				}
			}
			if err := gate.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			result := <-done
			joined = true
			want := foundation.ErrStaleLease
			if name == "cancel" {
				want = context.Canceled
			}
			if name == "deadline" {
				want = context.DeadlineExceeded
			}
			if result.finished || !errors.Is(result.err, want) {
				t.Errorf("TARGET post-wait rejection: %+v want %v (wait reached=%v)", result, want, blocked)
			}
			_ = reader.Rollback(t.Context())
			claimUnchanged(t, f, jobs, before, before.control, before.token, before.state)
			t.Logf("OBSERVED: joined target, released owned locks/transactions, public/full-row/counter invariance; real wait reached=%v", blocked)
		})
	}
}
