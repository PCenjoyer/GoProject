//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/PCenjoyer/GoProject/internal/auth"
	"github.com/PCenjoyer/GoProject/internal/database"
	"github.com/PCenjoyer/GoProject/internal/delivery"
	"github.com/PCenjoyer/GoProject/internal/secretbox"
	"github.com/PCenjoyer/GoProject/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTenantIsolationAndEncryptedDeliverySecret(t *testing.T) {
	databaseURL := os.Getenv("HOOKFORGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("HOOKFORGE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(ctx, pool, logger); err != nil {
		t.Fatal(err)
	}

	encodedKey, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(encodedKey)
	if err != nil {
		t.Fatal(err)
	}
	dataStore := store.NewPostgres(pool, box)
	authService := auth.NewService(auth.NewPostgresRepository(pool))

	tokenA, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.EnsureBootstrap(ctx, "tenant-a", "Tenant A", tokenA); err != nil {
		t.Fatal(err)
	}
	principalA, err := authService.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := authService.ProvisionTenant(ctx, "tenant-b", "Tenant B")
	if err != nil {
		t.Fatal(err)
	}
	principalB, err := authService.Authenticate(ctx, tokenB)
	if err != nil {
		t.Fatal(err)
	}

	endpointA, err := dataStore.CreateEndpoint(ctx, store.CreateEndpointParams{
		TenantID: principalA.TenantID,
		Name:     "tenant-a-receiver",
		URL:      "https://example.com/hooks/a",
		Secret:   "tenant-a-webhook-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpointB, err := dataStore.CreateEndpoint(ctx, store.CreateEndpointParams{
		TenantID: principalB.TenantID,
		Name:     "tenant-b-receiver",
		URL:      "https://example.com/hooks/b",
		Secret:   "tenant-b-webhook-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	endpointsA, err := dataStore.ListEndpoints(ctx, principalA.TenantID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpointsA) != 1 || endpointsA[0].ID != endpointA.ID {
		t.Fatalf("tenant A endpoints leaked or missing: %+v", endpointsA)
	}
	endpointsB, err := dataStore.ListEndpoints(ctx, principalB.TenantID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpointsB) != 1 || endpointsB[0].ID != endpointB.ID {
		t.Fatalf("tenant B endpoints leaked or missing: %+v", endpointsB)
	}

	eventA, err := dataStore.CreateEvent(ctx, store.CreateEventParams{
		TenantID:       principalA.TenantID,
		IdempotencyKey: "shared-key",
		Type:           "order.created",
		Payload:        []byte(`{"tenant":"a"}`),
		EndpointIDs:    []string{endpointA.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	eventB, err := dataStore.CreateEvent(ctx, store.CreateEventParams{
		TenantID:       principalB.TenantID,
		IdempotencyKey: "shared-key",
		Type:           "order.created",
		Payload:        []byte(`{"tenant":"b"}`),
		EndpointIDs:    []string{endpointB.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventA.Event.ID == eventB.Event.ID {
		t.Fatal("idempotency key was shared across tenants")
	}
	if _, err := dataStore.GetEvent(ctx, principalB.TenantID, eventA.Event.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant event read returned %v", err)
	}
	if _, err := dataStore.CreateEvent(ctx, store.CreateEventParams{
		TenantID:       principalB.TenantID,
		IdempotencyKey: "cross-tenant-endpoint",
		Type:           "order.created",
		Payload:        []byte(`{}`),
		EndpointIDs:    []string{endpointA.ID},
	}); !errors.Is(err, store.ErrInvalidEndpoint) {
		t.Fatalf("cross-tenant endpoint accepted: %v", err)
	}

	var plaintext *string
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `
		SELECT secret, secret_ciphertext
		FROM endpoints
		WHERE id = $1`,
		endpointA.ID,
	).Scan(&plaintext, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if plaintext != nil || len(ciphertext) == 0 || string(ciphertext) == "tenant-a-webhook-secret" {
		t.Fatal("endpoint secret was not encrypted at rest")
	}

	task, ok, err := delivery.NewPostgresStore(pool, box).Claim(ctx, "integration-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("delivery was not claimable")
	}
	if task.Secret != "tenant-a-webhook-secret" && task.Secret != "tenant-b-webhook-secret" {
		t.Fatal("delivery secret did not decrypt")
	}
}
