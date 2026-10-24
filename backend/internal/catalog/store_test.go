package catalog_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "11111111-1111-4111-8111-111111111111"
const other = "22222222-2222-4222-8222-222222222222"

func fixture() (catalog.Settings, []catalog.Item) {
	return catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{
		{ID: "10000000-0000-4000-8000-000000000001", Name: "Garden Sandwich", PriceMinor: 650, Currency: "USD", Available: true},
		{ID: "10000000-0000-4000-8000-000000000002", Name: "Tomato Soup", PriceMinor: 450, Currency: "USD", Available: true},
		{ID: "10000000-0000-4000-8000-000000000003", Name: "Sparkling Water", PriceMinor: 200, Currency: "USD", Available: true},
	}
}
func open(t *testing.T, dsn, id string) *catalog.Store {
	t.Helper()
	s, err := catalog.OpenStore(t.Context(), dsn, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func state(t *testing.T, s *catalog.Store) (catalog.Settings, []catalog.Item) {
	t.Helper()
	settings, err := s.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Menu(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return settings, items
}
func equalState(t *testing.T, s *catalog.Store, want catalog.Settings, items []catalog.Item) {
	t.Helper()
	got, menu := state(t, s)
	if got != want || !reflect.DeepEqual(menu, items) {
		t.Fatalf("state: %+v %+v; want %+v %+v", got, menu, want, items)
	}
}
func TestOpenIsGuardedAndLazy(t *testing.T) {
	for _, tc := range []struct{ dsn, id string }{{"", restaurant}, {"postgres://localhost/production", restaurant}, {"postgres://127.0.0.1:1/restaurant_foundation_absent", strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")}} {
		s, err := catalog.OpenStore(t.Context(), tc.dsn, tc.id)
		if s != nil || err != catalog.ErrInput {
			t.Fatalf("got %v %v", s, err)
		}
	}
	s := open(t, "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	got, err := s.Settings(t.Context())
	if got != (catalog.Settings{}) || err != catalog.ErrUnavailable {
		t.Fatalf("safe failure: %+v %v", got, err)
	}
}
func TestCatalogLifecycle(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	foreign := open(t, dsn, other)
	got, err := s.Settings(t.Context())
	if got != (catalog.Settings{}) || err != catalog.ErrNotFound {
		t.Fatalf("unconfigured: %+v %v", got, err)
	}
	menu, err := s.Menu(t.Context())
	if err != nil || len(menu) != 0 {
		t.Fatalf("implicit seed: %+v %v", menu, err)
	}
	base, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	event := foundation.Event{ID: "catalog-foundation", Sender: "fictional", Text: "preserved"}
	if _, err = base.Accept(t.Context(), event, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := base.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings, items := fixture()
	original := append([]catalog.Item(nil), items...)
	if err = s.Initialize(t.Context(), settings, items); err != nil {
		t.Fatal(err)
	}
	equalState(t, s, settings, items)
	if !reflect.DeepEqual(items, original) {
		t.Fatal("setup mutated caller input")
	}
	updated, err := s.SetAvailability(t.Context(), items[0].ID, 0, false)
	want := items[0]
	want.Available = false
	want.Version = 1
	if err != nil || updated != want {
		t.Fatalf("update: %+v %v want %+v", updated, err, want)
	}
	if err = s.Initialize(t.Context(), settings, items); err != nil {
		t.Fatal(err)
	}
	original[0] = want
	equalState(t, s, settings, original)
	for _, tc := range []struct {
		name, id string
		version  int64
		store    *catalog.Store
		want     error
	}{
		{"stale", items[0].ID, 0, s, catalog.ErrStale}, {"missing", "10000000-0000-4000-8000-000000000099", 0, s, catalog.ErrStale}, {"scope", items[0].ID, 1, foreign, catalog.ErrStale}, {"invalid", "bad", 0, s, catalog.ErrInput}, {"negative", items[0].ID, -1, s, catalog.ErrInput}, {"overflow", items[0].ID, math.MaxInt64, s, catalog.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.store.SetAvailability(t.Context(), tc.id, tc.version, true)
			if got != (catalog.Item{}) || err != tc.want {
				t.Fatalf("got %+v %v", got, err)
			}
			equalState(t, s, settings, original)
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*catalog.Settings, *[]catalog.Item)
		want   error
	}{
		{"currency", func(s *catalog.Settings, _ *[]catalog.Item) { s.Currency = "usd" }, catalog.ErrInput},
		{"ascii", func(s *catalog.Settings, _ *[]catalog.Item) { s.Currency = "ÜSD" }, catalog.ErrInput},
		{"exponent", func(s *catalog.Settings, _ *[]catalog.Item) { s.CurrencyExponent = 7 }, catalog.ErrInput},
		{"negative exponent", func(s *catalog.Settings, _ *[]catalog.Item) { s.CurrencyExponent = -1 }, catalog.ErrInput},
		{"fee", func(s *catalog.Settings, _ *[]catalog.Item) { s.FlatFeeMinor = -1 }, catalog.ErrInput},
		{"version", func(s *catalog.Settings, _ *[]catalog.Item) { s.Version = -1 }, catalog.ErrInput},
		{"blank", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Name = " \t" }, catalog.ErrInput},
		{"long", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Name = strings.Repeat("é", 201) }, catalog.ErrInput},
		{"utf8", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Name = "\xff" }, catalog.ErrInput},
		{"nul", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Name = "a\x00b" }, catalog.ErrInput},
		{"price", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].PriceMinor = -1 }, catalog.ErrInput},
		{"item version", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Version = -1 }, catalog.ErrInput},
		{"uuid", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].ID = "bad" }, catalog.ErrInput},
		{"mismatch", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Currency = "EUR" }, catalog.ErrInput},
		{"duplicate", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[1].ID = (*m)[0].ID }, catalog.ErrInput},
		{"fixed settings", func(s *catalog.Settings, _ *[]catalog.Item) { s.FlatFeeMinor++ }, catalog.ErrConflict},
		{"fixed price", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].PriceMinor++ }, catalog.ErrConflict},
		{"fixed name", func(_ *catalog.Settings, m *[]catalog.Item) { (*m)[0].Name = "Renamed" }, catalog.ErrConflict},
		{"fixed exponent", func(s *catalog.Settings, _ *[]catalog.Item) { s.CurrencyExponent = 3 }, catalog.ErrConflict},
		{"fixed currency", func(s *catalog.Settings, m *[]catalog.Item) {
			s.Currency = "EUR"
			for i := range *m {
				(*m)[i].Currency = "EUR"
			}
		}, catalog.ErrConflict},
		{"partial retry", func(_ *catalog.Settings, m *[]catalog.Item) { *m = (*m)[:1] }, catalog.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, m := fixture()
			tc.change(&cfg, &m)
			copyM := append([]catalog.Item(nil), m...)
			err := s.Initialize(t.Context(), cfg, m)
			if err != tc.want {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if !reflect.DeepEqual(copyM, m) {
				t.Fatal("rejection mutated input")
			}
			equalState(t, s, settings, original)
		})
	}
	// Global ID collisions roll back identity/settings/new preceding items as well.
	collision := append([]catalog.Item(nil), items...)
	collision[0].ID = "00000000-0000-4000-8000-000000000001"
	if err = foreign.Initialize(t.Context(), settings, collision); err != catalog.ErrConflict {
		t.Fatalf("collision: %v", err)
	}
	if cfg, err := foreign.Settings(t.Context()); cfg != (catalog.Settings{}) || err != catalog.ErrNotFound {
		t.Fatalf("partial settings: %+v %v", cfg, err)
	}
	if m, err := foreign.Menu(t.Context()); err != nil || len(m) != 0 {
		t.Fatalf("partial menu: %+v %v", m, err)
	}
	after, err := base.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("foundation changed: %+v %+v %v", before, after, err)
	}
}
func TestSetupBoundariesAndVersionAuthority(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	cfg, items := fixture()
	items[2].Name = "\xff"
	if err := s.Initialize(t.Context(), cfg, items); err != catalog.ErrInput {
		t.Fatalf("invalid fresh setup: %v", err)
	}
	if got, err := s.Settings(t.Context()); got != (catalog.Settings{}) || err != catalog.ErrNotFound {
		t.Fatalf("invalid setup wrote settings: %+v %v", got, err)
	}
	if got, err := s.Menu(t.Context()); err != nil || len(got) != 0 {
		t.Fatalf("invalid setup wrote items: %+v %v", got, err)
	}
	for i, id := range []string{restaurant, other} {
		store := open(t, dsn, id)
		cfg, items = fixture()
		cfg.CurrencyExponent = int16(i * 6)
		cfg.FlatFeeMinor = math.MaxInt64
		cfg.Version = math.MaxInt64
		for j := range items {
			if i == 1 {
				items[j].ID = strings.Replace(items[j].ID, "10000000", "30000000", 1)
			}
			items[j].Version = math.MaxInt64
		}
		items[0].Name = strings.Repeat("é", 200)
		items[0].PriceMinor = math.MaxInt64
		items[1].PriceMinor = 0
		items[1].Available = false
		if err := store.Initialize(t.Context(), cfg, items); err != nil {
			t.Fatal(err)
		}
		cfg.Version = 0
		for j := range items {
			items[j].Version = 0
		}
		equalState(t, store, cfg, items)
		// A same-value update still consumes one version, without changing fixed fields.
		got, err := store.SetAvailability(t.Context(), items[1].ID, 0, false)
		want := items[1]
		want.Version = 1
		if err != nil || got != want {
			t.Fatalf("same-value update: %+v %v", got, err)
		}
	}
}
func TestConcurrentSetupAndUpdates(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	cfg, items := fixture()
	run := func(fn func(int) error) []error {
		out := make([]error, 2)
		var wg sync.WaitGroup
		for i := range out {
			wg.Add(1)
			go func(i int) { defer wg.Done(); out[i] = fn(i) }(i)
		}
		wg.Wait()
		return out
	}
	for _, err := range run(func(int) error { return s.Initialize(t.Context(), cfg, items) }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	equalState(t, s, cfg, items)
	errs := run(func(int) error { _, err := s.SetAvailability(t.Context(), items[0].ID, 0, false); return err })
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, catalog.ErrStale) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("update winners %d: %v", wins, errs)
	}
	items[0].Available = false
	items[0].Version = 1
	equalState(t, s, cfg, items)
	fresh := open(t, dsn, other)
	errs = run(func(i int) error {
		c, m := fixture()
		for j := range m {
			m[j].ID = strings.Replace(m[j].ID, "10000000", "20000000", 1)
		}
		c.FlatFeeMinor += int64(i)
		return fresh.Initialize(t.Context(), c, m)
	})
	wins = 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else if err != catalog.ErrConflict {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("setup winners %d: %v", wins, errs)
	}
	c, m := state(t, fresh)
	wantC, wantM := fixture()
	for j := range wantM {
		wantM[j].ID = strings.Replace(wantM[j].ID, "10000000", "20000000", 1)
	}
	if errs[1] == nil {
		wantC.FlatFeeMinor++
	}
	if c != wantC || !reflect.DeepEqual(m, wantM) {
		t.Fatalf("mixed setup %+v %+v", c, m)
	}
}
