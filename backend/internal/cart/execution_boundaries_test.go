package cart_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// Supplemental GREEN-only boundary probes. The main plan/claim/guard cases
// preceded implementation; these strengthen rejection and integrity evidence.
func TestExecutionBoundaryRejections(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, err := postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	observer, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	now := time.Now()
	event := foundation.Event{ID: "0", Sender: "fictional-sender", Text: "opaque"}
	if _, err = jobs.Accept(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, cart.CreateCall())
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	base, err := f.s.Execute(ctx, f.local, plan, job)
	if err != nil {
		t.Fatal(err)
	}
	const otherRestaurant = "a0000000-0000-4000-8000-000000000002"
	otherCatalog, err := catalog.OpenStore(ctx, f.dsn, otherRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer otherCatalog.Close()
	if err = otherCatalog.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, nil); err != nil {
		t.Fatal(err)
	}
	otherStore, err := cart.OpenStore(ctx, f.dsn, otherRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	foreign, err := otherStore.ProvisionLocal(ctx, "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	wrongSender, err := f.s.ProvisionLocal(ctx, "fictional-business", "wrong-sender")
	if err != nil {
		t.Fatal(err)
	}
	hashes := func() map[string]string {
		out := map[string]string{}
		for _, table := range []string{"cart_execution_sources", "cart_execution_calls", "cart_operations", "carts", "cart_units", "restaurants", "restaurant_settings", "menu_items", "staff_memberships", "operations", "conversation_sequences", "inbound_events", "jobs", "outbox", "send_attempts"} {
			var hash string
			if e := observer.QueryRow(ctx, `SELECT count(*)::text||':'||md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.`+table+` r`).Scan(&hash); e != nil {
				t.Fatal(e)
			}
			out[table] = hash
		}
		return out
	}
	unbound := foundation.Event{ID: "unbound", Sender: "fictional-sender", Text: "opaque"}
	if _, err = jobs.Accept(ctx, unbound, now); err != nil {
		t.Fatal(err)
	}
	before := hashes()
	for _, tc := range []struct {
		name string
		run  func() (cart.Execution, error)
		want error
	}{
		{"foreign restaurant", func() (cart.Execution, error) {
			return otherStore.RegisterExecution(ctx, foreign, event.ID, 1, cart.CreateCall())
		}, cart.ErrConflict},
		{"rebind wrong sender", func() (cart.Execution, error) {
			return f.s.RegisterExecution(ctx, wrongSender, event.ID, 1, cart.CreateCall())
		}, cart.ErrConflict},
		{"unbound wrong sender", func() (cart.Execution, error) {
			return f.s.RegisterExecution(ctx, wrongSender, unbound.ID, 0, cart.CreateCall())
		}, cart.ErrNotFound},
		{"missing source", func() (cart.Execution, error) {
			return f.s.RegisterExecution(ctx, f.local, "missing", 0, cart.CreateCall())
		}, cart.ErrNotFound},
		{"missing params", func() (cart.Execution, error) { return f.s.RegisterExecution(ctx, f.local, event.ID, 1, cart.Call{}) }, cart.ErrInput},
		{"negative slot", func() (cart.Execution, error) {
			return f.s.RegisterExecution(ctx, f.local, event.ID, -1, cart.CreateCall())
		}, cart.ErrInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := tc.run()
			if !errors.Is(e, tc.want) || p != (cart.Execution{}) {
				t.Fatalf("reject=%+v %v", p, e)
			}
			if !reflect.DeepEqual(before, hashes()) {
				t.Fatal("rejection changed stored data or counters")
			}
		})
	}
	if _, e := cart.ReadCall(cart.CartID{}); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e := cart.EditCall(base.Cart.ID, base.Cart.Version, nil); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e := cart.EditCall(cart.CartID{}, base.Cart.Version, add(t, 1)); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	// Finite deadline is an explicit bridge rule. No execution-time clock
	// parameter exists, including after a claim made with a caller-supplied time.
	for _, deadline := range []string{"infinity", "-infinity", "2000-01-01T00:00:00Z"} {
		if _, err = observer.Exec(ctx, `UPDATE app.jobs SET lease_until=$1::timestamptz WHERE id=$2`, deadline, job.ID); err != nil {
			t.Fatal(err)
		}
		beforeDeadline := hashes()
		r, e := f.s.Execute(ctx, f.local, plan, job)
		if !errors.Is(e, foundation.ErrStaleLease) || !reflect.DeepEqual(r, cart.Result{}) {
			t.Fatalf("deadline %s=%+v %v", deadline, r, e)
		}
		if !reflect.DeepEqual(beforeDeadline, hashes()) {
			t.Fatal("deadline rejection changed records")
		}
	}
	read, err := f.s.Read(ctx, f.local, base.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, base.Cart) {
		t.Fatal("rejection changed public snapshot", err)
	}
}

func TestExecutionReadIsFreshAndPlanIsNotFailedResult(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, err := postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	now := time.Now()
	event := foundation.Event{ID: "fresh", Sender: "fictional-sender", Text: "opaque"}
	if _, err = jobs.Accept(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	base := create(t, f)
	rc, err := cart.ReadCall(base.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, rc)
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	first, err := f.s.Execute(ctx, f.local, rp, job)
	if err != nil || !reflect.DeepEqual(first.Cart, base.Cart) {
		t.Fatal("initial read", err)
	}
	changed := apply(t, f, base, add(t, 1))
	next, err := f.s.Execute(ctx, f.local, rp, job)
	if err != nil || !reflect.DeepEqual(next.Cart, changed.Cart) {
		t.Fatalf("fresh read=%+v %v", next, err)
	}
	// A stale planned edit fails without a committed result. Its plan nevertheless
	// remains immutable; unlike direct failed-key reuse, new intent needs a slot.
	stale, err := cart.EditCall(base.Cart.ID, base.Cart.Version, add(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	sp, err := f.s.RegisterExecution(ctx, f.local, event.ID, 1, stale)
	if err != nil {
		t.Fatal(err)
	}
	if r, e := f.s.Execute(ctx, f.local, sp, job); !errors.Is(e, cart.ErrStale) || !reflect.DeepEqual(r, cart.Result{}) {
		t.Fatalf("stale plan=%+v %v", r, e)
	}
	current, err := cart.EditCall(changed.Cart.ID, changed.Cart.Version, add(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, e := f.s.RegisterExecution(ctx, f.local, event.ID, 1, current); !errors.Is(e, cart.ErrConflict) {
		t.Fatal(e)
	}
	restored, err := f.s.RegisterExecution(ctx, f.local, event.ID, 1, stale)
	if err != nil || restored != sp {
		t.Fatal("failed plan identity changed", err)
	}
	observer, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	var n int64
	if err = observer.QueryRow(ctx, `SELECT count(*) FROM app.cart_operations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("failed plan/read became ledger=%d %v", n, err)
	}
	read, err := f.s.Read(ctx, f.local, base.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, changed.Cart) {
		t.Fatal("failed plan changed cart", err)
	}
}
