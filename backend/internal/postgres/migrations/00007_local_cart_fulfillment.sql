-- +goose Up
-- Absence is unselected. No default, backfill, payment/fee state or cleanup.
CREATE TABLE app.cart_fulfillments (
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    cart_id uuid NOT NULL,
    kind text NOT NULL,
    address text,
    recipient_phone text,
    PRIMARY KEY (restaurant_id, conversation_id, cart_id),
    FOREIGN KEY (restaurant_id, conversation_id, cart_id) REFERENCES app.carts(restaurant_id, conversation_id, id),
    CONSTRAINT cart_fulfillments_selection_check CHECK (
        (kind = 'pickup' AND address IS NULL AND recipient_phone IS NULL)
        OR (kind = 'delivery' AND address IS NOT NULL AND recipient_phone IS NOT NULL
            AND char_length(address) BETWEEN 1 AND 1000
            AND char_length(recipient_phone) BETWEEN 1 AND 128
            -- Match Go strings.TrimSpace's Unicode White_Space set even
            -- under the owned fixture's C locale; preserve nonblank bytes.
            AND char_length(btrim(address, U&'\0009\000a\000b\000c\000d\0020\0085\00a0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200a\2028\2029\202f\205f\3000')) > 0
            AND char_length(btrim(recipient_phone, U&'\0009\000a\000b\000c\000d\0020\0085\00a0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200a\2028\2029\202f\205f\3000')) > 0)
    )
);
ALTER TABLE app.cart_operations DROP CONSTRAINT cart_operations_action_check;
ALTER TABLE app.cart_operations ADD CONSTRAINT cart_operations_action_check
    CHECK (action IN ('create','add','remove','set_note','set_fulfillment'));
ALTER TABLE app.cart_execution_calls DROP CONSTRAINT cart_execution_calls_action_check;
ALTER TABLE app.cart_execution_calls ADD CONSTRAINT cart_execution_calls_action_check
    CHECK (action IN ('create','read','add','remove','set_note','set_fulfillment','quote'));
