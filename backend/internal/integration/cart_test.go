package integration_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

// Observe an actual PostgreSQL lock wait, not elapsed time or cancellation.
// Every observed backend belongs to this test's fresh cluster.
func awaitCartLock(t *testing.T, conn *pgx.Conn, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1)`, prefix+"%").Scan(&waiting)
		if err != nil {
			t.Fatalf("observe owned lock %q: %v", prefix, err)
		}
		if waiting {
			return
		}
	}
}
func TestLocalCartCancellationRollsBackPendingResult(t *testing.T) {
	dsn := testpg.Start(t)
	ctx := t.Context()
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	const restaurant = "b0000000-0000-4000-8000-000000000001"
	const menu = "10000000-0000-4000-8000-000000000001"
	cat, err := catalog.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if err = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	s, err := cart.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := s.ProvisionLocal(ctx, "fixture-business", "fixture-sender")
	if err != nil {
		t.Fatal(err)
	}
	createOp, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Create(ctx, local, createOp)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	event := foundation.Event{ID: "cancel-cart-event", Sender: "fictional", Text: "unchanged"}
	if _, err = legacy.Accept(ctx, event, time.Now()); err != nil {
		t.Fatal(err)
	}
	beforeEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close(ctx)
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	tx, err := gate.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Block only the result insertion, after the domain statements have run.
	if _, err = tx.Exec(ctx, `LOCK TABLE app.cart_operations IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	op, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	mid, err := cart.ParseMenuID(menu)
	if err != nil {
		t.Fatal(err)
	}
	action, err := cart.Add(mid, 2)
	if err != nil {
		t.Fatal(err)
	}
	live, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		result, err := s.Apply(live, local, op, base.Cart.ID, base.Cart.Version, action)
		if !reflect.DeepEqual(result, cart.Result{}) {
			finished <- errors.New("cancelled mutation returned a result")
			return
		}
		finished <- err
	}()
	awaitCartLock(t, observer, "INSERT INTO app.cart_operations")
	cancel()
	if err = <-finished; !errors.Is(err, cart.ErrUnavailable) {
		t.Fatalf("cancel=%v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	read, err := s.Read(ctx, local, base.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, base.Cart) {
		t.Fatalf("rollback=%+v %v", read, err)
	}
	var operations, units int64
	if err = observer.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.cart_operations),(SELECT count(*) FROM app.cart_units)`).Scan(&operations, &units); err != nil || operations != 1 || units != 0 {
		t.Fatalf("rollback ledger=%d units=%d err=%v", operations, units, err)
	}
	afterEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil || !reflect.DeepEqual(afterEvent, beforeEvent) {
		t.Fatalf("outbound delta=%+v %v", afterEvent, err)
	}
	retried, err := s.Apply(ctx, local, op, base.Cart.ID, base.Cart.Version, action)
	if err != nil || len(retried.Cart.Units) != 2 || retried.Cart.Version.Int64() != 1 {
		t.Fatalf("reuse=%+v %v", retried, err)
	}
}

func TestLocalCartAvailabilityWriterOrdering(t *testing.T) {
	for _, staffPath := range []bool{false, true} {
		for _, cartFirst := range []bool{false, true} {
			name := "legacy"
			if staffPath {
				name = "staff"
			}
			if cartFirst {
				name += "/cart-first"
			} else {
				name += "/writer-first"
			}
			t.Run(name, func(t *testing.T) {
				dsn := testpg.Start(t)
				ctx := t.Context()
				if err := postgres.Migrate(ctx, dsn); err != nil {
					t.Fatal(err)
				}
				const restaurant = "b0000000-0000-4000-8000-000000000001"
				const menu = "10000000-0000-4000-8000-000000000001"
				cat, err := catalog.OpenStore(ctx, dsn, restaurant)
				if err != nil {
					t.Fatal(err)
				}
				defer cat.Close()
				if err = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
					t.Fatal(err)
				}
				members, err := staff.OpenStore(ctx, dsn, restaurant)
				if err != nil {
					t.Fatal(err)
				}
				defer members.Close()
				if ok, err := members.EnsureMember(ctx, "fixture-staff"); err != nil || !ok {
					t.Fatalf("member=%v %v", ok, err)
				}
				s, err := cart.OpenStore(ctx, dsn, restaurant)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				local, err := s.ProvisionLocal(ctx, "fixture-business", "fixture-sender")
				if err != nil {
					t.Fatal(err)
				}
				op, err := cart.NewOperationID()
				if err != nil {
					t.Fatal(err)
				}
				base, err := s.Create(ctx, local, op)
				if err != nil {
					t.Fatal(err)
				}
				mid, err := cart.ParseMenuID(menu)
				if err != nil {
					t.Fatal(err)
				}
				a, err := cart.Add(mid, 1)
				if err != nil {
					t.Fatal(err)
				}
				retainedOp, err := cart.NewOperationID()
				if err != nil {
					t.Fatal(err)
				}
				base, err = s.Apply(ctx, local, retainedOp, base.Cart.ID, base.Cart.Version, a)
				if err != nil {
					t.Fatal(err)
				}
				note, err := cart.SetNote([]cart.UnitID{base.Cart.Units[0].ID}, " exact retained 🥪\n")
				if err != nil {
					t.Fatal(err)
				}
				op, err = cart.NewOperationID()
				if err != nil {
					t.Fatal(err)
				}
				base, err = s.Apply(ctx, local, op, base.Cart.ID, base.Cart.Version, note)
				if err != nil {
					t.Fatal(err)
				}
				addOp, err := cart.NewOperationID()
				if err != nil {
					t.Fatal(err)
				}
				gate, err := pgx.Connect(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Close(ctx)
				observer, err := pgx.Connect(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer observer.Close(ctx)
				tx, err := gate.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `SELECT id FROM app.menu_items WHERE id=$1 FOR UPDATE`, menu); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					result cart.Result
					err    error
				}
				added := make(chan outcome, 1)
				written := make(chan error, 1)
				launchAdd := func() {
					go func() { r, e := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, a); added <- outcome{r, e} }()
				}
				launchWriter := func() {
					go func() {
						if staffPath {
							_, err := cat.ApplyAvailability(ctx, catalog.AvailabilityIntent{Subject: "fixture-staff", Key: "fixture-availability", RequestID: "fixture-request", ItemID: menu, ExpectedVersion: 0, Value: false}, members.Access)
							written <- err
						} else {
							_, err := cat.SetAvailability(ctx, menu, 0, false)
							written <- err
						}
					}()
				}
				if cartFirst {
					launchAdd()
					awaitCartLock(t, observer, "SELECT m.price_minor")
					launchWriter()
					if staffPath {
						awaitCartLock(t, observer, "SELECT id FROM app.restaurants")
					} else {
						awaitCartLock(t, observer, "UPDATE app.menu_items SET available")
					}
				} else {
					launchWriter()
					awaitCartLock(t, observer, "UPDATE app.menu_items SET available")
					launchAdd()
					if staffPath {
						awaitCartLock(t, observer, "SELECT id::text FROM app.restaurants")
					} else {
						awaitCartLock(t, observer, "SELECT m.price_minor")
					}
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				result := <-added
				if err = <-written; err != nil {
					t.Fatal(err)
				}
				if cartFirst {
					if result.err != nil || len(result.result.Cart.Units) != 2 || result.result.Cart.Subtotal != 1300 {
						t.Fatalf("cart-first=%+v err=%v", result.result, result.err)
					}
				} else {
					if !errors.Is(result.err, cart.ErrUnavailableItem) || !reflect.DeepEqual(result.result, cart.Result{}) {
						t.Fatalf("writer-first=%+v err=%v", result.result, result.err)
					}
				}
				current, err := s.Read(ctx, local, base.Cart.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.Units[0] != base.Cart.Units[0] || !strings.Contains(current.Units[0].Note, "retained") {
					t.Fatalf("unavailable unit lost=%+v", current)
				}
				before := current
				// A failed availability rejection leaves the same identity reusable.
				retry := addOp
				if cartFirst {
					retry, err = cart.NewOperationID()
					if err != nil {
						t.Fatal(err)
					}
				}
				failed, err := s.Apply(ctx, local, retry, current.ID, current.Version, a)
				if !errors.Is(err, cart.ErrUnavailableItem) || !reflect.DeepEqual(failed, cart.Result{}) {
					t.Fatalf("unavailable=%+v %v", failed, err)
				}
				remove, err := cart.Remove([]cart.UnitID{current.Units[0].ID})
				if err != nil {
					t.Fatal(err)
				}
				changed, err := s.Apply(ctx, local, retry, current.ID, current.Version, remove)
				if err != nil || changed.Cart.Version.Int64() != before.Version.Int64()+1 {
					t.Fatalf("reuse=%+v %v", changed, err)
				}
				if cartFirst {
					replay, err := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, a)
					if err != nil || !reflect.DeepEqual(replay, result.result) {
						t.Fatalf("historical=%+v %v", replay, err)
					}
				}
			})
		}
	}
}
