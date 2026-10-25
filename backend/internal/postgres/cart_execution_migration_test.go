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
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

//go:embed migrations/00001_local_foundation.sql migrations/00002_local_staff_identity.sql migrations/00003_local_catalog.sql migrations/00004_staff_catalog_operations.sql migrations/00005_local_cart.sql
var beforeExecutionFiles embed.FS

func TestCartExecutionMigrationPreservesPopulatedV5(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
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
	files, err := fs.Sub(beforeExecutionFiles, "migrations")
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
	const restaurant = "c0000000-0000-4000-8000-000000000006"
	const item = "10000000-0000-4000-8000-000000000001"
	cat, err := catalog.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if err = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: item, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	if active, e := members.EnsureMember(ctx, "fixture"); e != nil || !active {
		t.Fatal(e)
	}
	intent := catalog.AvailabilityIntent{Subject: "fixture", Key: "before", RequestID: "before", ItemID: item, ExpectedVersion: 0, Value: true}
	staffResult, err := cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil {
		t.Fatal(err)
	}
	menuBefore, err := cat.Menu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := cat.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cart.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := s.ProvisionLocal(ctx, "fixture", "sender")
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
	mid, err := cart.ParseMenuID(item)
	if err != nil {
		t.Fatal(err)
	}
	add, err := cart.Add(mid, 2)
	if err != nil {
		t.Fatal(err)
	}
	addOp, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, add)
	if err != nil {
		t.Fatal(err)
	}
	note, err := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, " exact 🥪\n")
	if err != nil {
		t.Fatal(err)
	}
	noteOp, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	noted, err := s.Apply(ctx, local, noteOp, base.Cart.ID, added.Cart.Version, note)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	event := foundation.Event{ID: "before-execution", Sender: "sender", Text: "opaque"}
	now := time.Now()
	if _, err = legacy.Accept(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := legacy.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = legacy.CompleteJob(ctx, job, "ack", now); err != nil {
		t.Fatal(err)
	}
	delivery, ok, err := legacy.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = legacy.FinishDelivery(ctx, delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "fixture"}, now); err != nil {
		t.Fatal(err)
	}
	beforeEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	hashes := func() map[string]string {
		out := map[string]string{}
		for _, table := range []string{"conversation_sequences", "inbound_events", "jobs", "outbox", "send_attempts", "restaurants", "staff_memberships", "restaurant_settings", "menu_items", "operations", "cart_conversations", "carts", "cart_units", "cart_operations"} {
			var hash string
			if e := conn.QueryRow(ctx, `SELECT count(*)::text||':'||md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.`+table+` r`).Scan(&hash); e != nil {
				t.Fatal(e)
			}
			out[table] = hash
		}
		return out
	}
	before := hashes()
	var v int64
	if err = conn.QueryRow(ctx, `SELECT max(version_id) FROM app.goose_db_version`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("v5=%d %v", v, err)
	}
	if err = postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cart_execution_sources", "cart_execution_calls"} {
		var n int64
		if err = conn.QueryRow(ctx, `SELECT count(*) FROM app.`+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("new table %s=%d %v", table, n, err)
		}
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("upgrade changed historical data/counters")
	}
	read, err := s.Read(ctx, local, base.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, noted.Cart) {
		t.Fatalf("snapshot=%+v %v", read, err)
	}
	replay, err := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, add)
	if err != nil || !reflect.DeepEqual(replay, added) {
		t.Fatalf("history=%+v %v", replay, err)
	}
	created, err := s.Create(ctx, local, createOp)
	if err != nil || !reflect.DeepEqual(created, base) {
		t.Fatalf("create=%+v %v", created, err)
	}
	afterEvent, err := legacy.Inspect(ctx, event.ID)
	if err != nil || !reflect.DeepEqual(afterEvent, beforeEvent) {
		t.Fatalf("foundation=%+v %v", afterEvent, err)
	}
	menuAfter, err := cat.Menu(ctx)
	if err != nil || !reflect.DeepEqual(menuBefore, menuAfter) {
		t.Fatal("menu changed", err)
	}
	settingsAfter, err := cat.Settings(ctx)
	if err != nil || settingsBefore != settingsAfter {
		t.Fatal("settings changed", err)
	}
	if active, e := members.Access(ctx, "fixture"); e != nil || !active {
		t.Fatal("staff changed", e)
	}
	staffReplay, err := cat.ApplyAvailability(ctx, intent, members.Access)
	if err != nil || !reflect.DeepEqual(staffReplay, staffResult) {
		t.Fatal("staff replay changed", err)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("read/replay changed historical counters")
	}
}
