-- +goose Up
CREATE TABLE networks (
    id text PRIMARY KEY,
    metadata jsonb,
    config jsonb
);

CREATE TABLE nodes (
    network_id text NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    node_id text NOT NULL,
    config jsonb NOT NULL,
    PRIMARY KEY (network_id, node_id)
);

CREATE TABLE registrations (
    network_id text NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    node_id text NOT NULL,
    public_key text NOT NULL,
    document jsonb NOT NULL,
    PRIMARY KEY (network_id, node_id),
    UNIQUE (network_id, public_key)
);

-- +goose Down
DROP TABLE registrations;
DROP TABLE nodes;
DROP TABLE networks;
