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
	"restaurant-assistant/backend/internal/testpg"
)

//go:embed migrations/00001_local_foundation.sql migrations/00002_local_staff_identity.sql migrations/00003_local_catalog.sql migrations/00004_staff_catalog_operations.sql migrations/00005_local_cart.sql migrations/00006_local_cart_execution.sql migrations/00007_local_cart_fulfillment.sql
var beforeSummaryFiles embed.FS

func TestCartSummaryMigrationPreservesPopulatedV7(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
	cfg, e := postgres.LocalPoolConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	db := stdlib.OpenDB(*cfg.ConnConfig)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.ExecContext(ctx, "CREATE SCHEMA app"); e != nil {
		t.Fatal(e)
	}
	files, e := fs.Sub(beforeSummaryFiles, "migrations")
	if e != nil {
		t.Fatal(e)
	}
	provider, e := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithTableName("app.goose_db_version"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.Up(ctx); e != nil {
		t.Fatal(e)
	}
	const restaurant = "c0000000-0000-4000-8000-000000000008"
	const menu = "10000000-0000-4000-8000-000000000001"
	cat, e := catalog.OpenStore(ctx, dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer cat.Close()
	if e = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); e != nil {
		t.Fatal(e)
	}
	s, e := cart.OpenStore(ctx, dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	local, e := s.ProvisionLocal(ctx, "fictional", "sender")
	if e != nil {
		t.Fatal(e)
	}
	op, e := cart.NewOperationID()
	if e != nil {
		t.Fatal(e)
	}
	base, e := s.Create(ctx, local, op)
	if e != nil {
		t.Fatal(e)
	}
	mid, e := cart.ParseMenuID(menu)
	if e != nil {
		t.Fatal(e)
	}
	add, e := cart.Add(mid, 2)
	if e != nil {
		t.Fatal(e)
	}
	addOp, e := cart.NewOperationID()
	if e != nil {
		t.Fatal(e)
	}
	added, e := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, add)
	if e != nil {
		t.Fatal(e)
	}
	jobs, e := postgres.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer jobs.Close()
	now := time.Now()
	event := foundation.Event{ID: "before-summary", Sender: "sender", Text: "opaque"}
	if _, e = jobs.Accept(ctx, event, now); e != nil {
		t.Fatal(e)
	}
	job, ok, e := jobs.ClaimJob(ctx, now, time.Minute)
	if e != nil || !ok {
		t.Fatal(e)
	}
	note, e := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, " exact 🍲\n")
	if e != nil {
		t.Fatal(e)
	}
	call, e := cart.EditCall(base.Cart.ID, added.Cart.Version, note)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := s.RegisterExecution(ctx, local, event.ID, 0, call)
	if e != nil {
		t.Fatal(e)
	}
	noted, e := s.Execute(ctx, local, plan, job)
	if e != nil {
		t.Fatal(e)
	}
	choice, e := cart.DeliveryFulfillment(" fictional address ", " fictional phone ")
	if e != nil {
		t.Fatal(e)
	}
	richCall, e := cart.SetFulfillmentCall(noted.Cart.ID, noted.Cart.Version, choice)
	if e != nil {
		t.Fatal(e)
	}
	richPlan, e := s.RegisterExecution(ctx, local, event.ID, 1, richCall)
	if e != nil {
		t.Fatal(e)
	}
	selected, e := s.ExecuteDraft(ctx, local, richPlan, job)
	if e != nil {
		t.Fatal(e)
	}
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close(ctx)
	hashes := func() map[string]string {
		out := map[string]string{}
		for _, table := range []string{"conversation_sequences", "inbound_events", "jobs", "outbox", "send_attempts", "restaurants", "staff_memberships", "restaurant_settings", "menu_items", "operations", "cart_conversations", "carts", "cart_units", "cart_operations", "cart_execution_sources", "cart_execution_calls", "cart_fulfillments"} {
			var h string
			if e := conn.QueryRow(ctx, `SELECT count(*)::text||':'||md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.`+table+` r`).Scan(&h); e != nil {
				t.Fatal(e)
			}
			out[table] = h
		}
		return out
	}
	before := hashes()
	var v int64
	if e = conn.QueryRow(ctx, `SELECT max(version_id) FROM app.goose_db_version`).Scan(&v); e != nil || v != 7 {
		t.Fatalf("v7 %d %v", v, e)
	}
	if e = postgres.Migrate(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	var n int64
	if e = conn.QueryRow(ctx, `SELECT count(*) FROM app.cart_summary_previews`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("new preview table %d %v", n, e)
	}
	if e = conn.QueryRow(ctx, `SELECT max(version_id) FROM app.goose_db_version`).Scan(&v); e != nil || v != 8 {
		t.Fatalf("v8 %d %v", v, e)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("upgrade changed old bytes/fingerprints/counters")
	}
	read, e := s.Read(ctx, local, base.Cart.ID)
	if e != nil || !reflect.DeepEqual(read, selected.Cart) {
		t.Fatal(read, e)
	}
	replay, e := s.Execute(ctx, local, plan, job)
	if e != nil || !reflect.DeepEqual(replay, noted) {
		t.Fatal(replay, e)
	}
	rich, e := s.ExecuteDraft(ctx, local, richPlan, job)
	if e != nil || !reflect.DeepEqual(rich, selected) {
		t.Fatal(rich, e)
	}
	for i, c := range []cart.Call{call, richCall} {
		restored, e := s.RegisterExecution(ctx, local, event.ID, int64(i), c)
		want := plan
		if i == 1 {
			want = richPlan
		}
		if e != nil || restored != want {
			t.Fatal(restored, e)
		}
	}
	old, e := s.Create(ctx, local, op)
	if e != nil || !reflect.DeepEqual(old, base) {
		t.Fatal(old, e)
	}
	old, e = s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, add)
	if e != nil || !reflect.DeepEqual(old, added) {
		t.Fatal(old, e)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("replay changed historical bytes or counters")
	}
}
