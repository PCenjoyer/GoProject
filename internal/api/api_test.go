package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PCenjoyer/GoProject/internal/domain"
	"github.com/PCenjoyer/GoProject/internal/store"
)

type fakeStore struct {
	createEvent func(context.Context, store.CreateEventParams) (store.EventResult, error)
}

func (f *fakeStore) CreateEndpoint(context.Context, store.CreateEndpointParams) (domain.Endpoint, error) {
	return domain.Endpoint{ID: "endpoint-1", Name: "orders", URL: "https://example.com/hooks", Enabled: true}, nil
}
func (f *fakeStore) ListEndpoints(context.Context, int) ([]domain.Endpoint, error) {
	return []domain.Endpoint{}, nil
}
func (f *fakeStore) CreateEvent(ctx context.Context, params store.CreateEventParams) (store.EventResult, error) {
	return f.createEvent(ctx, params)
}
func (f *fakeStore) GetEvent(context.Context, string) (domain.Event, error) {
	return domain.Event{}, store.ErrNotFound
}
func (f *fakeStore) ListDeliveries(context.Context, store.DeliveryFilter) ([]domain.Delivery, error) {
	return []domain.Delivery{}, nil
}

func testAPI(dataStore store.Store) http.Handler {
	return New(dataStore, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
}

func TestCreateEventRequiresIdempotencyKey(t *testing.T) {
	handler := testAPI(&fakeStore{})
	request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(
		`{"type":"order.created","payload":{},"endpoint_ids":["01900000-0000-7000-8000-000000000001"]}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateEventReturnsAccepted(t *testing.T) {
	var got store.CreateEventParams
	handler := testAPI(&fakeStore{createEvent: func(_ context.Context, params store.CreateEventParams) (store.EventResult, error) {
		got = params
		return store.EventResult{Event: domain.Event{ID: "event-1", Type: params.Type}}, nil
	}})
	request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(
		`{"type":"order.created","payload":{"order_id":"42"},"endpoint_ids":["01900000-0000-7000-8000-000000000001"]}`,
	))
	request.Header.Set("Idempotency-Key", "checkout-42")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body)
	}
	if got.IdempotencyKey != "checkout-42" || got.Type != "order.created" {
		t.Fatalf("unexpected params: %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if duplicate, _ := body["duplicate"].(bool); duplicate {
		t.Fatal("new event reported as duplicate")
	}
}

func TestCreateEventReturnsExistingDuplicate(t *testing.T) {
	handler := testAPI(&fakeStore{createEvent: func(_ context.Context, params store.CreateEventParams) (store.EventResult, error) {
		return store.EventResult{
			Event:     domain.Event{ID: "event-existing", Type: params.Type},
			Duplicate: true,
		}, nil
	}})
	request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(
		`{"type":"order.created","payload":{},"endpoint_ids":["01900000-0000-7000-8000-000000000001"]}`,
	))
	request.Header.Set("Idempotency-Key", "same-key")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestCreateEndpointRejectsUnsafeShape(t *testing.T) {
	handler := testAPI(&fakeStore{})
	request := httptest.NewRequest(http.MethodPost, "/v1/endpoints", strings.NewReader(
		`{"name":"receiver","url":"file:///etc/passwd"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}
