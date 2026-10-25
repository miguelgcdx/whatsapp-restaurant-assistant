package cart_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// Registration is explicit server-owned setup, not event-text interpretation.
func TestExecutionPlanBindingAndClaimAuthority(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, err := postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	event := foundation.Event{ID: "local-plan", Sender: "fictional-sender", Text: "opaque text, not a command"}
	now := time.Now().UTC()
	if _, err = jobs.Accept(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, cart.CreateCall())
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, cart.CreateCall())
	if err != nil || again != plan || plan.OperationID().String() == "" {
		t.Fatalf("stable plan=%+v %v", again, err)
	}
	other, err := f.s.ProvisionLocal(ctx, "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []int64{0, 1} {
		if _, err = f.s.RegisterExecution(ctx, other, event.ID, slot, cart.CreateCall()); !errors.Is(err, cart.ErrConflict) {
			t.Fatalf("whole-event rebind slot %d: %v", slot, err)
		}
	}
	if _, err = f.s.RegisterExecution(ctx, f.local, event.ID, -1, cart.CreateCall()); !errors.Is(err, cart.ErrInput) {
		t.Fatal(err)
	}
	if _, err = f.s.RegisterExecution(ctx, f.local, "missing", 0, cart.CreateCall()); !errors.Is(err, cart.ErrNotFound) {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	first, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil || first.OperationID != plan.OperationID() || len(first.Cart.Units) != 0 {
		t.Fatalf("create=%+v %v", first, err)
	}
	edit, err := cart.EditCall(first.Cart.ID, first.Cart.Version, add(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RegisterExecution(ctx, f.local, event.ID, 0, edit); !errors.Is(err, cart.ErrConflict) {
		t.Fatal(err)
	}
	ep, err := f.s.RegisterExecution(ctx, f.local, event.ID, 1, edit)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.s.Execute(ctx, f.local, ep, job)
	if err != nil || changed.Cart.Subtotal != 1300 || len(changed.Cart.Units) != 2 {
		t.Fatalf("edit=%+v %v", changed, err)
	}
	readCall, err := cart.ReadCall(first.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := f.s.RegisterExecution(ctx, f.local, event.ID, 2, readCall)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := f.s.Execute(ctx, f.local, rp, job)
	if err != nil || !reflect.DeepEqual(fresh.Cart, changed.Cart) {
		t.Fatalf("fresh=%+v %v", fresh, err)
	}
	observer, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	// Supplement public state/replay with exact counters; this is not a public
	// ledger diagnostic. A bound read must not become a mutation ledger result.
	var operations int64
	if err = observer.QueryRow(ctx, `SELECT count(*) FROM app.cart_operations`).Scan(&operations); err != nil || operations != 2 {
		t.Fatalf("read ledger=%d %v", operations, err)
	}
	assertUnchanged := func() {
		t.Helper()
		read, e := f.s.Read(ctx, f.local, first.Cart.ID)
		if e != nil || !reflect.DeepEqual(read, changed.Cart) {
			t.Fatalf("rejection changed cart=%+v %v", read, e)
		}
		var n int64
		if e = observer.QueryRow(ctx, `SELECT count(*) FROM app.cart_operations`).Scan(&n); e != nil || n != operations {
			t.Fatalf("rejection changed ledger=%d %v", n, e)
		}
	}
	// Every mutable copied claim field is compared with persisted authority, even
	// for a mutation result that already exists.
	for _, name := range []string{"id", "generation", "attempt", "event", "sender", "text"} {
		bad := job
		switch name {
		case "id":
			bad.ID = "00000000-0000-4000-8000-000000000099"
		case "generation":
			bad.Generation++
		case "attempt":
			bad.Attempt++
		case "event":
			bad.Event.ID = "other"
		case "sender":
			bad.Event.Sender = "other"
		case "text":
			bad.Event.Text = "other"
		}
		got, e := f.s.Execute(ctx, f.local, ep, bad)
		if !errors.Is(e, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
			t.Fatalf("tampered %s: %+v %v", name, got, e)
		}
		assertUnchanged()
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, e := f.s.Execute(cancelled, f.local, ep, job); e == nil || !reflect.DeepEqual(got, cart.Result{}) {
		t.Fatalf("cancelled=%+v %v", got, e)
	}
	assertUnchanged()
	// Simulated crash after cart commit: foundation recovery may use its existing
	// caller clock, but the bridge itself samples the database clock.
	reclaimed, ok, err := jobs.ClaimJob(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok || reclaimed.Generation == job.Generation {
		t.Fatalf("reclaim=%+v %v", reclaimed, err)
	}
	if got, e := f.s.Execute(ctx, f.local, ep, job); !errors.Is(e, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
		t.Fatalf("old cache=%+v %v", got, e)
	}
	replay, err := f.s.Execute(ctx, f.local, ep, reclaimed)
	if err != nil || !reflect.DeepEqual(replay, changed) {
		t.Fatalf("reclaim replay=%+v %v", replay, err)
	}
	if err = jobs.CompleteJob(ctx, reclaimed, "unchanged local acknowledgement", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, e := f.s.Execute(ctx, f.local, ep, reclaimed); !errors.Is(e, foundation.ErrStaleLease) || !reflect.DeepEqual(got, cart.Result{}) {
		t.Fatalf("completed cache=%+v %v", got, e)
	}
	stored, err := f.s.Read(ctx, f.local, first.Cart.ID)
	if err != nil || !reflect.DeepEqual(stored, changed.Cart) {
		t.Fatalf("preserved=%+v %v", stored, err)
	}
	diagnostic, err := jobs.Inspect(ctx, event.ID)
	if err != nil || diagnostic.JobCount != 1 || diagnostic.OutboxCount != 1 || diagnostic.JobState != "completed" {
		t.Fatalf("foundation=%+v %v", diagnostic, err)
	}
}
