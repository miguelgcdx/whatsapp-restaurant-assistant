-- +goose Up
-- Explicit immutable local execution plans, not pending mutation results.
-- No seeds, defaults, TTL, cleanup or cascading deletion.
CREATE TABLE app.cart_execution_sources (
    event_id uuid PRIMARY KEY REFERENCES app.inbound_events(id),
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (event_id, restaurant_id, conversation_id),
    FOREIGN KEY (restaurant_id, conversation_id) REFERENCES app.cart_conversations(restaurant_id, id)
);
CREATE TABLE app.cart_execution_calls (
    event_id uuid NOT NULL,
    runtime_slot bigint NOT NULL CHECK (runtime_slot >= 0),
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('create','read','add','remove','set_note')),
    canonical_intent text NOT NULL CHECK (octet_length(canonical_intent) BETWEEN 1 AND 524288 AND jsonb_typeof(canonical_intent::jsonb) = 'object'),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    operation_id uuid NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (event_id, runtime_slot),
    FOREIGN KEY (event_id, restaurant_id, conversation_id) REFERENCES app.cart_execution_sources(event_id, restaurant_id, conversation_id)
);
