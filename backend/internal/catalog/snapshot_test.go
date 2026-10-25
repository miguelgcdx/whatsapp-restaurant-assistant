package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestMenuSnapshotValidate(t *testing.T) {
	cfg, items := fixture()
	valid := catalog.MenuSnapshot{Settings: cfg, Items: items}
	cases := []struct {
		name   string
		change func(*catalog.MenuSnapshot)
		want   error
	}{
		{"valid", func(*catalog.MenuSnapshot) {}, nil},
		{"closed unavailable", func(s *catalog.MenuSnapshot) { s.Settings.Open = false; s.Items[0].Available = false }, nil},
		{"empty", func(s *catalog.MenuSnapshot) { s.Items = []catalog.Item{} }, nil},
		{"nil items", func(s *catalog.MenuSnapshot) { s.Items = nil }, catalog.ErrStoredData},
		{"currency", func(s *catalog.MenuSnapshot) { s.Settings.Currency = "usd" }, catalog.ErrStoredData},
		{"currency length", func(s *catalog.MenuSnapshot) { s.Settings.Currency = "US" }, catalog.ErrStoredData},
		{"precision low", func(s *catalog.MenuSnapshot) { s.Settings.CurrencyExponent = -1 }, catalog.ErrStoredData},
		{"precision high", func(s *catalog.MenuSnapshot) { s.Settings.CurrencyExponent = 7 }, catalog.ErrStoredData},
		{"fee", func(s *catalog.MenuSnapshot) { s.Settings.FlatFeeMinor = -1 }, catalog.ErrStoredData},
		{"settings version", func(s *catalog.MenuSnapshot) { s.Settings.Version = -1 }, catalog.ErrStoredData},
		{"id", func(s *catalog.MenuSnapshot) { s.Items[0].ID = "bad" }, catalog.ErrStoredData},
		{"duplicate", func(s *catalog.MenuSnapshot) { s.Items[1].ID = s.Items[0].ID }, catalog.ErrStoredData},
		{"unordered", func(s *catalog.MenuSnapshot) { s.Items[0], s.Items[1] = s.Items[1], s.Items[0] }, catalog.ErrStoredData},
		{"blank name", func(s *catalog.MenuSnapshot) { s.Items[0].Name = "\t\n" }, catalog.ErrStoredData},
		{"long name", func(s *catalog.MenuSnapshot) { s.Items[0].Name = strings.Repeat("料", 201) }, catalog.ErrStoredData},
		{"nul name", func(s *catalog.MenuSnapshot) { s.Items[0].Name = "a\x00b" }, catalog.ErrStoredData},
		{"utf8 name", func(s *catalog.MenuSnapshot) { s.Items[0].Name = string([]byte{255}) }, catalog.ErrStoredData},
		{"price", func(s *catalog.MenuSnapshot) { s.Items[0].PriceMinor = -1 }, catalog.ErrStoredData},
		{"item currency", func(s *catalog.MenuSnapshot) { s.Items[0].Currency = "EUR" }, catalog.ErrStoredData},
		{"item version", func(s *catalog.MenuSnapshot) { s.Items[0].Version = -1 }, catalog.ErrStoredData},
		{"budget", func(s *catalog.MenuSnapshot) { s.Items = snapshotItems(1001) }, catalog.ErrSnapshotLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := catalog.MenuSnapshot{Settings: valid.Settings, Items: append([]catalog.Item{}, valid.Items...)}
			tc.change(&s)
			before := s
			if s.Items != nil {
				before.Items = append([]catalog.Item{}, s.Items...)
			}
			if err := s.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("Validate: %v; want %v", err, tc.want)
			}
			if !reflect.DeepEqual(s, before) {
				t.Fatal("Validate mutated input")
			}
		})
	}
	for _, exponent := range []int16{0, 6} {
		s := valid
		s.Settings.CurrencyExponent = exponent
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotItems(n int) []catalog.Item {
	items := make([]catalog.Item, n)
	for i := range items {
		items[i] = catalog.Item{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), Name: "料理 <&>", PriceMinor: int64(i), Currency: "USD", Available: i%2 == 0}
	}
	return items
}

