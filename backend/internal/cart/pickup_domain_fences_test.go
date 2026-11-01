package cart_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
)

func TestPickupDomainSecondCompletionAndNoSilentExtras(t *testing.T) {
	f := pickupSetup(t)
	submitted := pickupOrdered(t, f)
	f.finish(t, f.current)
	second := f.inbound(t, "That is all, please submit")
	_, ev := f.evidence(t, second)
	second = f.register(t, second, pickupSubmitCall(t, ev))
	before, intents, snapshot := pickupEffects(t, f)
	if out, e := f.p.Submit(t.Context(), second, ev); !errors.Is(e, cart.ErrConflict) || !reflect.DeepEqual(out, cart.PickupSubmission{}) {
		t.Fatal(out, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	f.next++
	call, _ := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	second = f.register(t, second, call)
	if _, e := f.p.Prepare(t.Context(), second, f.snapshot.ID, f.snapshot.Version); !errors.Is(e, cart.ErrPickupPolicyUnresolved) {
		t.Fatal(e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	detail, e := f.p.ReadOrder(t.Context(), "menu-operator", submitted.Order.ID)
	if e != nil || !reflect.DeepEqual(detail, submitted.Order) {
		t.Fatal(detail, e)
	}
}

func TestPickupDomainExpiredClaimAndSilenceFailClosed(t *testing.T) {
	f := pickupSetup(t)
	before, intents, snapshot := pickupEffects(t, f)
	if _, fresh, e := f.s.AcceptMenu(t.Context(), f.local, foundation.Event{ID: "silence", Sender: "fictional-sender", Text: ""}, time.Now()); !errors.Is(e, cart.ErrInput) || fresh {
		t.Fatal(fresh, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	event := foundation.Event{ID: "short-lease", Sender: "fictional-sender", Text: "summary please"}
	a, fresh, e := f.s.AcceptMenu(t.Context(), f.local, event, time.Now())
	if e != nil || !fresh {
		t.Fatal(a, fresh, e)
	}
	claim, ok, e := f.s.ClaimMenu(t.Context(), f.local, 10*time.Millisecond)
	if e != nil || !ok {
		t.Fatal(claim, ok, e)
	}
	x := cart.PickupExecution{Local: f.local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: version(t, 0)}
	call, _ := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	x = f.register(t, x, call)
	<-time.After(20 * time.Millisecond)
	if out, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version); !errors.Is(e, foundation.ErrStaleLease) || !reflect.DeepEqual(out, cart.PreparedPickupSummary{}) {
		t.Fatal(out, e)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	// A fresh actual claim can use the same immutable registration, without
	// allocating another operation or inventing a caller-owned clock.
	retry, ok, e := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
	if e != nil || !ok {
		t.Fatal(retry, ok, e)
	}
	x.Claim = retry.Job()
	out, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version)
	if e != nil || out.CandidateID.String() == "" {
		t.Fatal(out, e)
	}
}

func TestPickupDomainPresentationScopeReplayAndReadBounds(t *testing.T) {
	f := pickupSetup(t)
	s, x := f.prepare(t)
	intents, e := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 1)
	if e != nil || len(intents) != 1 || intents[0].Text != s.Intent.Text {
		t.Fatal(intents, e)
	}
	r := f.present(t, s)
	again := f.present(t, s)
	if r != again {
		t.Fatal(r, again)
	}
	before, transport, snapshot := pickupEffects(t, f)
	other, e := f.s.ProvisionLocal(t.Context(), "other-business", "other-sender")
	if e != nil {
		t.Fatal(e)
	}
	in := cart.PickupLocalPresentation{IntentID: s.Intent.ID, CandidateID: s.CandidateID, ReceiverReference: "fake-receiver:" + s.Intent.ID.String(), ReceivedTextDigest: s.Intent.TextDigest}
	if _, e = f.p.RecordLocalPresentation(t.Context(), other, in); !errors.Is(e, cart.ErrNotFound) {
		t.Fatal(e)
	}
	in.ReceiverReference = "changed-reference"
	if _, e = f.p.RecordLocalPresentation(t.Context(), f.local, in); !errors.Is(e, cart.ErrConflict) {
		t.Fatal(e)
	}
	for _, limit := range []int{-1, 101} {
		if _, e = f.p.ListOrders(t.Context(), "menu-operator", cart.PickupPageRequest{Limit: limit}); !errors.Is(e, cart.ErrInput) {
			t.Fatal(e)
		}
	}
	pickupUnchanged(t, f, before, transport, snapshot)
	replay, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version)
	if e != nil || !reflect.DeepEqual(replay, s) {
		t.Fatal(replay, e)
	}
	f.finish(t, x)
}

func TestPickupDomainStaffControlCannotCreateNewQuestion(t *testing.T) {
	f := pickupSetup(t)
	x := f.inbound(t, "Show a summary")
	if _, e := f.control.SetMode(t.Context(), f.token, f.local.ConversationID(), conversation.ModeStaff, 0, "staff-control"); e != nil {
		t.Fatal(e)
	}
	x.ControlVersion = version(t, 1)
	call, _ := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	x = f.register(t, x, call)
	orders, intents, snapshot := pickupEffects(t, f)
	out, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version)
	if !errors.Is(e, cart.ErrStale) || !reflect.DeepEqual(out, cart.PreparedPickupSummary{}) {
		t.Fatalf("new conversational question under Staff control: %+v %v", out, e)
	}
	pickupUnchanged(t, f, orders, intents, snapshot)
}

func TestPickupDomainGapExpiryAfterBlockedCommit(t *testing.T) {
	f := pickupSetup(t)
	event := foundation.Event{ID: "blocked-commit", Sender: "fictional-sender", Text: "Show the final pickup summary"}
	if _, fresh, e := f.s.AcceptMenu(t.Context(), f.local, event, time.Now()); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	claim, ok, e := f.s.ClaimMenu(t.Context(), f.local, 2*time.Second)
	if e != nil || !ok {
		t.Fatal(claim, ok, e)
	}
	x := cart.PickupExecution{Local: f.local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: version(t, 0)}
	call, _ := cart.PreparePickupCall(f.snapshot.ID, f.snapshot.Version)
	x = f.register(t, x, call)
	before, intents, snapshot := pickupEffects(t, f)
	jobBefore, e := f.jobs.Inspect(t.Context(), event.ID)
	if e != nil {
		t.Fatal(e)
	}
	gate, e := f.conn.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer gate.Rollback(context.Background())
	// SELECT/replay remains possible; the real final result INSERT must wait.
	if _, e = gate.Exec(t.Context(), `LOCK TABLE app.cart_operations IN SHARE MODE`); e != nil {
		t.Fatal(e)
	}
	locker, e := pgx.Connect(t.Context(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer locker.Close(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	type outcome struct {
		result cart.PreparedPickupSummary
		err    error
	}
	done := make(chan outcome, 1)
	consumed := false
	go func() { r, err := f.p.Prepare(ctx, x, f.snapshot.ID, f.snapshot.Version); done <- outcome{r, err} }()
	defer func() {
		cancel()
		_ = gate.Rollback(context.Background())
		if !consumed {
			<-done
		}
	}()
	// Fixture-only lock handshake, not a process/pg_stat_activity inventory:
	// the uncommitted intent INSERT holds RowExclusive on this exact table.
	// Its conflict proves the pre-write claim check already passed, and the
	// result-table gate still prevents commit. No stored success data is read.
	for {
		probe, err := locker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.Exec(ctx, `LOCK TABLE app.pickup_intents IN ACCESS EXCLUSIVE MODE NOWAIT`)
		_ = probe.Rollback(context.Background())
		var lock *pgconn.PgError
		if errors.As(err, &lock) && lock.Code == "55P03" {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case early := <-done:
			consumed = true
			t.Fatalf("operation never reached blocked commit: %+v %v", early.result, early.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	// The lock handshake synchronizes progress; this timer only ages the real
	// two-second server-issued lease, without editing its deadline or counters.
	select {
	case early := <-done:
		consumed = true
		t.Fatalf("escaped held result lock: %+v %v", early.result, early.err)
	case <-time.After(2100 * time.Millisecond):
	}
	if e = gate.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	var got outcome
	select {
	case got = <-done:
		consumed = true
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(got.err, foundation.ErrStaleLease) || !reflect.DeepEqual(got.result, cart.PreparedPickupSummary{}) {
		t.Fatal(got.result, got.err)
	}
	pickupUnchanged(t, f, before, intents, snapshot)
	jobAfter, e := f.jobs.Inspect(t.Context(), event.ID)
	if e != nil || jobAfter != jobBefore {
		t.Fatal(jobBefore, jobAfter, e)
	}
	retry, ok, e := f.s.ClaimMenu(t.Context(), f.local, time.Minute)
	if e != nil || !ok {
		t.Fatal(retry, ok, e)
	}
	x.Claim = retry.Job()
	prepared, e := f.p.Prepare(t.Context(), x, f.snapshot.ID, f.snapshot.Version)
	if e != nil || prepared.CandidateID.String() == "" {
		t.Fatal(prepared, e)
	}
	actual, e := f.p.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	if e != nil || len(actual) != 1 || actual[0].Text != prepared.Intent.Text {
		t.Fatal(actual, e)
	}
}
