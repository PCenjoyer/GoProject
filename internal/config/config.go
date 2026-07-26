package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr              string
	DiagnosticsAddr       string
	DatabaseURL           string
	DatabaseMaxConns      int32
	ShutdownTimeout       time.Duration
	LogLevel              string
	SecretEncryptionKey   string
	BootstrapAPIKey       string
	BootstrapTenantSlug   string
	BootstrapTenantName   string
	AllowPrivateEndpoints bool
	WorkerCount           int
	PollInterval          time.Duration
	DeliveryTimeout       time.Duration
	MaxAttempts           int
	EndpointConcurrency   int
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            env("HOOKFORGE_HTTP_ADDR", ":8080"),
		DiagnosticsAddr:     env("HOOKFORGE_DIAGNOSTICS_ADDR", ":9090"),
		DatabaseURL:         env("HOOKFORGE_DATABASE_URL", "postgres://hookforge:hookforge@localhost:5432/hookforge?sslmode=disable"),
		LogLevel:            env("HOOKFORGE_LOG_LEVEL", "info"),
		SecretEncryptionKey: env("HOOKFORGE_SECRET_ENCRYPTION_KEY", ""),
		BootstrapAPIKey:     env("HOOKFORGE_BOOTSTRAP_API_KEY", ""),
		BootstrapTenantSlug: env("HOOKFORGE_BOOTSTRAP_TENANT_SLUG", "default"),
		BootstrapTenantName: env("HOOKFORGE_BOOTSTRAP_TENANT_NAME", "Default tenant"),
		DatabaseMaxConns:    20,
		ShutdownTimeout:     15 * time.Second,
		WorkerCount:         16,
		PollInterval:        250 * time.Millisecond,
		DeliveryTimeout:     10 * time.Second,
		MaxAttempts:         8,
		EndpointConcurrency: 2,
	}

	var err error
	if cfg.DatabaseMaxConns, err = int32Env("HOOKFORGE_DATABASE_MAX_CONNS", cfg.DatabaseMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationEnv("HOOKFORGE_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WorkerCount, err = intEnv("HOOKFORGE_WORKER_COUNT", cfg.WorkerCount); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = durationEnv("HOOKFORGE_POLL_INTERVAL", cfg.PollInterval); err != nil {
		return Config{}, err
	}
	if cfg.DeliveryTimeout, err = durationEnv("HOOKFORGE_DELIVERY_TIMEOUT", cfg.DeliveryTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxAttempts, err = intEnv("HOOKFORGE_MAX_ATTEMPTS", cfg.MaxAttempts); err != nil {
		return Config{}, err
	}
	if cfg.EndpointConcurrency, err = intEnv("HOOKFORGE_ENDPOINT_CONCURRENCY", cfg.EndpointConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.AllowPrivateEndpoints, err = boolEnv("HOOKFORGE_ALLOW_PRIVATE_ENDPOINTS", false); err != nil {
		return Config{}, err
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("HOOKFORGE_DATABASE_URL is required")
	}
	if cfg.SecretEncryptionKey == "" {
		return Config{}, errors.New("HOOKFORGE_SECRET_ENCRYPTION_KEY is required; run `hookforge generate-secrets`")
	}
	if cfg.DatabaseMaxConns < 2 || cfg.WorkerCount < 1 || cfg.MaxAttempts < 1 || cfg.EndpointConcurrency < 1 {
		return Config{}, errors.New("connection, worker, attempt, and concurrency limits must be positive")
	}
	return cfg, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func int32Env(key string, fallback int32) (int32, error) {
	value, err := intEnv(key, int(fallback))
	return int32(value), err
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}
