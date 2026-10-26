package postgres_test

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

//go:embed migrations/00001_local_foundation.sql migrations/00002_local_staff_identity.sql migrations/00003_local_catalog.sql migrations/00004_staff_catalog_operations.sql migrations/00005_local_cart.sql migrations/00006_local_cart_execution.sql migrations/00007_local_cart_fulfillment.sql migrations/00008_local_summary_preview.sql
var beforeControlFiles embed.FS

func TestConversationControlMigrationPopulatedV8AndConstraints(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg.ConnConfig)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA app"); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(beforeControlFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithTableName("app.goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	const restaurant = "c0000000-0000-4000-8000-000000000009"
	const other = "c0000000-0000-4000-8000-000000000019"
	const menu = "10000000-0000-4000-8000-000000000001"
	members, err := staff.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	if ok, err := members.EnsureMember(ctx, "actor"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	cat, err := catalog.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if err := cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: menu, Name: "Fictional", PriceMinor: 650, Currency: "USD", Available: true}}); err != nil {
		t.Fatal(err)
	}
	s, err := cart.OpenStore(ctx, dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := s.ProvisionLocal(ctx, "fictional", "sender")
	if err != nil {
		t.Fatal(err)
	}
	op, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Create(ctx, local, op)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := cart.ParseMenuID(menu)
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
	jobs, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	finishLegacyFormat := pre11LegacyJobFormat(t, dsn)
	now := time.Now()
	event := foundation.Event{ID: "before-control", Sender: "sender", Text: "opaque"}
	if _, err := jobs.Accept(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobs.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	choice := cart.PickupFulfillment()
	call, err := cart.SetFulfillmentCall(added.Cart.ID, added.Cart.Version, choice)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterExecution(ctx, local, event.ID, 0, call)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.ExecuteDraft(ctx, local, plan, job)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := cart.CaptureSummaryPreviewCall(selected.Cart.ID, selected.Cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	capturePlan, err := s.RegisterExecution(ctx, local, event.ID, 1, capture)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.ExecuteSummaryPreview(ctx, local, capturePlan, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.CompleteJob(ctx, job, "exact preexisting reply", now); err != nil {
		t.Fatal(err)
	}
	finishLegacyFormat()
	beforeEvent, err := jobs.Inspect(ctx, event.ID)
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
		for _, table := range []string{"conversation_sequences", "inbound_events", "jobs", "outbox", "send_attempts", "restaurants", "staff_memberships", "restaurant_settings", "menu_items", "operations", "cart_conversations", "carts", "cart_units", "cart_operations", "cart_execution_sources", "cart_execution_calls", "cart_fulfillments", "cart_summary_previews"} {
			var h string
			if err := conn.QueryRow(ctx, legacyMigrationFingerprintSQL(table)).Scan(&h); err != nil {
				t.Fatal(err)
			}
			out[table] = h
		}
		return out
	}
	before := hashes()
	var version int64
	if err := conn.QueryRow(ctx, `SELECT max(version_id) FROM app.goose_db_version`).Scan(&version); err != nil || version != 8 {
		t.Fatal(version, err)
	}
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"conversation_controls", "conversation_control_operations"} {
		var n int64
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM app.`+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("new table %s count %d: %v", table, n, err)
		}
	}
	var applied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.goose_db_version WHERE version_id=9 AND is_applied)`).Scan(&applied); err != nil || !applied {
		t.Fatal("v9 applied", applied, err)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("upgrade changed old rows")
	}
	assertMigratedLegacyMetadata(t, conn)
	afterEvent, err := jobs.Inspect(ctx, event.ID)
	if err != nil || !reflect.DeepEqual(afterEvent, beforeEvent) {
		t.Fatalf("foundation changed=%+v %v", afterEvent, err)
	}
	read, err := s.Read(ctx, local, base.Cart.ID)
	if err != nil || !reflect.DeepEqual(read, selected.Cart) {
		t.Fatal(read, err)
	}
	replay, err := s.Apply(ctx, local, addOp, base.Cart.ID, base.Cart.Version, add)
	if err != nil || !reflect.DeepEqual(replay, added) {
		t.Fatal(replay, err)
	}
	// Supplement the legacy public read/replay checks with the saved summary bytes.
	var stored string
	if err := conn.QueryRow(ctx, `SELECT result FROM app.cart_operations WHERE restaurant_id=$1 AND operation_id=$2`, restaurant, preview.OperationID.String()).Scan(&stored); err != nil || stored == "" {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("legacy read/replay changed bytes")
	}
	id := local.ConversationID().String()
	control := `INSERT INTO app.conversation_controls(restaurant_id,conversation_id,mode,version,updated_at) VALUES($1,$2,$3,$4,$5)`
	reject := func(name, sql string, args ...any) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				c, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = tx.Rollback(c)
			}()
			_, err = tx.Exec(ctx, sql, args...)
			var constraint *pgconn.PgError
			if !errors.As(err, &constraint) || !strings.HasPrefix(constraint.Code, "23") {
				t.Fatalf("want an integrity constraint rejection, got %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name, mode string
		version    int64
	}{{"zero mode", "", 0}, {"unknown mode", "human", 0}, {"negative version", "ai", -1}} {
		reject(tc.name, control, restaurant, id, tc.mode, tc.version, now)
	}
	reject("foreign conversation", control, other, id, "ai", 0, now)
	reject("dangling conversation", control, restaurant, "90000000-0000-4000-8000-000000000001", "ai", 0, now)
	reject("explicit timestamp required", control, restaurant, id, "ai", 0, nil)
	reject("nonnull mode", control, restaurant, id, nil, 0, now)
	reject("nonnull version", control, restaurant, id, "ai", nil, now)
	if _, err := conn.Exec(ctx, control, restaurant, id, "ai", 0, now.UTC()); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO app.conversation_control_operations(restaurant_id,auth_user_id,operation_key,conversation_id,action,target_mode,expected_version,result_mode,result_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	type intent struct {
		actor, key, id, action, target string
		expected                       any
		result                         string
		version                        any
	}
	valid := intent{"actor", "key", id, "initialize", "ai", nil, "ai", int64(0)}
	args := func(i intent) []any {
		return []any{restaurant, i.actor, i.key, i.id, i.action, i.target, i.expected, i.result, i.version, now.UTC()}
	}
	cases := []struct {
		name string
		edit func(*intent)
	}{
		{"missing actor", func(i *intent) { i.actor = "unknown" }},
		{"empty key", func(i *intent) { i.key = "" }}, {"unicode key", func(i *intent) { i.key = "é" }},
		{"newline key", func(i *intent) { i.key = "a\n" }},
		{"invalid key punctuation", func(i *intent) { i.key = "a/b" }}, {"long key", func(i *intent) { i.key = strings.Repeat("a", 129) }},
		{"dangling control", func(i *intent) { i.id = "90000000-0000-4000-8000-000000000001" }},
		{"unknown action", func(i *intent) { i.action = "reset" }}, {"invalid target", func(i *intent) { i.target = "human" }},
		{"result differs", func(i *intent) { i.result = "staff" }}, {"init expected present", func(i *intent) { i.expected = int64(0) }},
		{"init result nonzero", func(i *intent) { i.version = int64(1) }}, {"result null", func(i *intent) { i.version = nil }},
		{"set expected absent", func(i *intent) { i.action = "set_mode"; i.version = int64(1) }},
		{"set expected negative", func(i *intent) { i.action = "set_mode"; i.expected = int64(-1) }},
		{"set result mismatch", func(i *intent) { i.action = "set_mode"; i.expected = int64(1); i.version = int64(1) }},
		{"safe maximum", func(i *intent) { i.action = "set_mode"; i.expected = int64(9223372036854775807); i.version = int64(0) }},
	}
	for _, tc := range cases {
		i := valid
		tc.edit(&i)
		reject(tc.name, insert, args(i)...)
	}
	missingTime := args(valid)
	missingTime[9] = nil
	reject("operation timestamp required", insert, missingTime...)
	if _, err := conn.Exec(ctx, insert, args(valid)...); err != nil {
		t.Fatal(err)
	}
	reject("unique actor restaurant key", insert, args(valid)...)
	next := valid
	next.key = "set"
	next.action = "set_mode"
	next.expected = int64(0)
	next.version = int64(1)
	if _, err := conn.Exec(ctx, insert, args(next)...); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM app.conversation_control_operations`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("new controls changed old rows")
	}
	t.Log(fmt.Sprintf("populated v8 -> v9 preserved %d legacy table fingerprints; constrained ledger rows=%d", len(before), n))
	// Only after the final historical comparison, deliver the preserved pending
	// v8 intent through the current public API on the upgraded schema.
	delivery, ok, err := jobs.ClaimDelivery(ctx, time.Now(), time.Minute)
	if err != nil || !ok || delivery.EventID != event.ID || delivery.Text != "exact preexisting reply" || delivery.Generation != 1 || delivery.Attempt != 1 {
		t.Fatal(delivery, ok, err)
	}
	if err := jobs.FinishDelivery(ctx, delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "post-control-provider"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	accepted, err := jobs.Inspect(ctx, event.ID)
	wantAccepted := beforeEvent
	wantAccepted.OutboxState = "accepted"
	wantAccepted.ProviderID = "post-control-provider"
	if err != nil || !reflect.DeepEqual(accepted, wantAccepted) {
		t.Fatal(accepted, err)
	}
}
