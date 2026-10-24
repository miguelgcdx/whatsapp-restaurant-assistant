package cart_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "a0000000-0000-4000-8000-000000000001"
const menu = "10000000-0000-4000-8000-000000000001"
const foreignMenu = "10000000-0000-4000-8000-000000000099"

type fixture struct {
	s     *cart.Store
	cat   *catalog.Store
	local cart.LocalContext
	dsn   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	return setupWithPrice(t, 650)
}
func setupWithPrice(t *testing.T, price int64) fixture {
	t.Helper()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cat.Close)
	if err = cat.Initialize(t.Context(), catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional sandwich", PriceMinor: price, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	s, err := cart.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	local, err := s.ProvisionLocal(t.Context(), "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{s, cat, local, dsn}
}
func operation(t *testing.T) cart.OperationID {
	t.Helper()
	id, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func version(t *testing.T, v int64) cart.Version {
	t.Helper()
	got, err := cart.ParseVersion(v)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func item(t *testing.T) cart.MenuID {
	t.Helper()
	got, err := cart.ParseMenuID(menu)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func add(t *testing.T, count int64) cart.Action {
	t.Helper()
	a, err := cart.Add(item(t), count)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func create(t *testing.T, f fixture) cart.Result {
	t.Helper()
	r, err := f.s.Create(t.Context(), f.local, operation(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func apply(t *testing.T, f fixture, r cart.Result, a cart.Action) cart.Result {
	t.Helper()
	got, err := f.s.Apply(t.Context(), f.local, operation(t), r.Cart.ID, r.Cart.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPersistentDraftUnitIdentityAndHistoricalReplay(t *testing.T) {
	f := setup(t)
	if _, err := f.s.Read(t.Context(), f.local, cart.CartID{}); !errors.Is(err, cart.ErrInput) {
		t.Fatalf("read invented draft: %v", err)
	}
	op := operation(t)
	first, err := f.s.Create(t.Context(), f.local, op)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cart.Version.Int64() != 0 || first.Cart.Subtotal != 0 || len(first.Cart.Units) != 0 {
		t.Fatalf("create=%+v", first)
	}
	r := apply(t, f, first, add(t, 2))
	ids := []cart.UnitID{r.Cart.Units[0].ID, r.Cart.Units[1].ID}
	note := "  exact 🥪\n"
	action, err := cart.SetNote(ids[:1], note)
	if err != nil {
		t.Fatal(err)
	}
	noteOp := operation(t)
	noted, err := f.s.Apply(t.Context(), f.local, noteOp, r.Cart.ID, r.Cart.Version, action)
	if err != nil {
		t.Fatal(err)
	}
	r = apply(t, f, noted, add(t, 1))
	remove, err := cart.Remove(ids[1:])
	if err != nil {
		t.Fatal(err)
	}
	r = apply(t, f, r, remove)
	if len(r.Cart.Units) != 2 || r.Cart.Units[0].ID != ids[0] || r.Cart.Units[0].Note != note || r.Cart.Subtotal != 1300 || r.Cart.Version.Int64() != 4 {
		t.Fatalf("edited=%+v", r)
	}
	for _, u := range r.Cart.Units {
		if u.PriceMinor != 650 || u.Currency != "USD" || u.Exponent != 2 {
			t.Fatalf("snapshot=%+v", u)
		}
	}
	replay, err := f.s.Apply(t.Context(), f.local, noteOp, noted.Cart.ID, version(t, 1), action)
	if err != nil || !reflect.DeepEqual(replay, noted) {
		t.Fatalf("historical replay=%+v err=%v", replay, err)
	}
	again, err := f.s.Create(t.Context(), f.local, op)
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("create replay=%+v %v", again, err)
	}
	current, err := f.s.Create(t.Context(), f.local, operation(t))
	if err != nil || !reflect.DeepEqual(current.Cart, r.Cart) {
		t.Fatalf("one draft=%+v %v", current, err)
	}
	reopened, err := cart.OpenStore(t.Context(), f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.LoadLocal(t.Context(), f.local.ConversationID(), "fictional-business", "fictional-sender")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(t.Context(), restored, r.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, r.Cart) {
		t.Fatalf("reopen=%+v %v", read, err)
	}
	targets := []cart.UnitID{r.Cart.Units[0].ID, r.Cart.Units[1].ID}
	remove, err = cart.Remove(targets)
	if err != nil {
		t.Fatal(err)
	}
	r = apply(t, f, r, remove)
	if len(r.Cart.Units) != 0 || r.Cart.Subtotal != 0 || r.Cart.Version.Int64() != 5 {
		t.Fatalf("empty=%+v", r)
	}
}

func TestRejectedRequestsRollbackAndOperationReuse(t *testing.T) {
	f := setup(t)
	r := apply(t, f, create(t, f), add(t, 2))
	conn, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	counters := func() string {
		t.Helper()
		var s string
		err := conn.QueryRow(t.Context(), `SELECT json_build_array((SELECT count(*) FROM app.cart_operations),(SELECT count(*) FROM app.cart_units),(SELECT count(*) FROM app.inbound_events),(SELECT count(*) FROM app.jobs),(SELECT count(*) FROM app.outbox))::text`).Scan(&s)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	missing, err := cart.ParseUnitID("20000000-0000-4000-8000-000000000099")
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := cart.ParseMenuID(foreignMenu)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.ProvisionLocal(t.Context(), "other-business", "other-sender")
	if err != nil {
		t.Fatal(err)
	}
	foreign := create(t, fixture{s: f.s, local: other})
	for _, tc := range []struct {
		name string
		run  func(cart.OperationID) (cart.Result, error)
		want error
	}{
		{"stale", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, version(t, 0), add(t, 1))
		}, cart.ErrStale},
		{"overflow version", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, version(t, math.MaxInt64), add(t, 1))
		}, cart.ErrStale},
		{"unknown menu", func(op cart.OperationID) (cart.Result, error) {
			a, e := cart.Add(unknown, 1)
			if e != nil {
				t.Fatal(e)
			}
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, a)
		}, cart.ErrNotFound},
		{"missing target whole request", func(op cart.OperationID) (cart.Result, error) {
			a, e := cart.Remove([]cart.UnitID{r.Cart.Units[0].ID, missing})
			if e != nil {
				t.Fatal(e)
			}
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, a)
		}, cart.ErrNotFound},
		{"foreign cart", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), f.local, op, foreign.Cart.ID, r.Cart.Version, add(t, 1))
		}, cart.ErrNotFound},
		{"nil action", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, nil)
		}, cart.ErrInput},
		{"zero local context", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), cart.LocalContext{}, op, r.Cart.ID, r.Cart.Version, add(t, 1))
		}, cart.ErrInput},
		{"huge count", func(op cart.OperationID) (cart.Result, error) {
			return f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, add(t, math.MaxInt64))
		}, cart.ErrLimit},
		{"cancelled", func(op cart.OperationID) (cart.Result, error) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return f.s.Apply(ctx, f.local, op, r.Cart.ID, r.Cart.Version, add(t, 1))
		}, cart.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counters()
			op := operation(t)
			got, err := tc.run(op)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, cart.Result{}) {
				t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
			}
			read, err := f.s.Read(t.Context(), f.local, r.Cart.ID)
			if err != nil || !reflect.DeepEqual(read, r.Cart) || counters() != before {
				t.Fatalf("rejection changed state: read=%+v err=%v", read, err)
			}
			reused, err := f.s.Create(t.Context(), f.local, op)
			if err != nil || !reflect.DeepEqual(reused.Cart, r.Cart) {
				t.Fatalf("rejected identity not reusable: %+v %v", reused, err)
			}
		})
	}
	for _, ids := range [][]cart.UnitID{nil, {r.Cart.Units[0].ID, r.Cart.Units[0].ID}, {cart.UnitID{}}} {
		if _, err := cart.Remove(ids); !errors.Is(err, cart.ErrInput) {
			t.Fatalf("targets=%v err=%v", ids, err)
		}
	}
	for _, note := range []string{strings.Repeat("🥪", 501), "a\x00b", "\xff"} {
		if _, err := cart.SetNote([]cart.UnitID{r.Cart.Units[0].ID}, note); !errors.Is(err, cart.ErrInput) {
			t.Fatalf("note accepted %v", err)
		}
	}
	for _, n := range []int64{0, -1} {
		if _, err := cart.Add(item(t), n); !errors.Is(err, cart.ErrInput) {
			t.Fatalf("count=%d %v", n, err)
		}
	}
	if _, err := cart.ParseVersion(-1); !errors.Is(err, cart.ErrInput) {
		t.Fatal(err)
	}
	boundary, err := cart.SetNote([]cart.UnitID{r.Cart.Units[0].ID}, strings.Repeat("🥪", 500))
	if err != nil {
		t.Fatal(err)
	}
	r = apply(t, f, r, boundary)
	if r.Cart.Units[0].Note != strings.Repeat("🥪", 500) || r.Cart.Subtotal != 1300 {
		t.Fatal("note boundary changed")
	}
}

