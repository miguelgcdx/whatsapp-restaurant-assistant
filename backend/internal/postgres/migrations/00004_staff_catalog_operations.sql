-- +goose Up
-- Complete local staff catalog results only; no pending claims or fixture defaults.
-- Writers lock the existing restaurant identity, then mutate and insert together.
-- Retention/deletion requires a separate policy; no automatic cleanup is installed.
CREATE TABLE app.operations (
    id uuid PRIMARY KEY,
    restaurant_id uuid NOT NULL REFERENCES app.restaurants(id),
    auth_user_id text NOT NULL CHECK (char_length(auth_user_id) > 0),
    method text NOT NULL CHECK (method = 'PATCH'),
    resource text NOT NULL CHECK (
        resource = '/v1/staff/restaurant/availability'
        OR resource ~ '^/v1/staff/catalog/items/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/availability$'
    ),
    idempotency_key text NOT NULL CHECK (
        idempotency_key ~ '^[!-~]{1,128}$'
        AND octet_length(idempotency_key) = char_length(idempotency_key)
    ),
    request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint) = 32),
    response_status smallint NOT NULL CHECK (response_status = 200),
    response_body text NOT NULL CHECK (
        octet_length(response_body) BETWEEN 1 AND 524288
        AND jsonb_typeof(response_body::jsonb) = 'object'
    ),
    created_at timestamptz NOT NULL,
    UNIQUE (restaurant_id, auth_user_id, method, resource, idempotency_key)
);
