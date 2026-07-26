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

	"github.com/PCenjoyer/GoProject/internal/auth"
	"github.com/PCenjoyer/GoProject/internal/domain"
	"github.com/PCenjoyer/GoProject/internal/ssrf"
	"github.com/PCenjoyer/GoProject/internal/store"
)

type fakeStore struct {
	createEvent func(context.Context, store.CreateEventParams) (store.EventResult, error)
}

func (f *fakeStore) CreateEndpoint(context.Context, store.CreateEndpointParams) (domain.Endpoint, error) {
	return domain.Endpoint{ID: "endpoint-1", Name: "orders", URL: "https://example.com/hooks", Enabled: true}, nil
}
func (f *fakeStore) ListEndpoints(context.Context, string, int) ([]domain.Endpoint, error) {
	return []domain.Endpoint{}, nil
}
func (f *fakeStore) CreateEvent(ctx context.Context, params store.CreateEventParams) (store.EventResult, error) {
	return f.createEvent(ctx, params)
}
func (f *fakeStore) GetEvent(context.Context, string, string) (domain.Event, error) {
	return domain.Event{}, store.ErrNotFound
}
func (f *fakeStore) ListDeliveries(context.Context, store.DeliveryFilter) ([]domain.Delivery, error) {
	return []domain.Delivery{}, nil
}
func (f *fakeStore) ReplayDelivery(context.Context, string, string) error { return nil }

func testAPI(dataStore store.Store) http.Handler {
	return testAPIWithPolicy(dataStore, ssrf.NewPolicy(true))
}

func testAPIWithPolicy(dataStore store.Store, policy *ssrf.Policy) http.Handler {
	routes := New(
		dataStore,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		policy,
	).Routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithPrincipal(r.Context(), auth.Principal{TenantID: "tenant-1"})
		routes.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestCreateEndpointBlocksPrivateNetwork(t *testing.T) {
	handler := testAPIWithPolicy(&fakeStore{}, ssrf.NewPolicy(false))
	request := httptest.NewRequest(http.MethodPost, "/v1/endpoints", strings.NewReader(
		`{"name":"metadata","url":"http://169.254.169.254/latest/meta-data"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestMeReturnsAuthenticatedTenant(t *testing.T) {
	routes := New(
		&fakeStore{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		ssrf.NewPolicy(true),
	).Routes()
	request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		TenantID:   "tenant-1",
		TenantSlug: "acme",
		TenantName: "Acme Corporation",
		APIKeyID:   "key-1",
	}))
	response := httptest.NewRecorder()

	routes.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		Tenant struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"tenant"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Tenant.ID != "tenant-1" || body.Tenant.Slug != "acme" || body.Tenant.Name != "Acme Corporation" {
		t.Fatalf("unexpected tenant: %+v", body.Tenant)
	}
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
	if got.TenantID != "tenant-1" {
		t.Fatalf("tenant = %q", got.TenantID)
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
