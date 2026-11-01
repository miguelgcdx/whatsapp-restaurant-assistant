package postgres_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

// SQL is the public persisted-format boundary here, not domain behavior. Reuse
// the existing isolated migration runner and frozen old-format source fixture.
func TestAmbiguousEditMigrationPreservesV13AndEmptyDown(t *testing.T) {
	dsn := testpg.Start(t)
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	if _, err := p.UpTo(t.Context(), 13); err != nil {
		t.Fatal(err)
	}
	c := claimConn(t, dsn)
	seedAIFormat(t, c)
	before := menuRows(t, c, true)
	schema := menuRows(t, c, false)["schema"]
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 14)
	after := menuRows(t, c, true)
	if rows, ok := after["unresolved_cart_edits"]; !ok || rows != "[]" {
		t.Fatalf("unexpected marker backfill: %q", rows)
	}
	delete(after, "unresolved_cart_edits")
	if !reflect.DeepEqual(after, before) {
		t.Fatal("Up changed old rows/counters", before, after)
	}
	if _, err := p.DownTo(t.Context(), 13); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 13)
	if !reflect.DeepEqual(menuRows(t, c, true), before) || menuRows(t, c, false)["schema"] != schema {
		t.Fatal("empty Down changed v13 rows/schema")
	}
}

func TestAmbiguousEditMigrationDescriptorHistoryRefusesDown(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	c := claimConn(t, dsn)
	seedAIFormat(t, c)
	if _, err := c.Exec(t.Context(), `INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) SELECT event_id,0,restaurant_id,conversation_id,'mark_unresolved_edit','{"action":"mark_unresolved_edit"}',decode(repeat('00',32),'hex'),'b0000000-0000-4000-8000-000000000001',clock_timestamp() FROM app.cart_execution_sources`); err != nil {
		t.Fatal(err)
	}
	before := menuRows(t, c, false)
	if _, err := p.DownTo(t.Context(), 13); err == nil || !strings.Contains(err.Error(), "Ambiguous edit history prevents migration 14 rollback") {
		t.Fatalf("descriptor-only history must prevent Down: %v", err)
	}
	assertMigrationVersion(t, p, 14)
	if !reflect.DeepEqual(menuRows(t, c, false), before) {
		t.Fatal("refused Down changed rows/schema/bookkeeping")
	}
}

