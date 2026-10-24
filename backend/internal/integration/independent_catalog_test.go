package integration_test

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/pricing"
	"restaurant-assistant/backend/internal/testpg"
)

// S1-S4: independent empty configuration, immutable retry, rejected-state hashes,
// and a non-USD/non-default settings mapping through exported boundaries.
func independentSpecProbes(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.OpenStore(t.Context(), dsn, "90000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := catalog.Settings{Currency: "EUR", CurrencyExponent: 6, FlatFeeMinor: 17, Open: false}
	menu := []catalog.Item{
		{ID: "90000000-0000-4000-8000-000000000002", Name: "Zero", Currency: "EUR", Available: true},
		{ID: "90000000-0000-4000-8000-000000000003", Name: "Fixed", Currency: "EUR", PriceMinor: 23, Available: true},
	}
	if err := store.Initialize(t.Context(), cfg, menu); err != nil {
		t.Fatal(err)
	}
	changed, err := store.SetAvailability(t.Context(), menu[1].ID, 0, false)
	if err != nil || changed.Version != 1 || changed.Available {
		t.Fatalf("update: %+v %v", changed, err)
	}
	snapshot := func() [32]byte {
		t.Helper()
		c, e := store.Settings(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		m, e := store.Menu(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(struct {
			Settings catalog.Settings
			Menu     []catalog.Item
		}{c, m})
		if e != nil {
			t.Fatal(e)
		}
		return sha256.Sum256(b)
	}
	before := snapshot()
	retry := []catalog.Item{menu[1], menu[0]}
	retry[0].Version = 45
	if err := store.Initialize(t.Context(), cfg, retry); err != nil || snapshot() != before {
		t.Fatalf("reordered retry: %v", err)
	}
	cases := []struct {
		name   string
		change func(*catalog.Settings, []catalog.Item)
		want   error
	}{
		{"name", func(_ *catalog.Settings, m []catalog.Item) { m[1].Name = "Other" }, catalog.ErrConflict},
		{"fee", func(c *catalog.Settings, _ []catalog.Item) { c.FlatFeeMinor++ }, catalog.ErrConflict},
		{"exponent", func(c *catalog.Settings, _ []catalog.Item) { c.CurrencyExponent-- }, catalog.ErrConflict},
		{"currency", func(c *catalog.Settings, m []catalog.Item) {
			c.Currency = "USD"
			for i := range m {
				m[i].Currency = "USD"
			}
		}, catalog.ErrConflict},
		{"last invalid", func(_ *catalog.Settings, m []catalog.Item) { m[1].Name = "\xff" }, catalog.ErrInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			m := append([]catalog.Item(nil), menu...)
			tc.change(&c, m)
			input := append([]catalog.Item(nil), m...)
			err := store.Initialize(t.Context(), c, m)
			if err != tc.want || !reflect.DeepEqual(input, m) || snapshot() != before {
				t.Fatalf("rejection: %v want %v", err, tc.want)
			}
		})
	}
	got, err := store.SetAvailability(t.Context(), menu[1].ID, 0, true)
	if err != catalog.ErrStale || err.Error() != "menu item missing or version stale" || got != (catalog.Item{}) || snapshot() != before {
		t.Fatalf("stale: %+v %v", got, err)
	}
	stored, err := store.Menu(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items := make([]pricing.Item, len(stored))
	for i, m := range stored {
		items[i] = pricing.Item{ID: m.ID, Name: m.Name, Currency: m.Currency, Price: m.PriceMinor, Available: m.Available}
	}
	engine, err := pricing.New(pricing.Settings{Currency: c.Currency, Exponent: int(c.CurrencyExponent), DeliveryFee: c.FlatFeeMinor}, items)
	if err != nil {
		t.Fatal(err)
	}
	draft := []pricing.Unit{{ID: "90000000-0000-4000-8000-000000000004", MenuID: menu[1].ID, Note: " exact "}}
	f := pricing.Fulfillment{Kind: pricing.Delivery, Address: " A ", Phone: " P "}
	q, err := engine.Quote(draft, f)
	want := pricing.Quote{Units: append([]pricing.Unit(nil), draft...), Currency: "EUR", Exponent: 6, Subtotal: 23, Fee: 17, Total: 40, Payment: "cash_on_delivery", Fulfillment: f}
	if err != nil || !reflect.DeepEqual(q, want) {
		t.Fatalf("quote: %+v %v", q, err)
	}
	q.Units[0].Note = "changed"
	if draft[0].Note != " exact " {
		t.Fatal("quote aliases input")
	}
	items = append(items, pricing.Item{ID: "90000000-0000-4000-8000-000000000005", Name: "Bad unused", Currency: "USD", Available: false})
	if e, err := pricing.New(pricing.Settings{Currency: "EUR", Exponent: 6, DeliveryFee: 17}, items); e != nil || err == nil || err.Error() != "invalid catalog" {
		t.Fatalf("unused invalid: %v %v", e, err)
	}
	empty, err := catalog.OpenStore(t.Context(), dsn, "90000000-0000-4000-8000-000000000006")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if err := empty.Initialize(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := empty.Initialize(t.Context(), cfg, []catalog.Item{}); err != nil {
		t.Fatal(err)
	}
	ec, err := empty.Settings(t.Context())
	if err != nil || ec != cfg {
		t.Fatalf("empty settings: %+v %v", ec, err)
	}
	em, err := empty.Menu(t.Context())
	if err != nil || len(em) != 0 {
		t.Fatalf("empty menu: %+v %v", em, err)
	}
}
