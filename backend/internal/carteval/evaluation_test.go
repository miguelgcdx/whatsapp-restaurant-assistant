package carteval_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
)

// This tests the evidence envelope only; it emits no fabricated case receipt.
func TestEvaluationRecordSchema(t *testing.T) {
	id, err := cart.ParseCartID("30000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := cart.ParseConversationID("40000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	unit, err := cart.ParseUnitID("20000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	menu, err := cart.ParseMenuID(burger)
	if err != nil {
		t.Fatal(err)
	}
	version, err := cart.ParseVersion(1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cart.Snapshot{ID: id, ConversationID: conversation, Version: version, Currency: "USD", Exponent: 2, Subtotal: 700, Units: []cart.Unit{{ID: unit, MenuID: menu, PriceMinor: 700, Currency: "USD", Exponent: 2, Note: "synthetic note"}}}
	zero := true
	r := record{SchemaVersion: 1, Candidate: "carteval-fixture-v1", Case: "schema-only", Variant: "serialization", Level: "test-helper", Script: "not executed", Expected: "round-trip public snapshot", Actual: "schema assertion only", Component: "NOT_RUN", Outcome: "NOT_RUN", ZeroCall: &zero, Before: &snapshot, Pending: &snapshot, After: &snapshot, PendingOperation: "synthetic-reference-metadata", PendingState: "PENDING, not execution"}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal("safe record serialization failed")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema_version", "candidate", "case", "variant", "level", "actual_script_not_original_NL", "expected", "actual", "error_identity", "zero_call", "component_outcome", "outcome", "gates", "invocations_excluding_setup", "returned_outcomes_not_mutation_counts", "unavailable_observations", "before", "pending_or_rejected", "after", "pending_operation_id", "pending_reference_state"} {
		require(t, len(fields[name]) > 0, "missing record field: "+name)
	}
	require(t, fields["execute_result"] == nil, "pending metadata fabricated an execution result")
	var decoded record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal("safe record round-trip failed")
	}
	require(t, decoded.SchemaVersion == 1 && decoded.Component == "NOT_RUN" && decoded.Outcome == "NOT_RUN", "serialization promoted unexecuted evidence")
	require(t, decoded.ZeroCall != nil && *decoded.ZeroCall, "explicit zero-call result lost")
	require(t, reflect.DeepEqual(decoded.Before, &snapshot) && reflect.DeepEqual(decoded.Pending, &snapshot) && reflect.DeepEqual(decoded.After, &snapshot), "public IDs/version/units/prices/notes did not round-trip")
	// Zero invocations and zero outcomes must remain visible, not omitted or
	// conflated with an executed result or a durable-mutation observation.
	for _, name := range []string{"invocations_excluding_setup", "returned_outcomes_not_mutation_counts"} {
		var values map[string]int
		if err := json.Unmarshal(fields[name], &values); err != nil {
			t.Fatal(err)
		}
		require(t, len(values) > 0, "missing zero boundary counters")
		for _, value := range values {
			require(t, value == 0, "serialization invented a boundary call/outcome")
		}
	}
}
