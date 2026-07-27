package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PCenjoyer/GoProject/internal/secretbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool      *pgxpool.Pool
	secretBox *secretbox.Box
}

func NewPostgresStore(pool *pgxpool.Pool, box *secretbox.Box) *PostgresStore {
	return &PostgresStore{pool: pool, secretBox: box}
}

func (s *PostgresStore) Claim(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
) (Task, bool, error) {
	var task Task
	var ciphertext, nonce []byte
	err := s.pool.QueryRow(ctx, `
		WITH ready AS (
			SELECT d.id, d.event_id, d.endpoint_id, d.attempt_count + 1 AS attempt,
			       e.tenant_id, e.type, e.payload, ep.url,
			       ep.secret_ciphertext, ep.secret_nonce
			FROM deliveries d
			JOIN events e ON e.id = d.event_id
			JOIN endpoints ep ON ep.id = d.endpoint_id
			WHERE ep.enabled
			  AND (
			    (d.status IN ('pending', 'retrying') AND d.next_attempt_at <= now())
			    OR
			    (d.status = 'delivering' AND d.locked_at < now() - $2::interval)
			  )
			ORDER BY d.next_attempt_at, d.created_at
			FOR UPDATE OF d SKIP LOCKED
			LIMIT 1
		)
		UPDATE deliveries d
		SET status = 'delivering',
		    locked_at = now(),
		    locked_by = $1,
		    updated_at = now()
		FROM ready
		WHERE d.id = ready.id
		RETURNING ready.tenant_id::text, d.id::text, ready.event_id::text,
		          ready.type, ready.payload, ready.endpoint_id::text, ready.url,
		          ready.secret_ciphertext, ready.secret_nonce, ready.attempt`,
		workerID, interval(leaseDuration),
	).Scan(
		&task.TenantID,
		&task.DeliveryID,
		&task.EventID,
		&task.EventType,
		&task.Payload,
		&task.EndpointID,
		&task.URL,
		&ciphertext,
		&nonce,
		&task.Attempt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("получение доставки: %w", err)
	}
	task.Secret, err = s.secretBox.Decrypt(
		ciphertext,
		nonce,
		secretbox.AssociatedData(task.TenantID, task.EndpointID),
	)
	if err != nil {
		return Task{}, false, fmt.Errorf("расшифровка секрета полученной точки: %w", err)
	}
	return task, true, nil
}

func (s *PostgresStore) Defer(
	ctx context.Context,
	workerID string,
	task Task,
	nextTryAt time.Time,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deliveries
		SET status = 'retrying',
		    next_attempt_at = $3,
		    locked_at = NULL,
		    locked_by = NULL,
		    updated_at = now()
		WHERE id = $1 AND locked_by = $2 AND status = 'delivering'`,
		task.DeliveryID, workerID, nextTryAt,
	)
	if err != nil {
		return fmt.Errorf("откладывание доставки: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("откладывание доставки: блокировка потеряна")
	}
	return nil
}

func (s *PostgresStore) Finish(
	ctx context.Context,
	workerID string,
	task Task,
	result Result,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало завершения доставки: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status := "succeeded"
	if result.Dead {
		status = "dead"
	} else if result.Error != "" || result.StatusCode == nil || *result.StatusCode < 200 || *result.StatusCode >= 300 {
		status = "retrying"
	}
	var lastError *string
	if result.Error != "" {
		lastError = &result.Error
	}
	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status = $3::delivery_status,
		    attempt_count = $4,
		    next_attempt_at = $5,
		    last_status_code = $6,
		    last_error = $7,
		    locked_at = NULL,
		    locked_by = NULL,
		    updated_at = now()
		WHERE id = $1 AND locked_by = $2 AND status = 'delivering'`,
		task.DeliveryID,
		workerID,
		status,
		task.Attempt,
		result.NextTryAt,
		result.StatusCode,
		lastError,
	)
	if err != nil {
		return fmt.Errorf("обновление результата доставки: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("завершение доставки: блокировка потеряна")
	}
	duration := result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO delivery_attempts (
			delivery_id, attempt_number, started_at, finished_at,
			status_code, error, duration_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		task.DeliveryID,
		task.Attempt,
		result.StartedAt,
		result.FinishedAt,
		result.StatusCode,
		lastError,
		duration,
	); err != nil {
		return fmt.Errorf("сохранение попытки доставки: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("фиксация результата доставки: %w", err)
	}
	return nil
}

func interval(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}