func snapshotError(t *testing.T, s catalog.MenuSnapshot, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(s, catalog.MenuSnapshot{}) {
		t.Fatalf("snapshot error: %+v %v; want zero / %v", s, err, want)
	}
}

func TestMenuSnapshotInputs(t *testing.T) {
	var nilStore *catalog.Store
	for _, s := range []*catalog.Store{nilStore, new(catalog.Store)} {
		got, err := s.Snapshot(context.Background())
		snapshotError(t, got, err, catalog.ErrInput)
	}
	s := open(t, testpg.Start(t), restaurant)
	got, err := s.Snapshot(nil)
	snapshotError(t, got, err, catalog.ErrInput)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = s.Snapshot(ctx)
	snapshotError(t, got, err, context.Canceled)
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	got, err = s.Snapshot(expired)
	snapshotError(t, got, err, context.DeadlineExceeded)
	// A closed owned pool proves unavailability without probing a host port.
	// The existing helper also closes at cleanup; pool Close is idempotent.
	s.Close()
	got, err = s.Snapshot(t.Context())
	snapshotError(t, got, err, catalog.ErrUnavailable)
}

func TestMenuSnapshotDatabase(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg, _ := fixture()
	t.Run("unconfigured", func(t *testing.T) {
		got, err := open(t, dsn, restaurant).Snapshot(t.Context())
		snapshotError(t, got, err, catalog.ErrNotFound)
	})
	for index, n := range []int{0, 105, 1000, 1001} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			id := fmt.Sprintf("30000000-0000-4000-8000-%012d", index+1)
			s := open(t, dsn, id)
			items := snapshotItems(n)
			for i := range items {
				items[i].ID = fmt.Sprintf("%08d", index+10) + items[i].ID[8:]
			}
			// Initialize in reverse order: Snapshot must order by UUID, not insertion.
			reverse := append([]catalog.Item{}, items...)
			for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
				reverse[i], reverse[j] = reverse[j], reverse[i]
			}
			if err := s.Initialize(t.Context(), cfg, reverse); err != nil {
				t.Fatal(err)
			}
			before, err := s.Page(t.Context(), 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Snapshot(t.Context())
			if n > 1000 {
				snapshotError(t, got, err, catalog.ErrSnapshotLimit)
			} else {
				if err != nil || got.Items == nil || got.Settings != cfg || !reflect.DeepEqual(got.Items, items) {
					t.Fatalf("complete snapshot n=%d: count=%d err=%v", n, len(got.Items), err)
				}
				if err := got.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			after, err := s.Page(t.Context(), 100, nil)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("Page changed: %v", err)
			}
			if len(after.Items) != min(n, 100) || (after.Next != nil) != (n > 100) {
				t.Fatal("prior Page limit/lookahead behavior changed")
			}
			equalState(t, s, cfg, items)
		})
	}
	t.Run("scope closed and stored validation", func(t *testing.T) {
		a, b := open(t, dsn, restaurant), open(t, dsn, other)
		_, items := fixture()
		closed := cfg
		closed.Open = false
		items[0].Available = false
		if err := a.Initialize(t.Context(), closed, items); err != nil {
			t.Fatal(err)
		}
		if err := b.Initialize(t.Context(), cfg, []catalog.Item{}); err != nil {
			t.Fatal(err)
		}
		got, err := a.Snapshot(t.Context())
		if err != nil || got.Settings != closed || !reflect.DeepEqual(got.Items, items) {
			t.Fatalf("closed: %+v %v", got, err)
		}
		foreign, err := b.Snapshot(t.Context())
		if err != nil || foreign.Settings != cfg || foreign.Items == nil || len(foreign.Items) != 0 {
			t.Fatalf("scope: %+v %v", foreign, err)
		}
		equalState(t, a, closed, items)
		// A tab passes DDL's btrim check but not the existing domain name rule.
		if _, err := pool.Exec(t.Context(), "UPDATE app.menu_items SET name=$1 WHERE restaurant_id=$2 AND id=$3", "\t", restaurant, items[0].ID); err != nil {
			t.Fatal(err)
		}
		got, err = a.Snapshot(t.Context())
		snapshotError(t, got, err, catalog.ErrStoredData)
	})
	t.Run("coherence", func(t *testing.T) {
		id := "40000000-0000-4000-8000-000000000001"
		s := open(t, dsn, id)
		items := snapshotItems(1)
		items[0].ID = "40000000" + items[0].ID[8:]
		if err := s.Initialize(t.Context(), cfg, items); err != nil {
			t.Fatal(err)
		}
		gate := snapshotGate(t, pool)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		result, done := snapshotAsync(s, ctx)
		defer snapshotReaderCleanup(t, gate, cancel, done)
		snapshotWait(t, pool)
		if _, err := gate.Exec(t.Context(), "UPDATE app.restaurant_settings SET open=false,version=version+1 WHERE restaurant_id=$1", id); err != nil {
			t.Fatal(err)
		}
		if _, err := gate.Exec(t.Context(), "UPDATE app.menu_items SET available=false,version=version+1 WHERE restaurant_id=$1", id); err != nil {
			t.Fatal(err)
		}
		if err := gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		first := snapshotReceive(t, result)
		if first.err != nil || first.value.Settings != cfg || !reflect.DeepEqual(first.value.Items, items) {
			t.Fatalf("mixed snapshot: %+v %v", first.value, first.err)
		}
		next, err := s.Snapshot(t.Context())
		want := cfg
		want.Open = false
		want.Version = 1
		items[0].Available = false
		items[0].Version = 1
		if err != nil || next.Settings != want || !reflect.DeepEqual(next.Items, items) {
			t.Fatalf("next snapshot: %+v %v", next, err)
		}
	})
	for _, mode := range []string{"cancel", "earlier deadline", "local cap"} {
		t.Run(mode, func(t *testing.T) {
			s := open(t, dsn, "30000000-0000-4000-8000-000000000001")
			gate := snapshotGate(t, pool)
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "earlier deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			result, done := snapshotAsync(s, ctx)
			defer snapshotReaderCleanup(t, gate, cancel, done)
			snapshotWait(t, pool)
			want := context.DeadlineExceeded
			if mode == "cancel" {
				want = context.Canceled
				cancel()
			}
			out := snapshotReceive(t, result)
			snapshotError(t, out.value, out.err, want)
			if err := gate.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot(t.Context()); err != nil {
				t.Fatalf("read after cancellation: %v", err)
			}
		})
	}
}

type snapshotOutcome struct {
	value catalog.MenuSnapshot
	err   error
}

func snapshotAsync(s *catalog.Store, ctx context.Context) (<-chan snapshotOutcome, <-chan struct{}) {
	result := make(chan snapshotOutcome, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		value, err := s.Snapshot(ctx)
		result <- snapshotOutcome{value, err}
	}()
	return result, done
}
func snapshotReceive(t *testing.T, ch <-chan snapshotOutcome) snapshotOutcome {
	t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out
	case <-timer.C:
		t.Fatal("snapshot did not finish within bounded wait")
		return snapshotOutcome{}
	}
}
func snapshotGate(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE app.menu_items IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	return tx
}
func snapshotReaderCleanup(t *testing.T, gate pgx.Tx, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = gate.Rollback(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Error("snapshot reader cleanup exceeded one second")
	}
}
func snapshotWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid
   WHERE a.datname=current_database() AND a.wait_event_type='Lock'
   AND l.relation='app.menu_items'::regclass AND NOT l.granted
   AND a.query LIKE 'SELECT id::text,name,price_minor,currency,available,version%')`).Scan(&waiting)
		if err != nil {
			t.Fatalf("observe menu lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("no positive menu lock wait observed")
			return
		}
	}
}
