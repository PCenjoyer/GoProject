package delivery

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	result Result
}

func (m *memoryStore) Claim(context.Context, string, time.Duration) (Task, bool, error) {
	return Task{}, false, nil
}
func (m *memoryStore) Defer(context.Context, string, Task, time.Time) error { return nil }
func (m *memoryStore) Finish(_ context.Context, _ string, _ Task, result Result) error {
	m.result = result
	return nil
}

func testRunner(dataStore Store) *Runner {
	runner := NewRunner(Config{
		WorkerCount:         1,
		PollInterval:        time.Millisecond,
		DeliveryTimeout:     time.Second,
		LeaseDuration:       time.Minute,
		MaxAttempts:         3,
		EndpointConcurrency: 1,
	}, dataStore, slog.New(slog.NewTextHandler(io.Discard, nil)), "test-worker")
	runner.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	runner.jitter = func(max time.Duration) time.Duration { return max / 2 }
	return runner
}

func TestProcessSignsSuccessfulRequest(t *testing.T) {
	var signatureHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signatureHeader = r.Header.Get("X-HookForge-Signature")
		if r.Header.Get("X-HookForge-Event-ID") != "event-1" {
			t.Error("missing stable event ID")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dataStore := &memoryStore{}
	runner := testRunner(dataStore)
	runner.process(Task{
		DeliveryID: "delivery-1",
		EventID:    "event-1",
		EventType:  "order.created",
		Payload:    []byte(`{"order_id":"42"}`),
		EndpointID: "endpoint-1",
		URL:        server.URL,
		Secret:     "top-secret",
		Attempt:    1,
	})

	if !strings.HasPrefix(signatureHeader, "v1=") {
		t.Fatalf("signature = %q", signatureHeader)
	}
	if dataStore.result.Dead || dataStore.result.Error != "" {
		t.Fatalf("unexpected result: %+v", dataStore.result)
	}
}

func TestProcessRetriesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	dataStore := &memoryStore{}
	runner := testRunner(dataStore)
	runner.process(Task{
		DeliveryID: "delivery-1",
		EventID:    "event-1",
		EventType:  "order.created",
		Payload:    []byte(`{}`),
		EndpointID: "endpoint-1",
		URL:        server.URL,
		Secret:     "secret",
		Attempt:    2,
	})

	if dataStore.result.Dead {
		t.Fatal("retryable response moved to DLQ")
	}
	wantNext := runner.now().Add(backoff(2) / 2)
	if !dataStore.result.NextTryAt.Equal(wantNext) {
		t.Fatalf("next try = %s, want %s", dataStore.result.NextTryAt, wantNext)
	}
}

func TestProcessMovesPermanentErrorToDLQ(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	dataStore := &memoryStore{}
	runner := testRunner(dataStore)
	runner.process(Task{
		DeliveryID: "delivery-1",
		EventID:    "event-1",
		EventType:  "order.created",
		Payload:    []byte(`{}`),
		EndpointID: "endpoint-1",
		URL:        server.URL,
		Secret:     "secret",
		Attempt:    1,
	})

	if !dataStore.result.Dead {
		t.Fatal("permanent client error did not move to DLQ")
	}
}

func TestEndpointLimiterIsNonBlocking(t *testing.T) {
	limiter := newEndpointLimiters(1)
	if !limiter.tryAcquire("slow") {
		t.Fatal("first acquire failed")
	}
	if limiter.tryAcquire("slow") {
		t.Fatal("second acquire exceeded endpoint limit")
	}
	if !limiter.tryAcquire("fast") {
		t.Fatal("different endpoint was blocked")
	}
	limiter.release("slow")
	limiter.release("fast")
}

func TestBackoffIsCapped(t *testing.T) {
	if got := backoff(100); got != time.Minute {
		t.Fatalf("backoff = %s, want 1m", got)
	}
}
