CREATE TABLE tenants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO tenants (slug, name)
VALUES ('default', 'Default tenant')
ON CONFLICT (slug) DO NOTHING;

ALTER TABLE endpoints
    ADD COLUMN tenant_id uuid REFERENCES tenants(id);

UPDATE endpoints
SET tenant_id = (SELECT id FROM tenants WHERE slug = 'default')
WHERE tenant_id IS NULL;

ALTER TABLE endpoints
    ALTER COLUMN tenant_id SET NOT NULL,
    ALTER COLUMN secret DROP NOT NULL,
    ADD COLUMN secret_ciphertext bytea,
    ADD COLUMN secret_nonce bytea,
    ADD COLUMN secret_key_version smallint NOT NULL DEFAULT 1;

CREATE INDEX endpoints_tenant_created_idx
    ON endpoints (tenant_id, created_at DESC);

ALTER TABLE events
    ADD COLUMN tenant_id uuid REFERENCES tenants(id);

UPDATE events
SET tenant_id = (SELECT id FROM tenants WHERE slug = 'default')
WHERE tenant_id IS NULL;

ALTER TABLE events
    ALTER COLUMN tenant_id SET NOT NULL;

ALTER TABLE events
    DROP CONSTRAINT events_idempotency_key_key;

CREATE UNIQUE INDEX events_tenant_idempotency_idx
    ON events (tenant_id, idempotency_key);

CREATE INDEX events_tenant_created_idx
    ON events (tenant_id, created_at DESC);

CREATE TABLE api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    key_prefix text NOT NULL UNIQUE,
    key_hash bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz
);

CREATE INDEX api_keys_tenant_idx
    ON api_keys (tenant_id, created_at DESC);
