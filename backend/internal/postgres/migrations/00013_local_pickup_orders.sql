-- +goose Up
-- Additive LOCAL pickup only. No seeds, auth-private references or cascades.
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote','capture_preview','read_preview','prepare_pickup_summary','submit_pickup_order'));
ALTER TABLE app.cart_operations DROP CONSTRAINT cart_operations_action_check;
ALTER TABLE app.cart_operations ADD CONSTRAINT cart_operations_action_check CHECK (action IN ('create','add','remove','set_note','set_fulfillment','capture_preview','prepare_pickup_summary','submit_pickup_order'));
CREATE TABLE app.pickup_candidates (
 id uuid PRIMARY KEY,
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 cart_id uuid NOT NULL,
 cart_version bigint NOT NULL CHECK (cart_version>=0),
 control_version bigint NOT NULL CHECK (control_version>=0),
 operation_id uuid NOT NULL UNIQUE,
 created_at timestamptz NOT NULL CHECK (isfinite(created_at)),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 524288 AND jsonb_typeof(body::jsonb)='object'),
 UNIQUE (restaurant_id,conversation_id,id),
 FOREIGN KEY (restaurant_id,conversation_id,cart_id) REFERENCES app.carts(restaurant_id,conversation_id,id),
 FOREIGN KEY (operation_id,restaurant_id,conversation_id) REFERENCES app.cart_execution_calls(operation_id,restaurant_id,conversation_id)
);
CREATE TABLE app.pickup_candidate_units (
 candidate_id uuid NOT NULL REFERENCES app.pickup_candidates(id),
 position bigint NOT NULL CHECK (position>=0),
 unit_id uuid NOT NULL,
 menu_id uuid NOT NULL,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
 note text NOT NULL CHECK (char_length(note)<=500),
 price_minor bigint NOT NULL CHECK (price_minor>=0),
 PRIMARY KEY(candidate_id,position), UNIQUE(candidate_id,unit_id)
);
CREATE TABLE app.pickup_presentations (
 id uuid PRIMARY KEY,
 candidate_id uuid NOT NULL UNIQUE REFERENCES app.pickup_candidates(id),
 intent_id uuid NOT NULL UNIQUE,
 receiver_reference text NOT NULL UNIQUE CHECK (octet_length(receiver_reference) BETWEEN 1 AND 256),
 text_digest bytea NOT NULL CHECK (octet_length(text_digest)=32),
 received_at timestamptz NOT NULL CHECK (isfinite(received_at)),
 after_sequence bigint NOT NULL CHECK (after_sequence>0)
);
CREATE TABLE app.pickup_orders (
 id uuid PRIMARY KEY,
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 cart_id uuid NOT NULL UNIQUE,
 candidate_id uuid NOT NULL UNIQUE,
 operation_id uuid NOT NULL UNIQUE,
 created_at timestamptz NOT NULL CHECK (isfinite(created_at)),
 version bigint NOT NULL CHECK (version>=0),
 status text NOT NULL CHECK (status IN ('submitted','accepted','preparing','ready','picked_up','rejected','cancelled')),
 payment text NOT NULL CHECK (payment IN ('unpaid','paid')),
 collected_at timestamptz CHECK (isfinite(collected_at)),
 queue_sequence bigint CHECK (queue_sequence>0),
 preparation_started_at timestamptz CHECK (isfinite(preparation_started_at)),
 estimate text NOT NULL CHECK (octet_length(estimate)<=512 AND (estimate='null' OR jsonb_typeof(estimate::jsonb)='object')),
 UNIQUE(restaurant_id,id), UNIQUE(restaurant_id,queue_sequence),
 FOREIGN KEY (restaurant_id,conversation_id,candidate_id) REFERENCES app.pickup_candidates(restaurant_id,conversation_id,id),
 FOREIGN KEY (restaurant_id,conversation_id,cart_id) REFERENCES app.carts(restaurant_id,conversation_id,id),
 FOREIGN KEY (operation_id,restaurant_id,conversation_id) REFERENCES app.cart_execution_calls(operation_id,restaurant_id,conversation_id),
 CHECK ((payment='unpaid' AND collected_at IS NULL) OR (payment='paid' AND collected_at IS NOT NULL)),
 CHECK ((status IN ('accepted','preparing','ready','picked_up') AND queue_sequence IS NOT NULL) OR (status IN ('submitted','rejected','cancelled') AND queue_sequence IS NULL)),
 CHECK ((status IN ('preparing','ready','picked_up') AND preparation_started_at IS NOT NULL) OR (status IN ('submitted','accepted','rejected','cancelled') AND preparation_started_at IS NULL))
);
CREATE TABLE app.pickup_evidence (
 event_id uuid PRIMARY KEY,
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 candidate_id uuid NOT NULL UNIQUE REFERENCES app.pickup_candidates(id),
 order_id uuid NOT NULL UNIQUE REFERENCES app.pickup_orders(id),
 presentation_id uuid NOT NULL REFERENCES app.pickup_presentations(id),
 assessment_version text NOT NULL CHECK (octet_length(assessment_version) BETWEEN 1 AND 128),
 context_digest bytea NOT NULL CHECK (octet_length(context_digest)=32),
 context_body text NOT NULL CHECK (octet_length(context_body) BETWEEN 1 AND 131072),
 FOREIGN KEY(event_id,restaurant_id,conversation_id) REFERENCES app.cart_execution_sources(event_id,restaurant_id,conversation_id)
);
CREATE TABLE app.pickup_intents (
 id uuid PRIMARY KEY,
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 candidate_id uuid NOT NULL,
 order_id uuid REFERENCES app.pickup_orders(id),
 kind text NOT NULL CHECK (kind IN ('summary','receipt','status')),
 content text NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 524288),
 created_at timestamptz NOT NULL CHECK (isfinite(created_at)),
 FOREIGN KEY(restaurant_id,conversation_id,candidate_id) REFERENCES app.pickup_candidates(restaurant_id,conversation_id,id),
 CHECK ((kind='summary' AND order_id IS NULL) OR (kind IN ('receipt','status') AND order_id IS NOT NULL))
);
ALTER TABLE app.pickup_presentations ADD FOREIGN KEY(intent_id) REFERENCES app.pickup_intents(id);
CREATE UNIQUE INDEX pickup_one_summary ON app.pickup_intents(candidate_id) WHERE kind='summary';
CREATE UNIQUE INDEX pickup_one_receipt ON app.pickup_intents(order_id) WHERE kind='receipt';
CREATE TABLE app.pickup_staff_operations (
 restaurant_id uuid NOT NULL,
 auth_user_id text NOT NULL,
 method text NOT NULL CHECK (method='POST'),
 resource text NOT NULL,
 operation_key text NOT NULL CHECK (octet_length(operation_key) BETWEEN 1 AND 128 AND operation_key COLLATE "C" ~ '^[!-~]+$'),
 fingerprint bytea NOT NULL CHECK (octet_length(fingerprint)=32),
 result text NOT NULL CHECK (octet_length(result) BETWEEN 1 AND 524288 AND jsonb_typeof(result::jsonb)='object'),
 PRIMARY KEY(restaurant_id,auth_user_id,method,resource,operation_key),
 FOREIGN KEY(restaurant_id,auth_user_id) REFERENCES app.staff_memberships(restaurant_id,auth_user_id)
);
-- Acknowledged exact existing outbound text, not queued/provider-accepted output.
CREATE TABLE app.pickup_local_replies (
 outbox_id uuid PRIMARY KEY REFERENCES app.outbox(id),
 restaurant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 receiver_reference text NOT NULL UNIQUE CHECK (octet_length(receiver_reference) BETWEEN 1 AND 256),
 text_digest bytea NOT NULL CHECK (octet_length(text_digest)=32),
 received_at timestamptz NOT NULL CHECK (isfinite(received_at)),
 after_sequence bigint NOT NULL CHECK (after_sequence>0),
 FOREIGN KEY(restaurant_id,conversation_id) REFERENCES app.cart_conversations(restaurant_id,id)
);
-- +goose StatementBegin
CREATE FUNCTION app.pickup_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable pickup history'; END;
$$;
-- +goose StatementEnd
CREATE TRIGGER pickup_candidate_immutable BEFORE UPDATE OR DELETE ON app.pickup_candidates FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_units_immutable BEFORE UPDATE OR DELETE ON app.pickup_candidate_units FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_presentation_immutable BEFORE UPDATE OR DELETE ON app.pickup_presentations FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_evidence_immutable BEFORE UPDATE OR DELETE ON app.pickup_evidence FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_intent_immutable BEFORE UPDATE OR DELETE ON app.pickup_intents FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_staff_operation_immutable BEFORE UPDATE OR DELETE ON app.pickup_staff_operations FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_local_reply_immutable BEFORE UPDATE OR DELETE ON app.pickup_local_replies FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_call_immutable BEFORE UPDATE OR DELETE ON app.cart_execution_calls FOR EACH ROW WHEN (OLD.action IN ('prepare_pickup_summary','submit_pickup_order')) EXECUTE FUNCTION app.pickup_immutable();
CREATE TRIGGER pickup_result_immutable BEFORE UPDATE OR DELETE ON app.cart_operations FOR EACH ROW WHEN (OLD.action IN ('prepare_pickup_summary','submit_pickup_order')) EXECUTE FUNCTION app.pickup_immutable();
-- +goose StatementBegin
CREATE FUNCTION app.pickup_order_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.restaurant_id,NEW.conversation_id,NEW.cart_id,NEW.candidate_id,NEW.operation_id,NEW.created_at,NEW.estimate)
    IS DISTINCT FROM ROW(OLD.id,OLD.restaurant_id,OLD.conversation_id,OLD.cart_id,OLD.candidate_id,OLD.operation_id,OLD.created_at,OLD.estimate)
 THEN RAISE EXCEPTION 'immutable pickup order identity'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER pickup_order_identity_immutable BEFORE UPDATE ON app.pickup_orders FOR EACH ROW EXECUTE FUNCTION app.pickup_order_identity_immutable();
