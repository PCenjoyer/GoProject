package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) FindCandidate(ctx context.Context, prefix string) (Candidate, error) {
	var candidate Candidate
	err := r.pool.QueryRow(ctx, `
		SELECT k.id::text, k.key_hash, t.id::text, t.slug, t.name
		FROM api_keys k
		JOIN tenants t ON t.id = k.tenant_id
		WHERE k.key_prefix = $1 AND k.revoked_at IS NULL`,
		prefix,
	).Scan(
		&candidate.Principal.APIKeyID,
		&candidate.KeyHash,
		&candidate.Principal.TenantID,
		&candidate.Principal.TenantSlug,
		&candidate.Principal.TenantName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, ErrInvalidCredentials
	}
	if err != nil {
		return Candidate{}, fmt.Errorf("find API key: %w", err)
	}
	return candidate, nil
}

func (r *PostgresRepository) MarkUsed(ctx context.Context, keyID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE api_keys
		SET last_used_at = now()
		WHERE id = $1
		  AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes')`,
		keyID,
	)
	if err != nil {
		return fmt.Errorf("update API key usage: %w", err)
	}
	return nil
}

func (r *PostgresRepository) KeyCount(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count API keys: %w", err)
	}
	return count, nil
}

func (r *PostgresRepository) Bootstrap(
	ctx context.Context,
	slug, name, prefix string,
	hash []byte,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tenants (slug, name)
		VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id::text`,
		slug, name,
	).Scan(&tenantID); err != nil {
		return fmt.Errorf("upsert bootstrap tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE api_keys
		SET revoked_at = now()
		WHERE tenant_id = $1
		  AND name = 'bootstrap'
		  AND key_prefix <> $2
		  AND revoked_at IS NULL`,
		tenantID, prefix,
	); err != nil {
		return fmt.Errorf("revoke replaced bootstrap API key: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, name, key_prefix, key_hash)
		VALUES ($1, 'bootstrap', $2, $3)
		ON CONFLICT (key_prefix) DO UPDATE
		SET tenant_id = EXCLUDED.tenant_id,
		    name = EXCLUDED.name,
		    key_hash = EXCLUDED.key_hash,
		    revoked_at = NULL`,
		tenantID, prefix, hash,
	); err != nil {
		return fmt.Errorf("insert bootstrap API key: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) ProvisionTenant(
	ctx context.Context,
	slug, name, prefix string,
	hash []byte,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant provision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tenants (slug, name)
		VALUES ($1, $2)
		RETURNING id::text`,
		slug, name,
	).Scan(&tenantID); err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, name, key_prefix, key_hash)
		VALUES ($1, 'initial', $2, $3)`,
		tenantID, prefix, hash,
	); err != nil {
		return fmt.Errorf("insert tenant API key: %w", err)
	}
	return tx.Commit(ctx)
}
