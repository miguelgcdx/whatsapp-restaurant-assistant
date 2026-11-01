-- +goose Up
-- Additive explicit clarification state. No backfill, inference, TTL or cascades.
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote','capture_preview','read_preview','prepare_pickup_summary','submit_pickup_order','mark_unresolved_edit'));
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT ambiguous_call_identity UNIQUE(event_id,runtime_slot,operation_id,restaurant_id,conversation_id);
ALTER TABLE app.cart_operations ADD CONSTRAINT ambiguous_result_identity UNIQUE(restaurant_id,conversation_id,operation_id,cart_id);

CREATE TABLE app.unresolved_cart_edits (
 event_id uuid NOT NULL,
 runtime_slot bigint NOT NULL CHECK(runtime_slot>=0),
 operation_id uuid PRIMARY KEY,
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 cart_id uuid NOT NULL,
 expected_version bigint NOT NULL CHECK(expected_version>=0),
 kind text NOT NULL CHECK(kind IN ('add','remove','set_note')),
 original_request text NOT NULL CHECK(char_length(btrim(original_request)) BETWEEN 1 AND 4000),
 admission_job_id uuid NOT NULL REFERENCES app.jobs(id),
 control_version bigint NOT NULL CHECK(control_version>=0),
 admission_generation bigint NOT NULL CHECK(admission_generation>0),
 admission_attempt bigint NOT NULL CHECK(admission_attempt>0),
 created_at timestamptz NOT NULL CHECK(isfinite(created_at)),
 resolved_operation uuid,
 resolved_at timestamptz CHECK(isfinite(resolved_at)),
 UNIQUE(event_id,runtime_slot),
 UNIQUE(operation_id,restaurant_id,conversation_id),
 FOREIGN KEY(event_id,runtime_slot,operation_id,restaurant_id,conversation_id)
  REFERENCES app.cart_execution_calls(event_id,runtime_slot,operation_id,restaurant_id,conversation_id),
 FOREIGN KEY(restaurant_id,conversation_id,cart_id) REFERENCES app.carts(restaurant_id,conversation_id,id),
 CHECK((resolved_operation IS NULL)=(resolved_at IS NULL))
);
CREATE INDEX unresolved_cart_edits_active ON app.unresolved_cart_edits(restaurant_id,conversation_id,cart_id,event_id,runtime_slot) WHERE resolved_operation IS NULL;

ALTER TABLE app.native_cart_actions ADD COLUMN unresolved_operation uuid;
ALTER TABLE app.native_cart_actions ADD CONSTRAINT native_unresolved_scope FOREIGN KEY(unresolved_operation,restaurant_id,conversation_id) REFERENCES app.unresolved_cart_edits(operation_id,restaurant_id,conversation_id);
ALTER TABLE app.native_cart_actions ADD CONSTRAINT native_unresolved_identity UNIQUE(operation_id,unresolved_operation,restaurant_id,conversation_id);
ALTER TABLE app.unresolved_cart_edits ADD CONSTRAINT unresolved_edit_native_link FOREIGN KEY(resolved_operation,operation_id,restaurant_id,conversation_id) REFERENCES app.native_cart_actions(operation_id,unresolved_operation,restaurant_id,conversation_id);
-- Application owns commit: staging resolution before apply cannot escape without
-- the corresponding actual cart operation result in the very same transaction.
ALTER TABLE app.unresolved_cart_edits ADD CONSTRAINT unresolved_edit_native_result FOREIGN KEY(restaurant_id,conversation_id,resolved_operation,cart_id) REFERENCES app.cart_operations(restaurant_id,conversation_id,operation_id,cart_id) DEFERRABLE INITIALLY DEFERRED;

-- +goose StatementBegin
CREATE FUNCTION app.ambiguous_edit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable ambiguous edit history'; END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION app.ambiguous_edit_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.resolved_operation IS NOT NULL
 OR NEW.resolved_operation IS NULL OR NEW.resolved_at IS NULL
 OR ROW(NEW.event_id,NEW.runtime_slot,NEW.operation_id,NEW.restaurant_id,NEW.conversation_id,NEW.cart_id,NEW.expected_version,NEW.kind,NEW.original_request,NEW.admission_job_id,NEW.control_version,NEW.admission_generation,NEW.admission_attempt,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.event_id,OLD.runtime_slot,OLD.operation_id,OLD.restaurant_id,OLD.conversation_id,OLD.cart_id,OLD.expected_version,OLD.kind,OLD.original_request,OLD.admission_job_id,OLD.control_version,OLD.admission_generation,OLD.admission_attempt,OLD.created_at)
 THEN RAISE EXCEPTION 'invalid ambiguous edit transition'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER unresolved_edit_transition BEFORE UPDATE ON app.unresolved_cart_edits FOR EACH ROW EXECUTE FUNCTION app.ambiguous_edit_transition();
CREATE TRIGGER unresolved_edit_no_delete BEFORE DELETE ON app.unresolved_cart_edits FOR EACH ROW EXECUTE FUNCTION app.ambiguous_edit_immutable();
CREATE TRIGGER ambiguous_call_immutable BEFORE UPDATE OR DELETE ON app.cart_execution_calls FOR EACH ROW WHEN (OLD.action='mark_unresolved_edit' OR OLD.canonical_intent::jsonb ? 'resolution') EXECUTE FUNCTION app.ambiguous_edit_immutable();
CREATE TRIGGER ambiguous_native_link_immutable BEFORE UPDATE OF unresolved_operation ON app.native_cart_actions FOR EACH ROW WHEN (OLD.unresolved_operation IS DISTINCT FROM NEW.unresolved_operation) EXECUTE FUNCTION app.ambiguous_edit_immutable();
CREATE TRIGGER ambiguous_native_no_delete BEFORE DELETE ON app.native_cart_actions FOR EACH ROW WHEN (OLD.unresolved_operation IS NOT NULL) EXECUTE FUNCTION app.ambiguous_edit_immutable();

-- +goose Down
-- Refuse before any DDL, including descriptor-only registrations.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM app.unresolved_cart_edits)
 OR EXISTS(SELECT 1 FROM app.native_cart_actions WHERE unresolved_operation IS NOT NULL)
 OR EXISTS(SELECT 1 FROM app.cart_execution_calls WHERE action='mark_unresolved_edit' OR canonical_intent::jsonb ? 'resolution')
 THEN RAISE EXCEPTION 'Ambiguous edit history prevents migration 14 rollback'; END IF;
END; $$;
-- +goose StatementEnd
DROP TRIGGER ambiguous_call_immutable ON app.cart_execution_calls;
DROP TRIGGER ambiguous_native_link_immutable ON app.native_cart_actions;
DROP TRIGGER ambiguous_native_no_delete ON app.native_cart_actions;
ALTER TABLE app.unresolved_cart_edits DROP CONSTRAINT unresolved_edit_native_link;
ALTER TABLE app.unresolved_cart_edits DROP CONSTRAINT unresolved_edit_native_result;
ALTER TABLE app.native_cart_actions DROP CONSTRAINT native_unresolved_identity;
ALTER TABLE app.native_cart_actions DROP CONSTRAINT native_unresolved_scope;
ALTER TABLE app.native_cart_actions DROP COLUMN unresolved_operation;
DROP TABLE app.unresolved_cart_edits;
DROP FUNCTION app.ambiguous_edit_transition();
DROP FUNCTION app.ambiguous_edit_immutable();
ALTER TABLE app.cart_operations DROP CONSTRAINT ambiguous_result_identity;
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT ambiguous_call_identity;
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote','capture_preview','read_preview','prepare_pickup_summary','submit_pickup_order'));