func TestAmbiguousEditMigrationConstraintsAndAtomicResolution(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	c := claimConn(t, dsn)
	seedAIFormat(t, c)
	for _, sql := range []string{
		`INSERT INTO app.carts(id,restaurant_id,conversation_id,currency,exponent,version,created_at) SELECT 'b0000000-0000-4000-8000-000000000002',restaurant_id,conversation_id,'USD',2,0,clock_timestamp() FROM app.cart_execution_sources`,
		`INSERT INTO app.jobs(id,event_id,state,due_at,attempt,generation,route,restaurant_id,conversation_id,admission_mode,admission_version) SELECT 'b0000000-0000-4000-8000-000000000003',event_id,'completed',clock_timestamp(),1,1,'menu_message',restaurant_id,conversation_id,'ai',0 FROM app.cart_execution_sources`,
		`INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) SELECT event_id,0,restaurant_id,conversation_id,'mark_unresolved_edit','{"action":"mark_unresolved_edit"}',decode(repeat('00',32),'hex'),'b0000000-0000-4000-8000-000000000004',clock_timestamp() FROM app.cart_execution_sources`,
		`INSERT INTO app.unresolved_cart_edits(event_id,runtime_slot,operation_id,restaurant_id,conversation_id,cart_id,expected_version,kind,original_request,admission_job_id,control_version,admission_generation,admission_attempt,created_at) SELECT event_id,runtime_slot,operation_id,restaurant_id,conversation_id,'b0000000-0000-4000-8000-000000000002',0,'remove','question','b0000000-0000-4000-8000-000000000003',0,1,1,clock_timestamp() FROM app.cart_execution_calls`,
		`INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) SELECT event_id,1,restaurant_id,conversation_id,'remove','{"action":"remove","resolution":{}}',decode(repeat('00',32),'hex'),'b0000000-0000-4000-8000-000000000005',clock_timestamp() FROM app.cart_execution_sources`,
		`INSERT INTO app.native_cart_actions(event_id,runtime_slot,operation_id,restaurant_id,conversation_id,admission_job_id,admission_version,admission_generation,admission_attempt,admitted_at,due_at,unresolved_operation) SELECT event_id,runtime_slot,operation_id,restaurant_id,conversation_id,'b0000000-0000-4000-8000-000000000003',0,1,1,clock_timestamp(),clock_timestamp(),'b0000000-0000-4000-8000-000000000004' FROM app.cart_execution_calls WHERE runtime_slot=1`,
	} {
		if _, err := c.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	before := menuRows(t, c, false)
	for _, tc := range []struct{ name, sql, code string }{
		{"duplicate-identity", `INSERT INTO app.unresolved_cart_edits SELECT * FROM app.unresolved_cart_edits`, "23505"},
		{"bad-kind", `INSERT INTO app.unresolved_cart_edits SELECT event_id,runtime_slot,operation_id,restaurant_id,conversation_id,cart_id,expected_version,'replace',original_request,admission_job_id,control_version,admission_generation,admission_attempt,created_at,resolved_operation,resolved_at FROM app.unresolved_cart_edits`, "23514"},
		{"immutable-request", `UPDATE app.unresolved_cart_edits SET original_request='rewritten'`, "P0001"},
		{"no-delete", `DELETE FROM app.unresolved_cart_edits`, "P0001"},
		{"immutable-call", `UPDATE app.cart_execution_calls SET canonical_intent='{}' WHERE runtime_slot=0`, "P0001"},
		{"no-link-stripping", `UPDATE app.native_cart_actions SET unresolved_operation=NULL`, "P0001"},
		{"wrong-native-reference", `UPDATE app.unresolved_cart_edits SET resolved_operation='b0000000-0000-4000-8000-000000000099',resolved_at=clock_timestamp()`, "23503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Exec(t.Context(), tc.sql)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != tc.code {
				t.Fatalf("error=%v want SQLSTATE %s", err, tc.code)
			}
			if !reflect.DeepEqual(menuRows(t, c, false), before) {
				t.Fatal("rejection changed schema/data/counters")
			}
		})
	}
	// A valid native link alone is insufficient: deferred FK checks the cart
	// operation result at COMMIT and rolls back the staged transition without it.
	tx, err := c.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	const resolve = `UPDATE app.unresolved_cart_edits SET resolved_operation='b0000000-0000-4000-8000-000000000005',resolved_at=clock_timestamp()`
	if _, err = tx.Exec(t.Context(), resolve); err != nil {
		t.Fatal(err)
	}
	err = tx.Commit(t.Context())
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23503" {
		t.Fatalf("resolution escaped without result: %v", err)
	}
	if !reflect.DeepEqual(menuRows(t, c, false), before) {
		t.Fatal("deferred failure leaked staged resolution")
	}
	tx, err = c.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), resolve); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO app.cart_operations(restaurant_id,conversation_id,operation_id,cart_id,action,fingerprint,result,created_at) SELECT restaurant_id,conversation_id,resolved_operation,cart_id,'remove',decode(repeat('00',32),'hex'),'{}',clock_timestamp() FROM app.unresolved_cart_edits`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE app.native_cart_actions SET committed_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	resolved := menuRows(t, c, false)
	if _, err = c.Exec(t.Context(), `UPDATE app.unresolved_cart_edits SET resolved_operation=NULL,resolved_at=NULL`); err == nil {
		t.Fatal("resolved record reopened")
	}
	if !reflect.DeepEqual(menuRows(t, c, false), resolved) {
		t.Fatal("reopening rejection changed history")
	}
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	if _, err = p.DownTo(t.Context(), 13); err == nil || !strings.Contains(err.Error(), "Ambiguous edit history prevents migration 14 rollback") {
		t.Fatalf("resolved history lost on Down: %v", err)
	}
	if !reflect.DeepEqual(menuRows(t, c, false), resolved) {
		t.Fatal("refused history Down changed state")
	}
}