CREATE TRIGGER pickup_order_no_delete BEFORE DELETE ON app.pickup_orders FOR EACH ROW EXECUTE FUNCTION app.pickup_immutable();
-- +goose Down
-- Refuse before any DDL; do not partially erase history or immutable intent.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM app.cart_execution_calls WHERE action IN ('prepare_pickup_summary','submit_pickup_order'))
 OR EXISTS(SELECT 1 FROM app.cart_operations WHERE action IN ('prepare_pickup_summary','submit_pickup_order'))
 OR EXISTS(SELECT 1 FROM app.pickup_candidates) OR EXISTS(SELECT 1 FROM app.pickup_candidate_units)
 OR EXISTS(SELECT 1 FROM app.pickup_presentations) OR EXISTS(SELECT 1 FROM app.pickup_orders)
 OR EXISTS(SELECT 1 FROM app.pickup_evidence) OR EXISTS(SELECT 1 FROM app.pickup_intents)
 OR EXISTS(SELECT 1 FROM app.pickup_staff_operations) OR EXISTS(SELECT 1 FROM app.pickup_local_replies)
 THEN RAISE EXCEPTION 'Pickup history prevents migration 13 rollback'; END IF;
END; $$;
-- +goose StatementEnd
DROP TABLE app.pickup_local_replies;
DROP TABLE app.pickup_staff_operations;
DROP TABLE app.pickup_evidence;
DROP TABLE app.pickup_presentations;
DROP TABLE app.pickup_intents;
DROP TABLE app.pickup_orders;
DROP TABLE app.pickup_candidate_units;
DROP TABLE app.pickup_candidates;
DROP FUNCTION app.pickup_order_identity_immutable();
DROP TRIGGER pickup_call_immutable ON app.cart_execution_calls;
DROP TRIGGER pickup_result_immutable ON app.cart_operations;
DROP FUNCTION app.pickup_immutable();
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote','capture_preview','read_preview'));
ALTER TABLE app.cart_operations DROP CONSTRAINT cart_operations_action_check;
ALTER TABLE app.cart_operations ADD CONSTRAINT cart_operations_action_check CHECK (action IN ('create','add','remove','set_note','set_fulfillment','capture_preview'));
