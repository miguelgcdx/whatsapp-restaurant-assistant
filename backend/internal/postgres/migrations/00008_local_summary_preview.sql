-- +goose Up
-- LOCAL immutable draft snapshots only, not order candidates or presentation.
-- No defaults, seeds, unit FKs, cascades, current pointer or cleanup.
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_operation_scope_key
    UNIQUE (operation_id, restaurant_id, conversation_id);
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check
    CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote','capture_preview','read_preview'));
ALTER TABLE app.cart_operations DROP CONSTRAINT cart_operations_action_check;
ALTER TABLE app.cart_operations ADD CONSTRAINT cart_operations_action_check
    CHECK (action IN ('create','add','remove','set_note','set_fulfillment','capture_preview'));
CREATE TABLE app.cart_summary_previews (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    cart_id uuid NOT NULL,
    source_cart_version bigint NOT NULL CHECK (source_cart_version >= 0),
    source_operation_id uuid NOT NULL UNIQUE,
    captured_at timestamptz NOT NULL,
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 524288 AND jsonb_typeof(body::jsonb) = 'object'),
    FOREIGN KEY (restaurant_id, conversation_id, cart_id) REFERENCES app.carts(restaurant_id, conversation_id, id),
    FOREIGN KEY (source_operation_id, restaurant_id, conversation_id) REFERENCES app.cart_execution_calls(operation_id, restaurant_id, conversation_id)
);
