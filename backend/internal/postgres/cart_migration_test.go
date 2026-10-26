package postgres_test

import (
	"embed"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

// Freeze the pre-cart fixture by selecting the existing migration files, not by
// reading git history, replacing product migrations, or mutating Goose globals.
//
//go:embed migrations/00001_local_foundation.sql migrations/00002_local_staff_identity.sql migrations/00003_local_catalog.sql migrations/00004_staff_catalog_operations.sql
var beforeCartFiles embed.FS

func TestCartMigrationUpgradesPopulatedV4WithoutSeeds(t *testing.T) {
	dsn := testpg.Start(t)
	ctx := t.Context()
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	if _, err = sqlDB.ExecContext(ctx, "CREATE SCHEMA app"); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(beforeCartFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, files, goose.WithTableName("app.goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var oldVersion int64
	var absent bool
	if err = conn.QueryRow(ctx, `SELECT max(version_id),to_regclass('app.carts') IS NULL FROM app.goose_db_version`).Scan(&oldVersion, &absent); err != nil || oldVersion != 4 || !absent {
		t.Fatalf("not pre-cart v4: version=%d absent=%v err=%v", oldVersion, absent, err)
	}
	const restaurant = "c0000000-0000-4000-8000-000000000001"
	const item = "10000000-0000-4000-8000-000000000001"
	legacy, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	finishLegacyFormat := pre11LegacyJobFormat(t, dsn)
	event := foundation.Event{ID: "pre-cart-event", Sender: "fictional-pre-cart", Text: "unchanged"}
	now := time.Now().UTC()
	if fresh, err := legacy.Accept(ctx, event, now); err != nil || !fresh {
		t.Fatalf("accept=%v %v", fresh, err)
	}
	job, ok, err := legacy.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	if err = legacy.CompleteJob(ctx, job, "fictional acknowledgement", now); err != nil {
		t.Fatal(err)
	}
	finishLegacyFormat()
	freezePre10AcceptedDelivery(t, dsn, event.ID, "fictional-provider-result", now)
	beforeEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	settings := catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}
	if err = cat.Initialize(ctx, settings, []catalog.Item{{ID: item, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	if active, err := members.EnsureMember(ctx, "fixture-member"); err != nil || !active {
		t.Fatalf("membership=%v %v", active, err)
	}
	intent := catalog.AvailabilityIntent{Subject: "fixture-member", Key: "pre-cart-key", RequestID: "pre-cart-request", ItemID: item, ExpectedVersion: 0, Value: false}
	beforeResult, err := cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil {
		t.Fatal(err)
	}
	beforeMenu, err := cat.Menu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeSettings, err := cat.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// SQL supplements the public exports/replay with exact row/counter preservation;
	// it is not an invented public ledger diagnostic endpoint.
	tables := []string{"conversation_sequences", "inbound_events", "jobs", "outbox", "send_attempts", "restaurants", "staff_memberships", "restaurant_settings", "menu_items", "operations"}
	hashes := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range tables {
			var hash string
			query := legacyMigrationFingerprintSQL(table)
			if err := conn.QueryRow(ctx, query).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			out[table] = hash
		}
		return out
	}
	beforeHashes := hashes()
	if err = postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hashes(), beforeHashes) {
		t.Fatal("upgrade changed legacy records/counters")
	}
	assertMigratedLegacyMetadata(t, conn)
	for _, table := range []string{"cart_conversations", "carts", "cart_units", "cart_operations"} {
		var n int64
		if err = conn.QueryRow(ctx, `SELECT count(*) FROM app.`+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("new table=%s count=%d err=%v", table, n, err)
		}
	}
	afterEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil || !reflect.DeepEqual(afterEvent, beforeEvent) {
		t.Fatalf("Inspect changed=%+v %v", afterEvent, err)
	}
	afterMenu, err := cat.Menu(ctx)
	if err != nil || !reflect.DeepEqual(afterMenu, beforeMenu) {
		t.Fatalf("menu changed=%+v %v", afterMenu, err)
	}
	afterSettings, err := cat.Settings(ctx)
	if err != nil || afterSettings != beforeSettings {
		t.Fatalf("settings changed=%+v %v", afterSettings, err)
	}
	if active, err := members.Access(ctx, "fixture-member"); err != nil || !active {
		t.Fatalf("access changed=%v %v", active, err)
	}
	replay, err := cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil || !reflect.DeepEqual(replay, beforeResult) {
		t.Fatalf("staff replay changed=%s %v", replay, err)
	}
	if !reflect.DeepEqual(hashes(), beforeHashes) {
		t.Fatal("replay changed legacy counters")
	}
	if fresh, err := legacy.Accept(ctx, event, now); err != nil || fresh {
		t.Fatalf("foundation replay=%v %v", fresh, err)
	}
	intent.Key = "post-cart-key"
	intent.ExpectedVersion = 1
	intent.Value = true
	next, err := cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil || reflect.DeepEqual(next, beforeResult) {
		t.Fatalf("new staff operation=%s %v", next, err)
	}
	replay, err = cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil || !reflect.DeepEqual(next, replay) {
		t.Fatalf("new key replay=%s %v", replay, err)
	}
	assertCurrentLegacyDelivery(t, legacy)
}
