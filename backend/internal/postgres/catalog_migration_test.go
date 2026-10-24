package postgres_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestCatalogMigrationPreservesFoundationAndUnconfiguredIdentity(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	for _, table := range []string{"app.restaurant_settings", "app.menu_items"} {
		var exists bool
		if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Fatalf("catalog migration missing %s: exists=%v err=%v", table, exists, err)
		}
	}
	const restaurant = "11111111-1111-4111-8111-111111111111"
	if _, err := conn.Exec(t.Context(), "INSERT INTO app.restaurants(id) VALUES ($1)", restaurant); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := foundation.Event{ID: "catalog-preserve", Sender: "fictional", Text: "unchanged"}
	if _, err := store.Accept(t.Context(), event, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := store.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	after, err := store.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun changed durable event: before=%+v after=%+v err=%v", before, after, err)
	}
	var identities, settings, items int
	if err := conn.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM app.restaurants), (SELECT count(*) FROM app.restaurant_settings), (SELECT count(*) FROM app.menu_items)").Scan(&identities, &settings, &items); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || settings != 0 || items != 0 {
		t.Fatalf("migration created operational defaults: identities=%d settings=%d items=%d", identities, settings, items)
	}
}
