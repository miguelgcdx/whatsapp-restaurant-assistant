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

func executionWait(t *testing.T, c *pgx.Conn, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1)`, prefix+"%").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
}
func executionExpired(t *testing.T, c *pgx.Conn, job foundation.Job) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var expired bool
		if err := c.QueryRow(ctx, `SELECT clock_timestamp() >= lease_until FROM app.jobs WHERE id=$1`, job.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
	}
}

// Locks and the trusted database clock are positive observations, not sleeps or
// elapsed-time assumptions. SQL only supplies barriers and supplementary counts.
func TestExecutionBlockingAuthority(t *testing.T) {
	for _, scenario := range []string{"job", "cart", "menu", "result", "cached", "cancel-result"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			jobs, err := postgres.Open(ctx, f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer jobs.Close()
			event := foundation.Event{ID: "blocked", Sender: "fictional-sender", Text: "opaque"}
			now := time.Now().UTC()
			if _, err = jobs.Accept(ctx, event, now); err != nil {
				t.Fatal(err)
			}
			base := create(t, f)
			call, err := cart.EditCall(base.Cart.ID, base.Cart.Version, add(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.s.RegisterExecution(ctx, f.local, event.ID, 0, call)
			if err != nil {
				t.Fatal(err)
			}
			job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim=%v %v", ok, err)
			}
			expected := base.Cart
			var committed cart.Result
			if scenario == "cached" {
				committed, err = f.s.Execute(ctx, f.local, plan, job)
				if err != nil {
					t.Fatal(err)
				}
				expected = committed.Cart
			}
			gate, err := pgx.Connect(ctx, f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Close(ctx)
			observer, err := pgx.Connect(ctx, f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(ctx)
			var before string
			counts := func() string {
				var n string
				if e := observer.QueryRow(ctx, `SELECT json_build_array((SELECT count(*) FROM app.cart_operations),(SELECT count(*) FROM app.cart_units),(SELECT count(*) FROM app.cart_execution_sources),(SELECT count(*) FROM app.cart_execution_calls),(SELECT count(*) FROM app.outbox),(SELECT count(*) FROM app.send_attempts))::text`).Scan(&n); e != nil {
					t.Fatal(e)
				}
				return n
			}
			before = counts()
			if _, err = observer.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '400 milliseconds' WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			tx, err := gate.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var sql, prefix string
			switch scenario {
			case "job":
				sql = `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`
				prefix = "SELECT j.id"
			case "cart":
				sql = `SELECT id FROM app.carts WHERE id=$1 FOR UPDATE`
				prefix = "SELECT currency,exponent,version"
			case "menu":
				sql = `SELECT id FROM app.menu_items WHERE id=$1 FOR UPDATE`
				prefix = "SELECT m.price_minor"
			case "result", "cancel-result":
				sql = `LOCK TABLE app.cart_operations IN SHARE MODE`
				prefix = "INSERT INTO app.cart_operations"
			case "cached":
				sql = `LOCK TABLE app.cart_operations IN ACCESS EXCLUSIVE MODE`
				prefix = "SELECT action,fingerprint,result"
			}
			switch scenario {
			case "job":
				_, err = tx.Exec(ctx, sql, job.ID)
			case "cart":
				_, err = tx.Exec(ctx, sql, base.Cart.ID.String())
			case "menu":
				_, err = tx.Exec(ctx, sql, menu)
			default:
				_, err = tx.Exec(ctx, sql)
			}
			if err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				r cart.Result
				e error
			}
			done := make(chan outcome, 1)
			live, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() { r, e := f.s.Execute(live, f.local, plan, job); done <- outcome{r, e} }()
			executionWait(t, observer, prefix)
			if scenario == "cancel-result" {
				cancel()
			} else {
				executionExpired(t, observer, job)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got := <-done
			want := foundation.ErrStaleLease
			if scenario == "cancel-result" {
				want = cart.ErrUnavailable
			}
			if !errors.Is(got.e, want) || !reflect.DeepEqual(got.r, cart.Result{}) {
				t.Fatalf("rejected=%+v %v want %v", got.r, got.e, want)
			}
			read, err := f.s.Read(ctx, f.local, base.Cart.ID)
			if err != nil || !reflect.DeepEqual(read, expected) || counts() != before {
				t.Fatalf("rollback=%+v %v counts %s != %s", read, err, counts(), before)
			}
			// A new valid generation reuses the exact plan and original result identity.
			retry, ok, err := jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
			if err != nil || !ok {
				t.Fatalf("reclaim=%v %v", ok, err)
			}
			result, err := f.s.Execute(ctx, f.local, plan, retry)
			if err != nil || result.OperationID != plan.OperationID() || len(result.Cart.Units) != 1 || result.Cart.Version.Int64() != 1 {
				t.Fatalf("retry=%+v %v", result, err)
			}
			if scenario == "cached" && !reflect.DeepEqual(result, committed) {
				t.Fatal("cached retry changed result")
			}
			state, err := jobs.Inspect(ctx, event.ID)
			if err != nil || state.OutboxCount != 0 || state.JobCount != 1 || state.JobAttempt != 2 {
				t.Fatalf("foundation=%+v %v", state, err)
			}
		})
	}
}

func TestExecutionSimultaneousSlotAndMissingScope(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	jobs, err := postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	event := foundation.Event{ID: "simultaneous", Sender: "fictional-sender", Text: "opaque"}
	now := time.Now()
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
	type outcome struct {
		r cart.Result
		e error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; r, e := f.s.Execute(ctx, f.local, plan, job); done <- outcome{r, e} }()
	}
	close(start)
	a, b := <-done, <-done
	if a.e != nil || b.e != nil || !reflect.DeepEqual(a.r, b.r) {
		t.Fatalf("convergence=%+v %+v", a, b)
	}
	for _, local := range []cart.LocalContext{{}, func() cart.LocalContext {
		v, e := f.s.ProvisionLocal(ctx, "other", "other")
		if e != nil {
			t.Fatal(e)
		}
		return v
	}()} {
		r, e := f.s.Execute(ctx, local, plan, job)
		if e == nil || !reflect.DeepEqual(r, cart.Result{}) {
			t.Fatalf("wrong scope=%+v %v", r, e)
		}
	}
	r, e := f.s.Execute(ctx, f.local, cart.Execution{}, job)
	if e == nil || !reflect.DeepEqual(r, cart.Result{}) {
		t.Fatalf("missing handle=%+v %v", r, e)
	}
}
