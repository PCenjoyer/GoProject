package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/PCenjoyer/GoProject/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (s *Postgres) CreateEndpoint(ctx context.Context, params CreateEndpointParams) (domain.Endpoint, error) {
	var endpoint domain.Endpoint
	err := s.pool.QueryRow(ctx, `
		INSERT INTO endpoints (name, url, secret)
		VALUES ($1, $2, $3)
		RETURNING id::text, name, url, secret, enabled, created_at, updated_at`,
		params.Name, params.URL, params.Secret,
	).Scan(
		&endpoint.ID,
		&endpoint.Name,
		&endpoint.URL,
		&endpoint.Secret,
		&endpoint.Enabled,
		&endpoint.CreatedAt,
		&endpoint.UpdatedAt,
	)
	if err != nil {
		return domain.Endpoint{}, fmt.Errorf("create endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *Postgres) ListEndpoints(ctx context.Context, limit int) ([]domain.Endpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, url, enabled, created_at, updated_at
		FROM endpoints
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
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
			return nil, fmt.Errorf("scan endpoint: %w", err)
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}

func (s *Postgres) CreateEvent(ctx context.Context, params CreateEventParams) (EventResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return EventResult{}, fmt.Errorf("begin create event: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result EventResult
	err = tx.QueryRow(ctx, `
		INSERT INTO events (idempotency_key, type, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id::text, idempotency_key, type, payload, created_at`,
		params.IdempotencyKey, params.Type, params.Payload,
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
			WHERE idempotency_key = $1`,
			params.IdempotencyKey,
		).Scan(
			&result.Event.ID,
			&result.Event.IdempotencyKey,
			&result.Event.Type,
			&result.Event.Payload,
			&result.Event.CreatedAt,
		)
	}
	if err != nil {
		return EventResult{}, fmt.Errorf("insert or find event: %w", err)
	}

	if !result.Duplicate {
		tag, err := tx.Exec(ctx, `
			INSERT INTO deliveries (event_id, endpoint_id)
			SELECT $1::uuid, id
			FROM endpoints
			WHERE id = ANY($2::uuid[]) AND enabled`,
			result.Event.ID, params.EndpointIDs,
		)
		if err != nil {
			return EventResult{}, fmt.Errorf("create event deliveries: %w", err)
		}
		if tag.RowsAffected() != int64(len(params.EndpointIDs)) {
			return EventResult{}, ErrInvalidEndpoint
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return EventResult{}, fmt.Errorf("commit create event: %w", err)
	}
	return result, nil
}

func (s *Postgres) GetEvent(ctx context.Context, id string) (domain.Event, error) {
	var event domain.Event
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, idempotency_key, type, payload, created_at
		FROM events
		WHERE id = $1`,
		id,
	).Scan(&event.ID, &event.IdempotencyKey, &event.Type, &event.Payload, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, ErrNotFound
	}
	if err != nil {
		return domain.Event{}, fmt.Errorf("get event: %w", err)
	}
	return event, nil
}

func (s *Postgres) ListDeliveries(ctx context.Context, filter DeliveryFilter) ([]domain.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, event_id::text, endpoint_id::text, status::text,
		       attempt_count, next_attempt_at, last_status_code, last_error,
		       created_at, updated_at
		FROM deliveries
		WHERE ($1 = '' OR status::text = $1)
		ORDER BY created_at DESC
		LIMIT $2`,
		filter.Status, filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
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
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Postgres) ReplayDelivery(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deliveries
		SET status = 'retrying',
		    attempt_count = 0,
		    next_attempt_at = now(),
		    locked_at = NULL,
		    locked_by = NULL,
		    last_status_code = NULL,
		    last_error = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'dead'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("replay delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
