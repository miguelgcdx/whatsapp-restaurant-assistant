-- +goose Up
-- Explicit optional operational setup; auth-only restaurant identities remain valid.
-- No test currency, price, fee, availability or open-state defaults belong in DDL.
CREATE TABLE app.restaurant_settings (
    restaurant_id uuid PRIMARY KEY REFERENCES app.restaurants(id),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    currency_exponent smallint NOT NULL CHECK (currency_exponent BETWEEN 0 AND 6),
    flat_fee_minor bigint NOT NULL CHECK (flat_fee_minor >= 0),
    open boolean NOT NULL,
    version bigint NOT NULL CHECK (version >= 0),
    UNIQUE (restaurant_id, currency)
);
CREATE TABLE app.menu_items (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(btrim(name)) > 0 AND char_length(name) <= 200),
    price_minor bigint NOT NULL CHECK (price_minor >= 0),
    currency text NOT NULL,
    available boolean NOT NULL,
    version bigint NOT NULL CHECK (version >= 0),
    created_at timestamptz NOT NULL,
    UNIQUE (restaurant_id, id),
    FOREIGN KEY (restaurant_id, currency) REFERENCES app.restaurant_settings(restaurant_id, currency)
);
CREATE INDEX menu_items_restaurant_order ON app.menu_items (restaurant_id, created_at, id);
