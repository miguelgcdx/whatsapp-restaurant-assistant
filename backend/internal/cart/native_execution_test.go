package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

func TestNativeActionConsumer(t *testing.T) {
	// Each group owns an isolated database; all assertions use public operations.
	for _, scenario := range []string{"takeover-replay", "leases-errors", "edits-rollback", "blocking-expiry"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			control, token := menuControl(t, f)
			menuReady(t, f, control, token, conversation.ModeAI, "consumer-init")
			event := foundation.Event{ID: "consumer", Sender: "fictional-sender", Text: "trusted saved edit"}
			if _, ok, err := f.s.AcceptMenu(ctx, f.local, event, time.Now().UTC()); err != nil || !ok {
				t.Fatalf("intake: %v %v", ok, err)
			}
			menuClaim, ok, err := f.s.ClaimMenu(ctx, f.local, time.Minute)
			if err != nil || !ok {
				t.Fatalf("menu claim: %v %v", ok, err)
			}
			initial := create(t, f)
			admit := func(slot int64, snapshot cart.Snapshot, action cart.Action) cart.NativeActionRef {
				t.Helper()
				call, err := cart.EditCall(snapshot.ID, snapshot.Version, action)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := f.s.AdmitNativeEdit(ctx, f.local, menuClaim, slot, call)
				if err != nil {
					t.Fatal(err)
				}
				return ref
			}
			ref := admit(0, initial.Cart, add(t, 1))
			jobs := menuJobs(t, f)
			c := sourceConn(t, f)
			work := func() string {
				t.Helper()
				var body string
				if err := c.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(n) ORDER BY runtime_slot)::text FROM app.native_cart_actions n`).Scan(&body); err != nil {
					t.Fatal(err)
				}
				return body
			}
			read := func() cart.Snapshot {
				t.Helper()
				snapshot, err := f.s.Read(ctx, f.local, initial.Cart.ID)
				if err != nil {
					t.Fatal(err)
				}
				return snapshot
			}
			expire := func() {
				t.Helper()
				if _, err := c.Exec(ctx, `UPDATE app.native_cart_actions SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1`, menuClaim.Source().EventUUID()); err != nil {
					t.Fatal(err)
				}
			}
			acquire := func(ref cart.NativeActionRef) cart.NativeActionClaim {
				t.Helper()
				claim, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute)
				if err != nil || !ok || claim == (cart.NativeActionClaim{}) || claim.Ref() != ref {
					t.Fatalf("fresh recovery: %+v %v %v", claim, ok, err)
				}
				return claim
			}
			unchanged := func(before cart.Snapshot, beforeWork string) {
				t.Helper()
				if !reflect.DeepEqual(read(), before) || work() != beforeWork {
					t.Fatal("rejected operation changed cart/native work")
				}
			}
			jobBefore, err := jobs.Inspect(ctx, event.ID)
			if err != nil {
				t.Fatal(err)
			}
			admissionBefore, err := f.s.ReadAdmission(ctx, f.local, event.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "takeover-replay":
				if _, err := control.SetMode(ctx, token, f.local.ConversationID(), conversation.ModeStaff, 0, "takeover"); err != nil {
					t.Fatal(err)
				}
				if ok, err := f.s.FinishMenuWithoutReply(ctx, f.local, menuClaim); err != nil || !ok {
					t.Fatalf("retire reply: %v %v", ok, err)
				}
				jobBefore, err = jobs.Inspect(ctx, event.ID)
				if err != nil || jobBefore.JobState != "completed" || jobBefore.OutboxCount != 0 {
					t.Fatalf("paused reply: %+v %v", jobBefore, err)
				}
				admissionBefore, err = f.s.ReadAdmission(ctx, f.local, event.ID)
				if err != nil {
					t.Fatal(err)
				}
				// Restart discovery requires neither a saved reference nor a model call.
				reopened, err := cart.OpenStore(ctx, f.dsn, restaurant)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.Close)
				claim, ok, err := reopened.ClaimNextNativeEdit(ctx, time.Minute)
				if err != nil || !ok || claim.Ref() != ref {
					t.Fatalf("discover admitted work after takeover: %+v %v %v", claim, ok, err)
				}
				if raw, err := json.Marshal(claim); err != nil || string(raw) != "{}" {
					t.Fatalf("claim exposed authority: %s %v", raw, err)
				}
				before := work()
				if got, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute); err != nil || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("live contention: %+v %v %v", got, ok, err)
				}
				if got, ok, err := f.s.ClaimNextNativeEdit(ctx, time.Minute); err != nil || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("live discovery: %+v %v %v", got, ok, err)
				}
				unchanged(initial.Cart, before)
				result, err := reopened.ExecuteNativeEdit(ctx, claim)
				if err != nil || result.OperationID != ref.OperationID() || result.Cart.Version.Int64() != 1 || len(result.Cart.Units) != 1 || result.Cart.Subtotal != 650 {
					t.Fatalf("execute saved edit: %+v %v", result, err)
				}
				if got, err := f.s.ExecuteNativeEdit(ctx, claim); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("retired claim/history: %+v %v", got, err)
				}
				if got, ok, err := f.s.ClaimNextNativeEdit(ctx, time.Minute); err != nil || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("completed discovery: %+v %v %v", got, ok, err)
				}
				note, err := cart.SetNote([]cart.UnitID{result.Cart.Units[0].ID}, "later note")
				if err != nil {
					t.Fatal(err)
				}
				later := apply(t, f, result, note)
				if _, err := f.cat.SetAvailability(ctx, menu, 0, false); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Exec(ctx, `UPDATE app.menu_items SET price_minor=999 WHERE restaurant_id=$1 AND id=$2`, restaurant, menu); err != nil {
					t.Fatal(err)
				}
				fresh := acquire(ref)
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, claim); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("old claim before cached history: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
				expire()
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, fresh); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("expired authority before cached history: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
				fresh = acquire(ref)
				got, err := f.s.ExecuteNativeEdit(ctx, fresh)
				if err != nil || !reflect.DeepEqual(got, result) || !reflect.DeepEqual(read(), later.Cart) {
					t.Fatalf("exact historical replay: %+v %v", got, err)
				}
				if state, err := control.Read(ctx, token, f.local.ConversationID()); err != nil || state.Mode != conversation.ModeStaff || state.Version != 1 {
					t.Fatalf("business execution changed reply permission: %+v %v", state, err)
				}
				malformedClaim := acquire(ref)
				if _, err := c.Exec(ctx, `UPDATE app.cart_operations SET result='{}' WHERE operation_id=$1`, ref.OperationID().String()); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, malformedClaim); !errors.Is(err, cart.ErrStoredData) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("malformed stored result: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
			case "leases-errors":
				foreign, err := cart.OpenStore(ctx, f.dsn, "a0000000-0000-4000-8000-000000000099")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(foreign.Close)
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				for _, tc := range []struct {
					name  string
					store *cart.Store
					ctx   context.Context
					ref   cart.NativeActionRef
					lease time.Duration
					want  error
				}{
					{"nil-context", f.s, nil, ref, time.Minute, cart.ErrInput},
					{"nil-store", nil, ctx, ref, time.Minute, cart.ErrInput},
					{"zero-store", &cart.Store{}, ctx, ref, time.Minute, cart.ErrInput},
					{"zero-ref", f.s, ctx, cart.NativeActionRef{}, time.Minute, cart.ErrInput},
					{"foreign-ref", foreign, ctx, ref, time.Minute, cart.ErrNotFound},
					{"zero-lease", f.s, ctx, ref, 0, cart.ErrInput},
					{"below-lease", f.s, ctx, ref, time.Millisecond - time.Nanosecond, cart.ErrInput},
					{"above-lease", f.s, ctx, ref, 5*time.Minute + time.Nanosecond, cart.ErrInput},
					{"cancel", f.s, cancelled, ref, time.Minute, context.Canceled},
					{"deadline", f.s, deadline, ref, time.Minute, context.DeadlineExceeded},
				} {
					t.Run(tc.name, func(t *testing.T) {
						before := work()
						got, ok, err := tc.store.RecoverNativeEdit(tc.ctx, tc.ref, tc.lease)
						if !errors.Is(err, tc.want) || ok || got != (cart.NativeActionClaim{}) {
							t.Fatalf("recover: %+v %v %v", got, ok, err)
						}
						unchanged(initial.Cart, before)
					})
				}
				for _, duration := range []time.Duration{-1, 0, time.Millisecond - time.Nanosecond, 5*time.Minute + time.Nanosecond} {
					before := work()
					if got, ok, err := f.s.ClaimNextNativeEdit(ctx, duration); !errors.Is(err, cart.ErrInput) || ok || got != (cart.NativeActionClaim{}) {
						t.Fatalf("discovery duration: %+v %v %v", got, ok, err)
					}
					unchanged(initial.Cart, before)
				}
				for _, duration := range []time.Duration{time.Millisecond, 5 * time.Minute} {
					if got, ok, err := f.s.RecoverNativeEdit(ctx, ref, duration); err != nil || !ok || got.Ref() != ref {
						t.Fatalf("inclusive boundary %v: %+v %v %v", duration, got, ok, err)
					}
					expire()
				}
				// Independent simultaneous callers must acquire exactly one lease.
				type acquisition struct {
					claim cart.NativeActionClaim
					ok    bool
					err   error
				}
				start := make(chan struct{})
				results := make(chan acquisition, 6)
				for range 6 {
					go func() {
						<-start
						claim, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute)
						results <- acquisition{claim, ok, err}
					}()
				}
				close(start)
				var old cart.NativeActionClaim
				winners := 0
				for range 6 {
					got := <-results
					if got.err != nil {
						t.Errorf("concurrent recovery: %v", got.err)
					}
					if got.ok {
						winners++
						old = got.claim
					} else if got.claim != (cart.NativeActionClaim{}) {
						t.Error("queue miss returned authority")
					}
				}
				if winners != 1 || old.Ref() != ref {
					t.Fatalf("lease winners=%d claim=%+v", winners, old)
				}
				before := work()
				if got, err := foreign.ExecuteNativeEdit(ctx, old); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("foreign business claim: %+v %v", got, err)
				}
				if got, err := f.s.ExecuteNativeEdit(ctx, cart.NativeActionClaim{}); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("zero business claim: %+v %v", got, err)
				}
				for _, tc := range []struct {
					store  *cart.Store
					parent context.Context
					want   error
				}{
					{nil, ctx, cart.ErrInput}, {&cart.Store{}, ctx, cart.ErrInput}, {f.s, nil, cart.ErrInput},
					{f.s, cancelled, context.Canceled}, {f.s, deadline, context.DeadlineExceeded},
				} {
					if got, ok, err := tc.store.ClaimNextNativeEdit(tc.parent, time.Minute); !errors.Is(err, tc.want) || ok || got != (cart.NativeActionClaim{}) {
						t.Fatalf("discovery invalid context/store: %+v %v %v", got, ok, err)
					}
					if got, err := tc.store.ExecuteNativeEdit(tc.parent, old); !errors.Is(err, tc.want) || !reflect.DeepEqual(got, cart.Result{}) {
						t.Fatalf("execution invalid context/store: %+v %v", got, err)
					}
					unchanged(initial.Cart, before)
				}
				unchanged(initial.Cart, before)
				expire()
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, old); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("expired execution: %+v %v", got, err)
				}
				unchanged(initial.Cart, before)
				fresh := acquire(ref)
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, old); !errors.Is(err, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("stale generation/attempt: %+v %v", got, err)
				}
				unchanged(initial.Cart, before)
				if fresh == old {
					t.Fatal("reclaim reused stale claim")
				}
				expire()
				if _, err := c.Exec(ctx, `UPDATE app.native_cart_actions SET due_at=clock_timestamp()+interval '1 hour'`); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute); err != nil || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("not due: %+v %v %v", got, ok, err)
				}
				if got, ok, err := f.s.ClaimNextNativeEdit(ctx, time.Minute); err != nil || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("not due discovery: %+v %v %v", got, ok, err)
				}
				unchanged(initial.Cart, before)
				for _, counters := range [][2]int64{{math.MaxInt64, 1}, {1, math.MaxInt64}} {
					if _, err := c.Exec(ctx, `UPDATE app.native_cart_actions SET due_at=clock_timestamp()-interval '1 second',lease_until=NULL,generation=$1,attempt=$2`, counters[0], counters[1]); err != nil {
						t.Fatal(err)
					}
					before = work()
					if got, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute); !errors.Is(err, cart.ErrLimit) || ok || got != (cart.NativeActionClaim{}) {
						t.Fatalf("counter exhaustion: %+v %v %v", got, ok, err)
					}
					if got, ok, err := f.s.ClaimNextNativeEdit(ctx, time.Minute); !errors.Is(err, cart.ErrLimit) || ok || got != (cart.NativeActionClaim{}) {
						t.Fatalf("discovery exhaustion: %+v %v %v", got, ok, err)
					}
					unchanged(initial.Cart, before)
				}
				if _, err := c.Exec(ctx, `UPDATE app.native_cart_actions SET generation=1,attempt=1; CREATE FUNCTION app.fixture_claim_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned claim commit failure'; END; $$; CREATE CONSTRAINT TRIGGER fixture_claim_commit_failure AFTER UPDATE ON app.native_cart_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.fixture_claim_commit_failure()`); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, ok, err := f.s.RecoverNativeEdit(ctx, ref, time.Minute); !errors.Is(err, cart.ErrUnavailable) || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("recover commit failure: %+v %v %v", got, ok, err)
				}
				unchanged(initial.Cart, before)
				if got, ok, err := f.s.ClaimNextNativeEdit(ctx, time.Minute); !errors.Is(err, cart.ErrUnavailable) || ok || got != (cart.NativeActionClaim{}) {
					t.Fatalf("discovery commit failure: %+v %v %v", got, ok, err)
				}
				unchanged(initial.Cart, before)
			case "edits-rollback":
				claim := acquire(ref)
				if _, err := f.cat.SetAvailability(ctx, menu, 0, false); err != nil {
					t.Fatal(err)
				}
				before := work()
				if got, err := f.s.ExecuteNativeEdit(ctx, claim); !errors.Is(err, cart.ErrUnavailableItem) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("domain rollback: %+v %v", got, err)
				}
				unchanged(initial.Cart, before)
				if _, err := f.cat.SetAvailability(ctx, menu, 1, true); err != nil {
					t.Fatal(err)
				}
				result, err := f.s.ExecuteNativeEdit(ctx, claim)
				if err != nil {
					t.Fatal(err)
				}
				note, err := cart.SetNote([]cart.UnitID{result.Cart.Units[0].ID}, "  exact\né ")
				if err != nil {
					t.Fatal(err)
				}
				refNote := admit(1, result.Cart, note)
				claimNote := acquire(refNote)
				result, err = f.s.ExecuteNativeEdit(ctx, claimNote)
				if err != nil || result.Cart.Units[0].Note != "  exact\né " {
					t.Fatalf("saved note: %+v %v", result, err)
				}
				remove, err := cart.Remove([]cart.UnitID{result.Cart.Units[0].ID})
				if err != nil {
					t.Fatal(err)
				}
				refRemove := admit(2, result.Cart, remove)
				result, err = f.s.ExecuteNativeEdit(ctx, acquire(refRemove))
				if err != nil || len(result.Cart.Units) != 0 || result.Cart.Subtotal != 0 {
					t.Fatalf("saved removal: %+v %v", result, err)
				}
				stale := admit(3, result.Cart, add(t, 1))
				staleClaim := acquire(stale)
				later := apply(t, f, result, add(t, 1))
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, staleClaim); !errors.Is(err, cart.ErrStale) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("cart CAS: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
				broken := admit(4, later.Cart, add(t, 1))
				brokenClaim := acquire(broken)
				if _, err := c.Exec(ctx, `UPDATE app.cart_execution_calls SET canonical_intent='{}' WHERE runtime_slot=4`); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, brokenClaim); !errors.Is(err, cart.ErrStoredData) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("stored canonical intent: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
				atomic := admit(5, later.Cart, add(t, 1))
				atomicClaim := acquire(atomic)
				if _, err := c.Exec(ctx, `ALTER TABLE app.native_cart_actions ADD CONSTRAINT fixture_completion_failure CHECK (runtime_slot<>5 OR committed_at IS NULL)`); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, atomicClaim); !errors.Is(err, cart.ErrUnavailable) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("completion failure: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
				commitFailure := admit(6, later.Cart, add(t, 1))
				commitClaim := acquire(commitFailure)
				// A deferred fixture error exercises the actual commit boundary.
				if _, err := c.Exec(ctx, `CREATE FUNCTION app.fixture_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned deferred commit failure'; END; $$; CREATE CONSTRAINT TRIGGER fixture_commit_failure AFTER INSERT ON app.cart_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.fixture_commit_failure()`); err != nil {
					t.Fatal(err)
				}
				before = work()
				if got, err := f.s.ExecuteNativeEdit(ctx, commitClaim); !errors.Is(err, cart.ErrUnavailable) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("commit must not pretend success: %+v %v", got, err)
				}
				unchanged(later.Cart, before)
			case "blocking-expiry":
				claim, ok, err := f.s.RecoverNativeEdit(ctx, ref, 150*time.Millisecond)
				if err != nil || !ok {
					t.Fatalf("short business lease: %v %v", ok, err)
				}
				locker := sourceConn(t, f)
				tx, err := locker.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
				if _, err := tx.Exec(ctx, `SELECT id FROM app.carts WHERE id=$1 FOR UPDATE`, initial.Cart.ID.String()); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					result cart.Result
					err    error
				}
				done := make(chan outcome, 1)
				go func() { result, err := f.s.ExecuteNativeEdit(ctx, claim); done <- outcome{result, err} }()
				executionWait(t, c, "SELECT currency,exponent,version")
				wait, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				for {
					var expired bool
					if err := c.QueryRow(wait, `SELECT clock_timestamp()>=lease_until FROM app.native_cart_actions WHERE event_id=$1`, menuClaim.Source().EventUUID()).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				got := <-done
				if !errors.Is(got.err, foundation.ErrStaleLease) || !reflect.DeepEqual(got.result, cart.Result{}) {
					t.Fatalf("fresh clock after cart wait: %+v", got)
				}
				if !reflect.DeepEqual(read(), initial.Cart) {
					t.Fatal("expired lease mutated cart")
				}
				fresh := acquire(ref)
				if got, err := f.s.ExecuteNativeEdit(ctx, fresh); err != nil || got.Cart.Version.Int64() != 1 {
					t.Fatalf("recovery after blocked expiry: %+v %v", got, err)
				}
			}
			if got, err := jobs.Inspect(ctx, event.ID); err != nil || got != jobBefore {
				t.Fatalf("consumer changed reply job/effects: %+v %v", got, err)
			}
			if got, err := f.s.ReadAdmission(ctx, f.local, event.ID); err != nil || got != admissionBefore {
				t.Fatalf("consumer changed source admission: %+v %v", got, err)
			}
		})
	}
}