func TestConcurrentCreationAndEdits(t *testing.T) {
	f := setup(t)
	op := operation(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]cart.Result, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Go(func() { <-start; results[i], errs[i] = f.s.Create(t.Context(), f.local, op) })
	}
	close(start)
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !reflect.DeepEqual(results[i], results[0]) {
			t.Fatalf("create[%d]=%+v %v", i, results[i], errs[i])
		}
	}
	base := results[0]
	run := func(sameID, contradict bool) {
		t.Helper()
		start = make(chan struct{})
		a := add(t, 1)
		b := a
		if contradict {
			b = add(t, 2)
		}
		o1, o2 := operation(t), operation(t)
		if sameID {
			o2 = o1
		}
		actions := []cart.Action{a, b}
		ops := []cart.OperationID{o1, o2}
		for i := 0; i < 2; i++ {
			wg.Go(func() {
				<-start
				results[i], errs[i] = f.s.Apply(t.Context(), f.local, ops[i], base.Cart.ID, base.Cart.Version, actions[i])
			})
		}
		close(start)
		wg.Wait()
		wins := 0
		for i := 0; i < 2; i++ {
			if errs[i] == nil {
				wins++
				base = results[i]
			} else if sameID && contradict {
				if !errors.Is(errs[i], cart.ErrConflict) {
					t.Fatal(errs[i])
				}
			} else if !errors.Is(errs[i], cart.ErrStale) {
				t.Fatal(errs[i])
			}
		}
		want := 1
		if sameID && !contradict {
			want = 2
			if !reflect.DeepEqual(results[0], results[1]) {
				t.Fatal("duplicate IDs changed")
			}
		}
		if wins != want {
			t.Fatalf("wins=%d err=%v", wins, errs[:2])
		}
	}
	run(true, false)
	run(true, true)
	run(false, false)
	if base.Cart.Version.Int64() != 3 {
		t.Fatalf("version=%v", base.Cart.Version)
	}
}

func TestFreshOwnershipBeforeReplayAndCorruptResults(t *testing.T) {
	f := setup(t)
	op := operation(t)
	r, err := f.s.Create(t.Context(), f.local, op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, add(t, 1)); !errors.Is(err, cart.ErrConflict) {
		t.Fatalf("action mismatch=%v", err)
	}
	conn, err := pgx.Connect(t.Context(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err = conn.Exec(t.Context(), `UPDATE app.cart_conversations SET sender='changed' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Create(t.Context(), f.local, op); !errors.Is(err, cart.ErrNotFound) {
		t.Fatalf("ownership after replay=%v", err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE app.cart_conversations SET sender='fictional-sender' WHERE id=$1`, f.local.ConversationID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE app.cart_operations SET result='{}' WHERE operation_id=$1`, op.String()); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Create(t.Context(), f.local, op)
	if !errors.Is(err, cart.ErrStoredData) || !reflect.DeepEqual(got, cart.Result{}) {
		t.Fatalf("corrupt replay=%+v %v", got, err)
	}
	read, err := f.s.Read(t.Context(), f.local, r.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, r.Cart) {
		t.Fatalf("corruption changed domain %+v %v", read, err)
	}
}
