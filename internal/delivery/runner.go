package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const maxResponseBody = 4 << 10

type Config struct {
	WorkerCount         int
	PollInterval        time.Duration
	DeliveryTimeout     time.Duration
	LeaseDuration       time.Duration
	MaxAttempts         int
	EndpointConcurrency int
}

type Runner struct {
	cfg      Config
	store    Store
	client   *http.Client
	logger   *slog.Logger
	workerID string
	limiters *endpointLimiters
	now      func() time.Time
	jitter   func(time.Duration) time.Duration
	metrics  Metrics
}

type Metrics interface {
	ClaimSucceeded()
	ClaimFailed()
	EndpointDeferred()
	AttemptStarted()
	AttemptFinished(string, time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) ClaimSucceeded()                       {}
func (noopMetrics) ClaimFailed()                          {}
func (noopMetrics) EndpointDeferred()                     {}
func (noopMetrics) AttemptStarted()                       {}
func (noopMetrics) AttemptFinished(string, time.Duration) {}

func NewRunner(cfg Config, dataStore Store, logger *slog.Logger, workerID string) *Runner {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.WorkerCount * 2
	transport.MaxIdleConnsPerHost = cfg.EndpointConcurrency
	transport.ResponseHeaderTimeout = cfg.DeliveryTimeout

	return &Runner{
		cfg:      cfg,
		store:    dataStore,
		client:   &http.Client{Transport: transport},
		logger:   logger,
		workerID: workerID,
		limiters: newEndpointLimiters(cfg.EndpointConcurrency),
		now:      time.Now,
		jitter: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max)))
		},
		metrics: noopMetrics{},
	}
}

func (r *Runner) SetMetrics(metrics Metrics) {
	if metrics != nil {
		r.metrics = metrics
	}
}

func (r *Runner) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(r.cfg.WorkerCount)
	for i := 0; i < r.cfg.WorkerCount; i++ {
		go func(workerNumber int) {
			defer workers.Done()
			r.worker(ctx, workerNumber)
		}(i + 1)
	}
	workers.Wait()
	r.logger.Info("delivery workers stopped")
}

func (r *Runner) worker(ctx context.Context, workerNumber int) {
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		task, ok, err := r.store.Claim(ctx, r.workerID, r.cfg.LeaseDuration)
		if err != nil {
			r.metrics.ClaimFailed()
			if !errors.Is(err, context.Canceled) {
				r.logger.Error("claim failed", "worker", workerNumber, "error", err)
			}
			timer.Reset(r.cfg.PollInterval)
			continue
		}
		if !ok {
			timer.Reset(r.cfg.PollInterval)
			continue
		}
		r.metrics.ClaimSucceeded()
		if !r.limiters.tryAcquire(task.EndpointID) {
			r.metrics.EndpointDeferred()
			next := r.now().Add(r.cfg.PollInterval)
			deferCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := r.store.Defer(deferCtx, r.workerID, task, next)
			cancel()
			if err != nil {
				r.logger.Error("defer saturated endpoint", "delivery_id", task.DeliveryID, "error", err)
			}
			timer.Reset(r.cfg.PollInterval)
			continue
		}

		r.process(task)
		r.limiters.release(task.EndpointID)
		timer.Reset(0)
	}
}

func (r *Runner) process(task Task) {
	started := r.now()
	r.metrics.AttemptStarted()
	statusCode, deliveryErr := r.send(task, started)
	finished := r.now()

	result := Result{
		StatusCode: statusCode,
		StartedAt:  started,
		FinishedAt: finished,
	}
	retryable := deliveryErr != nil || statusCode == nil || retryableStatus(*statusCode)
	if deliveryErr != nil {
		result.Error = deliveryErr.Error()
	} else if statusCode != nil && (*statusCode < 200 || *statusCode >= 300) {
		result.Error = fmt.Sprintf("receiver returned HTTP %d", *statusCode)
	}

	switch {
	case statusCode != nil && *statusCode >= 200 && *statusCode < 300:
		result.NextTryAt = finished
	case !retryable:
		result.Dead = true
		result.NextTryAt = finished
	case task.Attempt >= r.cfg.MaxAttempts:
		result.Dead = true
		result.NextTryAt = finished
	default:
		result.NextTryAt = finished.Add(r.jitter(backoff(task.Attempt)))
	}
	outcome := "retry"
	if result.Dead {
		outcome = "dead"
	} else if result.Error == "" {
		outcome = "success"
	}
	r.metrics.AttemptFinished(outcome, finished.Sub(started))

	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.Finish(persistCtx, r.workerID, task, result); err != nil {
		r.logger.Error("persist delivery result",
			"delivery_id", task.DeliveryID,
			"attempt", task.Attempt,
			"error", err,
		)
		return
	}
	r.logger.Info("delivery finished",
		"delivery_id", task.DeliveryID,
		"endpoint_id", task.EndpointID,
		"attempt", task.Attempt,
		"status_code", statusCode,
		"retry", !result.Dead && result.NextTryAt.After(finished),
		"dead", result.Dead,
	)
}

func (r *Runner) send(task Task, now time.Time) (*int, error) {
	body, err := json.Marshal(map[string]any{
		"id":         task.EventID,
		"type":       task.EventType,
		"created_by": "hookforge",
		"payload":    task.Payload,
	})
	if err != nil {
		return nil, fmt.Errorf("encode webhook body: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.DeliveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, task.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build webhook request: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "HookForge/1.0")
	request.Header.Set("X-HookForge-Event-ID", task.EventID)
	request.Header.Set("X-HookForge-Attempt", strconv.Itoa(task.Attempt))
	request.Header.Set("X-HookForge-Timestamp", timestamp)
	request.Header.Set("X-HookForge-Signature", signature(task.Secret, timestamp, body))

	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send webhook: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
	return &response.StatusCode, nil
}

func signature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func backoff(attempt int) time.Duration {
	const (
		base = 500 * time.Millisecond
		max  = time.Minute
	)
	if attempt < 1 {
		attempt = 1
	}
	value := base
	for i := 1; i < attempt; i++ {
		if value > max/2 {
			return max
		}
		value *= 2
	}
	if value > max {
		return max
	}
	return value
}

type endpointLimiters struct {
	mu    sync.Mutex
	size  int
	slots map[string]chan struct{}
}

func newEndpointLimiters(size int) *endpointLimiters {
	return &endpointLimiters{size: size, slots: make(map[string]chan struct{})}
}

func (l *endpointLimiters) tryAcquire(endpointID string) bool {
	l.mu.Lock()
	slot, ok := l.slots[endpointID]
	if !ok {
		slot = make(chan struct{}, l.size)
		l.slots[endpointID] = slot
	}
	l.mu.Unlock()

	select {
	case slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *endpointLimiters) release(endpointID string) {
	l.mu.Lock()
	slot := l.slots[endpointID]
	l.mu.Unlock()
	<-slot
}
