package integration_test

import (
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/pricing"
	"restaurant-assistant/backend/internal/testpg"
)

// This seam checks exported storage/calculation APIs, not a customer or staff endpoint.
func TestStoredCatalogPricesDetachedDraft(t *testing.T) {
	t.Run("IndependentSpecProbes", independentSpecProbes)
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.OpenStore(t.Context(), dsn, "a0000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const sandwich = "10000000-0000-4000-8000-000000000001"
	const water = "10000000-0000-4000-8000-000000000003"
	settings := catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}
	menu := []catalog.Item{
		{ID: sandwich, Name: "Garden Sandwich", PriceMinor: 650, Currency: "USD", Available: true},
		{ID: "10000000-0000-4000-8000-000000000002", Name: "Tomato Soup", PriceMinor: 450, Currency: "USD", Available: true},
		{ID: water, Name: "Sparkling Water", PriceMinor: 200, Currency: "USD", Available: true},
	}
	if err := store.Initialize(t.Context(), settings, menu); err != nil {
		t.Fatal(err)
	}
	load := func() *pricing.Engine {
		t.Helper()
		cfg, err := store.Settings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		stored, err := store.Menu(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		items := make([]pricing.Item, len(stored))
		for i, item := range stored {
			items[i] = pricing.Item{ID: item.ID, Name: item.Name, Price: item.PriceMinor, Currency: item.Currency, Available: item.Available}
		}
		engine, err := pricing.New(pricing.Settings{Currency: cfg.Currency, Exponent: int(cfg.CurrencyExponent), DeliveryFee: cfg.FlatFeeMinor}, items)
		if err != nil {
			t.Fatal(err)
		}
		return engine
	}
	engine := load()
	units := []pricing.Unit{
		{ID: "20000000-0000-4000-8000-000000000001", MenuID: sandwich, Note: "no onions"},
		{ID: "20000000-0000-4000-8000-000000000002", MenuID: sandwich},
		{ID: "20000000-0000-4000-8000-000000000003", MenuID: water},
	}
	var draft []pricing.Unit
	for _, unit := range units {
		draft, err = engine.Add(draft, unit)
		if err != nil {
			t.Fatal(err)
		}
	}
	pickup := pricing.Fulfillment{Kind: pricing.Pickup}
	delivery := pricing.Fulfillment{Kind: pricing.Delivery, Address: "10 Fictional Street", Phone: "555-0100"}
	assertQuote := func(engine *pricing.Engine, draft []pricing.Unit, f pricing.Fulfillment, subtotal, fee int64) {
		t.Helper()
		before := append([]pricing.Unit(nil), draft...)
		quote, err := engine.Quote(draft, f)
		payment := "pay_at_pickup"
		if f.Kind == pricing.Delivery {
			payment = "cash_on_delivery"
		}
		want := pricing.Quote{Units: before, Currency: "USD", Exponent: 2, Subtotal: subtotal, Fee: fee, Total: subtotal + fee, Payment: payment, Fulfillment: f}
		if err != nil || !reflect.DeepEqual(quote, want) || !reflect.DeepEqual(draft, before) {
			t.Fatalf("quote=%+v want=%+v err=%v input=%+v", quote, want, err, draft)
		}
	}
	assertQuote(engine, draft, pickup, 1500, 0)
	assertQuote(engine, draft, delivery, 1500, 300)
	updated, err := store.SetAvailability(t.Context(), sandwich, 0, false)
	wantItem := menu[0]
	wantItem.Available = false
	wantItem.Version = 1
	if err != nil || updated != wantItem {
		t.Fatalf("availability=%+v err=%v", updated, err)
	}
	engine = load()
	rejected, err := engine.Add(draft, pricing.Unit{ID: "20000000-0000-4000-8000-000000000004", MenuID: sandwich})
	if err == nil || err.Error() != "menu item unavailable" || rejected != nil || !reflect.DeepEqual(draft, units) {
		t.Fatalf("unavailable add: result=%+v err=%v draft=%+v", rejected, err, draft)
	}
	assertQuote(engine, draft, delivery, 1500, 300)
	remaining, err := engine.Remove(draft, units[1].ID)
	if err != nil || !reflect.DeepEqual(remaining, []pricing.Unit{units[0], units[2]}) || !reflect.DeepEqual(draft, units) {
		t.Fatalf("exact remove: result=%+v err=%v input=%+v", remaining, err, draft)
	}
	assertQuote(engine, remaining, pickup, 850, 0)
	if err := store.Initialize(t.Context(), settings, menu); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Menu(t.Context())
	wantMenu := append([]catalog.Item(nil), menu...)
	wantMenu[0] = wantItem
	if err != nil || !reflect.DeepEqual(stored, wantMenu) {
		t.Fatalf("retry reset catalog: got=%+v want=%+v err=%v", stored, wantMenu, err)
	}
	assertQuote(load(), remaining, delivery, 850, 300)
}
