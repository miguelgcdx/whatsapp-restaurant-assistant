package cart_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
)

func TestResourceAndArithmeticRejectionsAreWholeAndReusable(t *testing.T) {
	for _, price := range []int64{0, math.MaxInt64} {
		t.Run(map[bool]string{true: "free", false: "max-price"}[price == 0], func(t *testing.T) {
			f := setupWithPrice(t, price)
			r := create(t, f)
			conn, err := pgx.Connect(t.Context(), f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(t.Context())
			count := func() int64 {
				t.Helper()
				var n int64
				if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM app.cart_operations`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			for _, n := range []int64{math.MaxInt64, 4000} {
				if price != 0 {
					n = 2
				}
				before := count()
				op := operation(t)
				got, err := f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, add(t, n))
				if !errors.Is(err, cart.ErrLimit) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("count=%d got=%+v err=%v", n, got, err)
				}
				read, err := f.s.Read(t.Context(), f.local, r.Cart.ID)
				if err != nil || !reflect.DeepEqual(read, r.Cart) || count() != before {
					t.Fatalf("rejected change: %+v %v", read, err)
				}
				reused, err := f.s.Create(t.Context(), f.local, op)
				if err != nil || !reflect.DeepEqual(reused.Cart, r.Cart) {
					t.Fatalf("reuse=%+v %v", reused, err)
				}
			}
			r = apply(t, f, r, add(t, 1))
			if r.Cart.Subtotal != price {
				t.Fatalf("price=%+v", r)
			}
			if price == 0 {
				r = apply(t, f, r, add(t, 999))
				targets := make([]cart.UnitID, len(r.Cart.Units))
				for i, u := range r.Cart.Units {
					targets[i] = u.ID
				}
				a, err := cart.SetNote(targets, strings.Repeat("\n", 500))
				if err != nil {
					t.Fatal(err)
				}
				op := operation(t)
				before := count()
				got, err := f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, a)
				if !errors.Is(err, cart.ErrLimit) || !reflect.DeepEqual(got, cart.Result{}) {
					t.Fatalf("oversized note=%+v %v", got, err)
				}
				read, err := f.s.Read(t.Context(), f.local, r.Cart.ID)
				if err != nil || !reflect.DeepEqual(read, r.Cart) || count() != before {
					t.Fatalf("oversized note changed cart: %+v %v", read, err)
				}
				remove, err := cart.Remove(targets)
				if err != nil {
					t.Fatal(err)
				}
				empty, err := f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, remove)
				if err != nil || empty.Cart.Subtotal != 0 || len(empty.Cart.Units) != 0 {
					t.Fatalf("reuse rejected identity=%+v %v", empty, err)
				}
			}
		})
	}
}

func TestForeignAndUnknownLocalResources(t *testing.T) {
	f := setup(t)
	r := apply(t, f, create(t, f), add(t, 1))
	other, err := catalog.OpenStore(t.Context(), f.dsn, "a0000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.Initialize(t.Context(), catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, []catalog.Item{{ID: foreignMenu, Name: "Foreign fictional", Currency: "USD", PriceMinor: 1, Available: true}}); err != nil {
		t.Fatal(err)
	}
	otherCart, err := cart.OpenStore(t.Context(), f.dsn, "a0000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	defer otherCart.Close()
	otherLocal, err := otherCart.ProvisionLocal(t.Context(), "other", "other")
	if err != nil {
		t.Fatal(err)
	}
	otherResult, err := otherCart.Create(t.Context(), otherLocal, operation(t))
	if err != nil {
		t.Fatal(err)
	}
	foreignItem, err := cart.ParseMenuID(foreignMenu)
	if err != nil {
		t.Fatal(err)
	}
	foreignAdd, err := cart.Add(foreignItem, 1)
	if err != nil {
		t.Fatal(err)
	}
	otherResult, err = otherCart.Apply(t.Context(), otherLocal, operation(t), otherResult.Cart.ID, otherResult.Cart.Version, foreignAdd)
	if err != nil {
		t.Fatal(err)
	}
	foreignRemove, err := cart.Remove([]cart.UnitID{r.Cart.Units[0].ID, otherResult.Cart.Units[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	foreignNote, err := cart.SetNote([]cart.UnitID{r.Cart.Units[0].ID, otherResult.Cart.Units[0].ID}, "not applied")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []cart.Action{foreignAdd, foreignRemove, foreignNote} {
		op := operation(t)
		got, err := f.s.Apply(t.Context(), f.local, op, r.Cart.ID, r.Cart.Version, a)
		if !errors.Is(err, cart.ErrNotFound) || !reflect.DeepEqual(got, cart.Result{}) {
			t.Fatalf("foreign result=%+v %v", got, err)
		}
		read, err := f.s.Read(t.Context(), f.local, r.Cart.ID)
		if err != nil || !reflect.DeepEqual(read, r.Cart) {
			t.Fatalf("whole rejection=%+v %v", read, err)
		}
		if _, err = f.s.Create(t.Context(), f.local, op); err != nil {
			t.Fatalf("reuse=%v", err)
		}
	}
	unknownConversation, err := cart.ParseConversationID("d0000000-0000-4000-8000-000000000099")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []cart.ConversationID{unknownConversation, otherLocal.ConversationID()} {
		if _, err := f.s.LoadLocal(t.Context(), id, "fictional-business", "fictional-sender"); !errors.Is(err, cart.ErrNotFound) {
			t.Fatalf("load foreign/unknown=%v", err)
		}
	}
	if _, err := f.s.LoadLocal(t.Context(), f.local.ConversationID(), "wrong-business", "fictional-sender"); !errors.Is(err, cart.ErrNotFound) {
		t.Fatalf("business=%v", err)
	}
	unknownCart, err := cart.ParseCartID("d0000000-0000-4000-8000-000000000099")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []cart.CartID{unknownCart, otherResult.Cart.ID} {
		if got, err := f.s.Read(t.Context(), f.local, id); !errors.Is(err, cart.ErrNotFound) || !reflect.DeepEqual(got, cart.Snapshot{}) {
			t.Fatalf("read foreign/unknown=%+v %v", got, err)
		}
	}
	for _, raw := range []string{"", "bad", "A0000000-0000-4000-8000-000000000001"} {
		for _, parse := range []func(string) error{func(s string) error { _, err := cart.ParseCartID(s); return err }, func(s string) error { _, err := cart.ParseMenuID(s); return err }, func(s string) error { _, err := cart.ParseUnitID(s); return err }, func(s string) error { _, err := cart.ParseConversationID(s); return err }, func(s string) error { _, err := cart.ParseOperationID(s); return err }} {
			if !errors.Is(parse(raw), cart.ErrInput) {
				t.Fatalf("accepted ID %q", raw)
			}
		}
	}
}
