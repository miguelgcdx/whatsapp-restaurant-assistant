-- +goose Up
-- Explicit local control only: no default mode, backfill, or dispatch authority.
CREATE TABLE app.conversation_controls (
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    mode text NOT NULL CHECK (mode IN ('ai', 'staff')),
    version bigint NOT NULL CHECK (version >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, conversation_id),
    FOREIGN KEY (restaurant_id, conversation_id)
        REFERENCES app.cart_conversations(restaurant_id, id)
);

CREATE TABLE app.conversation_control_operations (
    restaurant_id uuid NOT NULL,
    auth_user_id text NOT NULL CHECK (
        octet_length(auth_user_id) BETWEEN 1 AND 256 AND btrim(auth_user_id) <> ''
    ),
    operation_key text NOT NULL CHECK (
        octet_length(operation_key) BETWEEN 1 AND 128
        AND operation_key COLLATE "C" ~ '^[A-Za-z0-9._:-]+$'
    ),
    conversation_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('initialize', 'set_mode')),
    target_mode text NOT NULL CHECK (target_mode IN ('ai', 'staff')),
    expected_version bigint,
    result_mode text NOT NULL CHECK (result_mode IN ('ai', 'staff')),
    result_version bigint NOT NULL CHECK (result_version >= 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, auth_user_id, operation_key),
    FOREIGN KEY (restaurant_id, auth_user_id)
        REFERENCES app.staff_memberships(restaurant_id, auth_user_id),
    FOREIGN KEY (restaurant_id, conversation_id)
        REFERENCES app.conversation_controls(restaurant_id, conversation_id),
    CHECK (result_mode = target_mode),
    CHECK (
        (action = 'initialize' AND expected_version IS NULL AND result_version = 0)
        OR
        (action = 'set_mode' AND expected_version IS NOT NULL
            AND expected_version >= 0 AND expected_version < 9223372036854775807
            -- Numeric arithmetic cannot overflow even for rejected bigint input.
            AND result_version::numeric = expected_version::numeric + 1)
    )
);

-- +goose Down
DROP TABLE app.conversation_control_operations;
DROP TABLE app.conversation_controls;
