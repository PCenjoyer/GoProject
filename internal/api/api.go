package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/PCenjoyer/GoProject/internal/domain"
	"github.com/PCenjoyer/GoProject/internal/store"
)

const maxRequestBody = 1 << 20

type API struct {
	store  store.Store
	logger *slog.Logger
}

func New(dataStore store.Store, logger *slog.Logger) *API {
	return &API{store: dataStore, logger: logger}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/endpoints", a.createEndpoint)
	mux.HandleFunc("GET /v1/endpoints", a.listEndpoints)
	mux.HandleFunc("POST /v1/events", a.createEvent)
	mux.HandleFunc("GET /v1/events/{id}", a.getEvent)
	mux.HandleFunc("GET /v1/deliveries", a.listDeliveries)
	return requestLog(a.logger, recoverPanic(a.logger, mux))
}

type createEndpointRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (a *API) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var input createEndpointRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "name must contain 1 to 120 characters")
		return
	}
	if err := validateEndpointURL(input.URL); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	secret, err := generateSecret()
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	endpoint, err := a.store.CreateEndpoint(r.Context(), store.CreateEndpointParams{
		Name: input.Name, URL: input.URL, Secret: secret,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"endpoint": endpoint,
		"secret":   secret,
	})
}

func (a *API) listEndpoints(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	endpoints, err := a.store.ListEndpoints(r.Context(), limit)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": endpoints})
}

type createEventRequest struct {
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	EndpointIDs []string        `json:"endpoint_ids"`
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key header must contain 1 to 200 characters")
		return
	}
	var input createEventRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Type = strings.TrimSpace(input.Type)
	if input.Type == "" || len(input.Type) > 120 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "type must contain 1 to 120 characters")
		return
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "payload must be valid JSON")
		return
	}
	if len(input.EndpointIDs) == 0 || len(input.EndpointIDs) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "endpoint_ids must contain 1 to 100 IDs")
		return
	}
	if hasDuplicate(input.EndpointIDs) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "endpoint_ids must be unique")
		return
	}

	result, err := a.store.CreateEvent(r.Context(), store.CreateEventParams{
		IdempotencyKey: idempotencyKey,
		Type:           input.Type,
		Payload:        input.Payload,
		EndpointIDs:    input.EndpointIDs,
	})
	if errors.Is(err, store.ErrInvalidEndpoint) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_endpoint", err.Error())
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"event":     result.Event,
		"duplicate": result.Duplicate,
	})
}

func (a *API) getEvent(w http.ResponseWriter, r *http.Request) {
	event, err := a.store.GetEvent(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "event not found")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (a *API) listDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status := domain.DeliveryStatus(r.URL.Query().Get("status"))
	if status != "" && !validStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown delivery status")
		return
	}
	deliveries, err := a.store.ListDeliveries(r.Context(), store.DeliveryFilter{
		Status: status,
		Limit:  limit,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deliveries})
}

func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func validateEndpointURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("url must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url scheme must be http or https")
	}
	if parsed.User != nil {
		return errors.New("url must not contain credentials")
	}
	return nil
}

func generateSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate endpoint secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func queryLimit(r *http.Request, fallback int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 200 {
		return 0, errors.New("limit must be between 1 and 200")
	}
	return value, nil
}

func validStatus(status domain.DeliveryStatus) bool {
	switch status {
	case domain.DeliveryPending, domain.DeliveryDelivering, domain.DeliverySucceeded,
		domain.DeliveryRetrying, domain.DeliveryDead:
		return true
	default:
		return false
	}
}

func hasDuplicate(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("request panicked", "method", r.Method, "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
