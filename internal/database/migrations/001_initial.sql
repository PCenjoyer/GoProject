CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE delivery_status AS ENUM (
    'pending',
    'delivering',
    'succeeded',
    'retrying',
    'dead'
);

CREATE TABLE endpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    url text NOT NULL,
    secret text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key text NOT NULL UNIQUE,
    type text NOT NULL CHECK (length(type) BETWEEN 1 AND 120),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    endpoint_id uuid NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
    status delivery_status NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    locked_at timestamptz,
    locked_by text,
    last_status_code integer,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, endpoint_id)
);

CREATE TABLE delivery_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    delivery_id uuid NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    attempt_number integer NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    status_code integer,
    error text,
    duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
    UNIQUE (delivery_id, attempt_number)
);

CREATE INDEX deliveries_ready_idx
    ON deliveries (next_attempt_at, created_at)
    WHERE status IN ('pending', 'retrying');

CREATE INDEX deliveries_endpoint_idx ON deliveries (endpoint_id, created_at DESC);
CREATE INDEX delivery_attempts_delivery_idx ON delivery_attempts (delivery_id, attempt_number DESC);

