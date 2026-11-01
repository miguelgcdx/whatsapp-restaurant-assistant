package postgres_test

import (
	"reflect"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/testpg"
)

// Migration tests deliberately inspect the owned database: preserving all old
// rows and refusing destructive rollback are public persisted-format promises.
func TestPickupMigrationPreservesAndGuardsHistory(t *testing.T) {
	dsn := testpg.Start(t)
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	if _, err := p.UpTo(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	c := claimConn(t, dsn)
	seedAIFormat(t, c)
	before := menuRows(t, c, true)
	if _, err := p.UpTo(t.Context(), 13); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 13)
	after := menuRows(t, c, true)
	for _, name := range []string{"pickup_candidates", "pickup_candidate_units", "pickup_presentations", "pickup_orders", "pickup_evidence", "pickup_intents", "pickup_staff_operations", "pickup_local_replies"} {
		if data, ok := after[name]; !ok || data != "[]" {
			t.Fatalf("new table %s not empty: %q", name, data)
		}
		delete(after, name)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("Up changed old rows", before, after)
	}
	if _, err := p.DownTo(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 12)
	if !reflect.DeepEqual(before, menuRows(t, c, true)) {
		t.Fatal("empty Down changed old rows")
	}
	if _, err := p.UpTo(t.Context(), 13); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"prepare_pickup_summary", "submit_pickup_order"} {
		if _, err := c.Exec(t.Context(), `INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) SELECT event_id,$1,restaurant_id,conversation_id,$2,'{}',decode(repeat('00',32),'hex'),gen_random_uuid(),clock_timestamp() FROM app.cart_execution_sources`, len(action), action); err != nil {
			t.Fatal(err)
		}
	}
	exact := menuRows(t, c, false)
	if _, err := p.DownTo(t.Context(), 12); err == nil || !strings.Contains(err.Error(), "Pickup history prevents migration 13 rollback") {
		t.Fatalf("unguarded pickup registration rollback: %v", err)
	}
	assertMigrationVersion(t, p, 13)
	if !reflect.DeepEqual(exact, menuRows(t, c, false)) {
		t.Fatal("refused Down changed schema, rows, or bookkeeping")
	}
}
