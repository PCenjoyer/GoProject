package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/PCenjoyer/GoProject/internal/domain"
	"github.com/PCenjoyer/GoProject/internal/secretbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool      *pgxpool.Pool
	secretBox *secretbox.Box
}

func NewPostgres(pool *pgxpool.Pool, box *secretbox.Box) *Postgres {
	return &Postgres{pool: pool, secretBox: box}
}

func (s *Postgres) CreateEndpoint(ctx context.Context, params CreateEndpointParams) (domain.Endpoint, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Endpoint{}, fmt.Errorf("начало создания точки назначения: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var endpoint domain.Endpoint
	err = tx.QueryRow(ctx, `
		INSERT INTO endpoints (tenant_id, name, url)
		VALUES ($1, $2, $3)
		RETURNING id::text, name, url, enabled, created_at, updated_at`,
		params.TenantID, params.Name, params.URL,
	).Scan(
		&endpoint.ID,
		&endpoint.Name,
		&endpoint.URL,
		&endpoint.Enabled,
		&endpoint.CreatedAt,
		&endpoint.UpdatedAt,
	)
	if err != nil {
		return domain.Endpoint{}, fmt.Errorf("создание точки назначения: %w", err)
	}
	ciphertext, nonce, err := s.secretBox.Encrypt(
		params.Secret,
		secretbox.AssociatedData(params.TenantID, endpoint.ID),
	)
	if err != nil {
		return domain.Endpoint{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE endpoints
		SET secret_ciphertext = $2, secret_nonce = $3, secret_key_version = 1
		WHERE id = $1`,
		endpoint.ID, ciphertext, nonce,
	); err != nil {
		return domain.Endpoint{}, fmt.Errorf("шифрование секрета точки назначения: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Endpoint{}, fmt.Errorf("фиксация создания точки назначения: %w", err)
	}
	return endpoint, nil
}

func (s *Postgres) ListEndpoints(ctx context.Context, tenantID string, limit int) ([]domain.Endpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, url, enabled, created_at, updated_at
		FROM endpoints
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("получение списка точек назначения: %w", err)
	}
	defer rows.Close()

	endpoints := make([]domain.Endpoint, 0)
	for rows.Next() {
		var endpoint domain.Endpoint
		if err := rows.Scan(
			&endpoint.ID,
			&endpoint.Name,
			&endpoint.URL,
			&endpoint.Enabled,
			&endpoint.CreatedAt,
			&endpoint.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("чтение точки назначения: %w", err)
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}

func (s *Postgres) CreateEvent(ctx context.Context, params CreateEventParams) (EventResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return EventResult{}, fmt.Errorf("начало создания события: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result EventResult
	err = tx.QueryRow(ctx, `
		INSERT INTO events (tenant_id, idempotency_key, type, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING id::text, idempotency_key, type, payload, created_at`,
		params.TenantID, params.IdempotencyKey, params.Type, params.Payload,
	).Scan(
		&result.Event.ID,
		&result.Event.IdempotencyKey,
		&result.Event.Type,
		&result.Event.Payload,
		&result.Event.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		result.Duplicate = true
		err = tx.QueryRow(ctx, `
			SELECT id::text, idempotency_key, type, payload, created_at
			FROM events
			WHERE tenant_id = $1 AND idempotency_key = $2`,
			params.TenantID, params.IdempotencyKey,
		).Scan(
			&result.Event.ID,
			&result.Event.IdempotencyKey,
			&result.Event.Type,
			&result.Event.Payload,
			&result.Event.CreatedAt,
		)
	}
	if err != nil {
		return EventResult{}, fmt.Errorf("сохранение или поиск события: %w", err)
	}

	if !result.Duplicate {
		tag, err := tx.Exec(ctx, `
			INSERT INTO deliveries (event_id, endpoint_id)
			SELECT $1::uuid, id
			FROM endpoints
			WHERE tenant_id = $2
			  AND id = ANY($3::uuid[])
			  AND enabled`,
			result.Event.ID, params.TenantID, params.EndpointIDs,
		)
		if err != nil {
			return EventResult{}, fmt.Errorf("создание доставок события: %w", err)
		}
		if tag.RowsAffected() != int64(len(params.EndpointIDs)) {
			return EventResult{}, ErrInvalidEndpoint
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return EventResult{}, fmt.Errorf("фиксация создания события: %w", err)
	}
	return result, nil
}

func (s *Postgres) GetEvent(ctx context.Context, tenantID, id string) (domain.Event, error) {
	var event domain.Event
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, idempotency_key, type, payload, created_at
		FROM events
		WHERE tenant_id = $1 AND id = $2`,
		tenantID, id,
	).Scan(&event.ID, &event.IdempotencyKey, &event.Type, &event.Payload, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, ErrNotFound
	}
	if err != nil {
		return domain.Event{}, fmt.Errorf("получение события: %w", err)
	}
	return event, nil
}

func (s *Postgres) ListDeliveries(ctx context.Context, filter DeliveryFilter) ([]domain.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id::text, d.event_id::text, d.endpoint_id::text, d.status::text,
		       d.attempt_count, d.next_attempt_at, d.last_status_code, d.last_error,
		       d.created_at, d.updated_at
		FROM deliveries d
		JOIN events e ON e.id = d.event_id
		WHERE e.tenant_id = $1
		  AND ($2 = '' OR d.status::text = $2)
		ORDER BY d.created_at DESC
		LIMIT $3`,
		filter.TenantID, filter.Status, filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("получение списка доставок: %w", err)
	}
	defer rows.Close()

	deliveries := make([]domain.Delivery, 0)
	for rows.Next() {
		var delivery domain.Delivery
		if err := rows.Scan(
			&delivery.ID,
			&delivery.EventID,
			&delivery.EndpointID,
			&delivery.Status,
			&delivery.AttemptCount,
			&delivery.NextAttemptAt,
			&delivery.LastStatusCode,
			&delivery.LastError,
			&delivery.CreatedAt,
			&delivery.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("чтение доставки: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Postgres) ReplayDelivery(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deliveries d
		SET status = 'retrying',
		    attempt_count = 0,
		    next_attempt_at = now(),
		    locked_at = NULL,
		    locked_by = NULL,
		    last_status_code = NULL,
		    last_error = NULL,
		    updated_at = now()
		FROM events e
		WHERE d.id = $1
		  AND d.status = 'dead'
		  AND e.id = d.event_id
		  AND e.tenant_id = $2`,
		id, tenantID,
	)
	if err != nil {
		return fmt.Errorf("повтор доставки: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) EncryptLegacyEndpointSecrets(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, secret
		FROM endpoints
		WHERE secret IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("получение старых секретов точек назначения: %w", err)
	}
	type legacySecret struct {
		endpointID string
		tenantID   string
		plaintext  string
	}
	var legacy []legacySecret
	for rows.Next() {
		var item legacySecret
		if err := rows.Scan(&item.endpointID, &item.tenantID, &item.plaintext); err != nil {
			rows.Close()
			return 0, fmt.Errorf("чтение старого секрета точки назначения: %w", err)
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("обход старых секретов точек назначения: %w", err)
	}
	rows.Close()

	for _, item := range legacy {
		ciphertext, nonce, err := s.secretBox.Encrypt(
			item.plaintext,
			secretbox.AssociatedData(item.tenantID, item.endpointID),
		)
		if err != nil {
			return 0, err
		}
		tag, err := s.pool.Exec(ctx, `
			UPDATE endpoints
			SET secret_ciphertext = $2,
			    secret_nonce = $3,
			    secret_key_version = 1,
			    secret = NULL
			WHERE id = $1 AND secret IS NOT NULL`,
			item.endpointID, ciphertext, nonce,
		)
		if err != nil {
			return 0, fmt.Errorf("перенос старого секрета точки назначения: %w", err)
		}
		if tag.RowsAffected() != 1 {
			continue
		}
	}
	return len(legacy), nil
}
