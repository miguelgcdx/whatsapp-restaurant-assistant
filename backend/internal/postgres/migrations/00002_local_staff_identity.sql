-- +goose Up
-- Identity only: operational settings and orders require later explicit setup.
CREATE TABLE app.restaurants (
    id uuid PRIMARY KEY
);
CREATE TABLE app.staff_memberships (
    restaurant_id uuid NOT NULL REFERENCES app.restaurants(id),
    auth_user_id text NOT NULL CHECK (
        length(auth_user_id) BETWEEN 1 AND 256 AND btrim(auth_user_id) <> ''
    ),
    active boolean NOT NULL,
    PRIMARY KEY (restaurant_id, auth_user_id)
);
