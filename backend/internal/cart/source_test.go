package cart_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

func sourceJobs(t *testing.T, f fixture, id, sender string) (*postgres.Store, foundation.Job, string) {
	t.Helper()
	jobs, err := postgres.Open(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jobs.Close)
	event := foundation.Event{ID: id, Sender: sender, Text: "explicit fixture text"}
	now := time.Now().UTC()
	if _, err = jobs.Accept(t.Context(), event, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	c := sourceConn(t, f)
	var eid string
	if err = c.QueryRow(t.Context(), `SELECT id::text FROM app.inbound_events WHERE provider_event_id=$1`, id).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	return jobs, job, eid
}
func sourceConn(t *testing.T, f fixture) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}
func sourceCounts(t *testing.T, c *pgx.Conn) string {
	t.Helper()
	var s string
	err := c.QueryRow(t.Context(), `SELECT json_build_array((SELECT count(*) FROM app.cart_execution_sources),(SELECT count(*) FROM app.cart_execution_calls),(SELECT count(*) FROM app.cart_operations),(SELECT count(*) FROM app.carts),(SELECT count(*) FROM app.jobs),(SELECT count(*) FROM app.outbox),(SELECT count(*) FROM app.send_attempts))::text`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSourceBindingGettersRetryReopenAndNoAllocation(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, job, eid := sourceJobs(t, f, "source-exact", "fictional-sender")
	c := sourceConn(t, f)
	before := sourceCounts(t, c)
	source, err := f.s.BindSource(ctx, f.local, job.Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.RestaurantID() != restaurant || source.ConversationID() != f.local.ConversationID() || source.EventUUID() != eid || source.EventProviderID() != job.Event.ID || source.Business() != "fictional-business" || source.Sender() != "fictional-sender" {
		t.Fatalf("getters: %+v", source)
	}
	again, err := f.s.BindSource(ctx, f.local, job.Event.ID)
	if err != nil || again != source {
		t.Fatalf("retry: %+v %v", again, err)
	}
	reopened, err := cart.OpenStore(ctx, f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	local, err := reopened.LoadLocal(ctx, source.ConversationID(), source.Business(), source.Sender())
	if err != nil {
		t.Fatal(err)
	}
	again, err = reopened.BindSource(ctx, local, job.Event.ID)
	if err != nil || again != source {
		t.Fatalf("reopen: %+v %v", again, err)
	}
	if before != "[0, 0, 0, 0, 1, 0, 0]" || sourceCounts(t, c) != "[1, 0, 0, 0, 1, 0, 0]" {
		t.Fatalf("allocation before=%s after=%s", before, sourceCounts(t, c))
	}
	diagnostic, err := jobs.Inspect(ctx, job.Event.ID)
	if err != nil || diagnostic.JobState != "leased" || diagnostic.JobAttempt != 1 || diagnostic.OutboxCount != 0 {
		t.Fatalf("inspect: %+v %v", diagnostic, err)
	}
	plan, err := f.s.RegisterExecution(ctx, f.local, job.Event.ID, 0, cart.CreateCall())
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
}

func TestSourceBindingRejectionsPreservePublicState(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, job, _ := sourceJobs(t, f, "bound", "fictional-sender")
	source, err := f.s.BindSource(ctx, f.local, job.Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.RegisterExecution(ctx, f.local, job.Event.ID, 0, cart.CreateCall())
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil {
		t.Fatal(err)
	}
	same, err := f.s.ProvisionLocal(ctx, "another-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := f.s.ProvisionLocal(ctx, "another-business", "wrong-sender")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Accept(ctx, foundation.Event{ID: "wrong-sender-event", Sender: "wrong-sender", Text: "fixture"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := sourceConn(t, f)
	before := sourceCounts(t, c)
	diag, err := jobs.Inspect(ctx, job.Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	// Err deliberately has not published the elapsed deadline yet.
	late := sourceLateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}
	var nilStore *cart.Store
	cases := []struct {
		name  string
		store *cart.Store
		ctx   context.Context
		local cart.LocalContext
		event string
		want  error
	}{
		{"nil-context", f.s, nil, f.local, "bound", cart.ErrInput},
		{"nil-store", nilStore, ctx, f.local, "bound", cart.ErrInput},
		{"zero-store", &cart.Store{}, ctx, f.local, "bound", cart.ErrInput},
		{"zero-local", f.s, ctx, cart.LocalContext{}, "bound", cart.ErrInput},
		{"empty", f.s, ctx, f.local, "", cart.ErrInput},
		{"over-bound", f.s, ctx, f.local, strings.Repeat("x", 129), cart.ErrInput},
		{"invalid-text", f.s, ctx, f.local, "x\x00", cart.ErrInput},
		{"missing", f.s, ctx, f.local, "missing", cart.ErrNotFound},
		{"wrong-sender-new", f.s, ctx, f.local, "wrong-sender-event", cart.ErrNotFound},
		{"foreign", f.s, ctx, same, "bound", cart.ErrConflict},
		{"conflict-before-sender", f.s, ctx, wrong, "bound", cart.ErrConflict},
		{"cancel", f.s, cancelled, f.local, "bound", context.Canceled},
		{"deadline", f.s, expired, f.local, "bound", context.DeadlineExceeded},
		{"deadline-unpublished", f.s, late, f.local, "bound", context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, e := tc.store.BindSource(tc.ctx, tc.local, tc.event)
			if !errors.Is(e, tc.want) || got != (cart.Source{}) {
				t.Fatalf("source=%+v error=%v want=%v", got, e, tc.want)
			}
		})
	}
	// Legacy registration's validation still precedes source conflicts.
	if _, err = f.s.RegisterExecution(ctx, wrong, "bound", -1, cart.Call{}); !errors.Is(err, cart.ErrInput) {
		t.Fatal(err)
	}
	if _, err = f.s.RegisterExecution(ctx, wrong, "bound", 0, cart.CreateCall()); !errors.Is(err, cart.ErrConflict) {
		t.Fatal(err)
	}
	if sourceCounts(t, c) != before {
		t.Fatalf("effects %s != %s", sourceCounts(t, c), before)
	}
	read, err := f.s.Read(ctx, f.local, result.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, result.Cart) {
		t.Fatalf("read: %+v %v", read, err)
	}
	replay, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil || !reflect.DeepEqual(replay, result) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	after, err := jobs.Inspect(ctx, job.Event.ID)
	if err != nil || !reflect.DeepEqual(after, diag) {
		t.Fatalf("inspect: %+v %v", after, err)
	}
	retry, err := f.s.BindSource(ctx, f.local, job.Event.ID)
	if err != nil || retry != source {
		t.Fatal(err)
	}
	// Exact maximum provider key remains valid.
	max := strings.Repeat("m", 128)
	if _, err = jobs.Accept(ctx, foundation.Event{ID: max, Sender: "fictional-sender", Text: "fixture"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, e := f.s.BindSource(ctx, f.local, max); e != nil || got.EventProviderID() != max {
		t.Fatalf("max: %+v %v", got, e)
	}
}

type sourceLateDeadline struct {
	context.Context
	end time.Time
}

func (c sourceLateDeadline) Deadline() (time.Time, bool) { return c.end, true }

func TestSourceBindingContextBoundsAndStoredIdentity(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	_, job, _ := sourceJobs(t, f, "context", "fictional-sender")
	c := sourceConn(t, f)
	gate := sourceConn(t, f)
	tx, err := gate.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		s cart.Source
		e error
	}
	// Both observable cancellation and an earlier caller deadline retain their
	// identity while the unchanged begin path is positively blocked.
	for _, kind := range []string{"cancel", "earlier-deadline"} {
		live, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		done := make(chan outcome, 1)
		go func() { s, e := f.s.BindSource(live, f.local, job.Event.ID); done <- outcome{s, e} }()
		executionWait(t, c, "SELECT id::text FROM app.restaurants")
		want := context.DeadlineExceeded
		if kind == "cancel" {
			cancel()
			want = context.Canceled
		}
		got := <-done
		cancel()
		if !errors.Is(got.e, want) || got.s != (cart.Source{}) {
			t.Fatalf("%s: %+v %v", kind, got.s, got.e)
		}
	}
	done := make(chan outcome, 1)
	go func() { s, e := f.s.BindSource(ctx, f.local, job.Event.ID); done <- outcome{s, e} }()
	executionWait(t, c, "SELECT id::text FROM app.restaurants")
	got := <-done
	if !errors.Is(got.e, context.DeadlineExceeded) || got.s != (cart.Source{}) {
		t.Fatalf("5s bound: %+v %v", got.s, got.e)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// A formerly valid handle is not a substitute for current stored identities.
	if _, err = c.Exec(ctx, `UPDATE app.cart_conversations SET sender='changed' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
		t.Fatal(err)
	}
	if s, e := f.s.BindSource(ctx, f.local, job.Event.ID); !errors.Is(e, cart.ErrNotFound) || s != (cart.Source{}) {
		t.Fatalf("stored identity: %+v %v", s, e)
	}
	if sourceCounts(t, c) != "[0, 0, 0, 0, 1, 0, 0]" {
		t.Fatal(sourceCounts(t, c))
	}
}

func TestSourceBindingRegistrationRacesAndRollback(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, job, _ := sourceJobs(t, f, "race", "fictional-sender")
	type outcome struct {
		s cart.Source
		p cart.Execution
		e error
	}
	start := make(chan struct{})
	done := make(chan outcome, 3)
	go func() { <-start; s, e := f.s.BindSource(ctx, f.local, job.Event.ID); done <- outcome{s: s, e: e} }()
	for range 2 {
		go func() {
			<-start
			p, e := f.s.RegisterExecution(ctx, f.local, job.Event.ID, 7, cart.CreateCall())
			done <- outcome{p: p, e: e}
		}()
	}
	close(start)
	var plans []cart.Execution
	for range 3 {
		got := <-done
		if got.e != nil {
			t.Fatal(got.e)
		}
		if got.p != (cart.Execution{}) {
			plans = append(plans, got.p)
		}
	}
	if len(plans) != 2 || plans[0] != plans[1] {
		t.Fatalf("slot convergence: %+v", plans)
	}
	a, err := f.s.Execute(ctx, f.local, plans[0], job)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.Execute(ctx, f.local, plans[1], job)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("result convergence: %+v %v", b, err)
	}
	other, err := f.s.ProvisionLocal(ctx, "foreign", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh event contested through both APIs has exactly one whole-event owner.
	c := sourceConn(t, f)
	if sourceCounts(t, c) != "[1, 1, 1, 1, 1, 0, 0]" {
		t.Fatal(sourceCounts(t, c))
	}
	if _, err = jobs.Accept(ctx, foundation.Event{ID: "contested", Sender: "fictional-sender", Text: "fixture"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	done = make(chan outcome, 2)
	go func() { <-start; s, e := f.s.BindSource(ctx, other, "contested"); done <- outcome{s: s, e: e} }()
	go func() {
		<-start
		p, e := f.s.RegisterExecution(ctx, f.local, "contested", 0, cart.CreateCall())
		done <- outcome{p: p, e: e}
	}()
	close(start)
	one, two := <-done, <-done
	if (one.e == nil) == (two.e == nil) || (one.e != nil && !errors.Is(one.e, cart.ErrConflict)) || (two.e != nil && !errors.Is(two.e, cart.ErrConflict)) {
		t.Fatalf("whole event: %+v %+v", one, two)
	}
	wantCounts := "[2, 2, 1, 1, 2, 0, 0]"
	winner := one
	if winner.e != nil {
		winner = two
	}
	if winner.s != (cart.Source{}) {
		wantCounts = "[2, 1, 1, 1, 2, 0, 0]"
	}
	if sourceCounts(t, c) != wantCounts {
		t.Fatalf("race effects: %s want %s", sourceCounts(t, c), wantCounts)
	}
	// Force a real post-source registration failure, not an invalid top-level call.
	if _, err = jobs.Accept(ctx, foundation.Event{ID: "rollback", Sender: "fictional-sender", Text: "fixture"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := sourceCounts(t, c)
	if _, err = c.Exec(ctx, `ALTER TABLE app.cart_execution_calls ADD CONSTRAINT fixture_reject_slot CHECK(runtime_slot<>99)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RegisterExecution(ctx, f.local, "rollback", 99, cart.CreateCall()); !errors.Is(err, cart.ErrUnavailable) {
		t.Fatal(err)
	}
	if sourceCounts(t, c) != before {
		t.Fatalf("partial source: %s != %s", sourceCounts(t, c), before)
	}
	// The failed registration did not reserve ownership in a separate transaction.
	if _, err = f.s.BindSource(ctx, other, "rollback"); err != nil {
		t.Fatal(err)
	}
}
