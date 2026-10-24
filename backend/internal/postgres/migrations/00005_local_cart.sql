-- +goose Up
-- Local fixture ownership is deliberately separate from synthetic foundation senders.
-- No seeds, implicit provisioning, cleanup, TTL, or cascading deletion.
CREATE TABLE app.cart_conversations (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL REFERENCES app.restaurants(id),
    source text NOT NULL CHECK (source = 'local_fixture'),
    business text NOT NULL CHECK (char_length(btrim(business)) > 0 AND char_length(business) <= 256),
    sender text NOT NULL CHECK (char_length(btrim(sender)) > 0 AND char_length(sender) <= 256),
    created_at timestamptz NOT NULL,
    UNIQUE (restaurant_id, id)
);
CREATE TABLE app.carts (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    exponent smallint NOT NULL CHECK (exponent BETWEEN 0 AND 6),
    version bigint NOT NULL CHECK (version >= 0),
    created_at timestamptz NOT NULL,
    UNIQUE (restaurant_id, conversation_id),
    UNIQUE (restaurant_id, conversation_id, id),
    UNIQUE (restaurant_id, conversation_id, id, currency, exponent),
    FOREIGN KEY (restaurant_id, conversation_id) REFERENCES app.cart_conversations(restaurant_id, id)
);
CREATE TABLE app.cart_units (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    cart_id uuid NOT NULL,
    menu_id uuid NOT NULL,
    position bigint NOT NULL CHECK (position >= 0),
    price_minor bigint NOT NULL CHECK (price_minor >= 0),
    currency text NOT NULL,
    exponent smallint NOT NULL,
    note text NOT NULL CHECK (char_length(note) <= 500),
    created_at timestamptz NOT NULL,
    UNIQUE (restaurant_id, conversation_id, cart_id, position),
    FOREIGN KEY (restaurant_id, conversation_id, cart_id, currency, exponent) REFERENCES app.carts(restaurant_id, conversation_id, id, currency, exponent),
    FOREIGN KEY (restaurant_id, menu_id) REFERENCES app.menu_items(restaurant_id, id)
);
CREATE TABLE app.cart_operations (
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    cart_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('create','add','remove','set_note')),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    result text NOT NULL CHECK (octet_length(result) BETWEEN 1 AND 524288 AND jsonb_typeof(result::jsonb) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, conversation_id, operation_id),
    FOREIGN KEY (restaurant_id, conversation_id, cart_id) REFERENCES app.carts(restaurant_id, conversation_id, id)
);
