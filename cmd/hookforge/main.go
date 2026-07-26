package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PCenjoyer/GoProject/internal/api"
	"github.com/PCenjoyer/GoProject/internal/auth"
	"github.com/PCenjoyer/GoProject/internal/config"
	"github.com/PCenjoyer/GoProject/internal/database"
	"github.com/PCenjoyer/GoProject/internal/delivery"
	"github.com/PCenjoyer/GoProject/internal/observability"
	"github.com/PCenjoyer/GoProject/internal/secretbox"
	"github.com/PCenjoyer/GoProject/internal/ssrf"
	"github.com/PCenjoyer/GoProject/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "generate-secrets" {
		if err := generateSecrets(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger := newLogger()
	if err := run(logger); err != nil {
		logger.Error("hookforge stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	secretBox, err := secretbox.New(cfg.SecretEncryptionKey)
	if err != nil {
		return err
	}
	pool, err := database.Open(rootCtx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(rootCtx, pool, logger); err != nil {
		return err
	}
	dataStore := store.NewPostgres(pool, secretBox)
	encrypted, err := dataStore.EncryptLegacyEndpointSecrets(rootCtx)
	if err != nil {
		return err
	}
	if encrypted > 0 {
		logger.Info("legacy endpoint secrets encrypted", "count", encrypted)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return nil
	}

	authService := auth.NewService(auth.NewPostgresRepository(pool))
	if len(os.Args) > 1 && os.Args[1] == "provision-tenant" {
		return provisionTenant(rootCtx, authService, os.Args[2:])
	}
	if err := authService.EnsureBootstrap(
		rootCtx,
		cfg.BootstrapTenantSlug,
		cfg.BootstrapTenantName,
		cfg.BootstrapAPIKey,
	); err != nil {
		return err
	}

	outboundPolicy := ssrf.NewPolicy(cfg.AllowPrivateEndpoints)
	metrics := observability.NewMetrics()
	apiHandler := metrics.HTTPMiddleware(
		authService.Middleware(api.New(dataStore, logger, outboundPolicy).Routes()),
	)
	workerID := workerIdentity()
	runner := delivery.NewRunner(delivery.Config{
		WorkerCount:         cfg.WorkerCount,
		PollInterval:        cfg.PollInterval,
		DeliveryTimeout:     cfg.DeliveryTimeout,
		LeaseDuration:       cfg.DeliveryTimeout + 10*time.Second,
		MaxAttempts:         cfg.MaxAttempts,
		EndpointConcurrency: cfg.EndpointConcurrency,
	}, delivery.NewPostgresStore(pool, secretBox), logger, workerID, outboundPolicy)
	runner.SetMetrics(metrics)
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		runner.Run(rootCtx)
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"HookForge","status":"running","health":"/healthz","readiness":"/readyz","api":"/v1"}` + "\n"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/", apiHandler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	diagnosticsMux := http.NewServeMux()
	diagnosticsMux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}))
	diagnosticsMux.HandleFunc("/debug/pprof/", pprof.Index)
	diagnosticsMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	diagnosticsMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	diagnosticsMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	diagnosticsMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	diagnosticsServer := &http.Server{
		Addr:              cfg.DiagnosticsAddr,
		Handler:           diagnosticsMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	serverErr := make(chan error, 2)
	go func() {
		logger.Info("http server started", "address", cfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()
	go func() {
		logger.Info("diagnostics server started", "address", cfg.DiagnosticsAddr)
		serverErr <- diagnosticsServer.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-rootCtx.Done():
	case err := <-serverErr:
		stop()
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := diagnosticsServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	select {
	case <-workersDone:
		return serveErr
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
}

func generateSecrets() error {
	apiKey, err := auth.GenerateToken()
	if err != nil {
		return err
	}
	encryptionKey, err := secretbox.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "HOOKFORGE_BOOTSTRAP_API_KEY=%s\n", apiKey)
	fmt.Fprintf(os.Stdout, "HOOKFORGE_SECRET_ENCRYPTION_KEY=%s\n", encryptionKey)
	databasePassword, err := randomOpaqueSecret(24)
	if err != nil {
		return err
	}
	grafanaPassword, err := randomOpaqueSecret(24)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "HOOKFORGE_POSTGRES_PASSWORD=%s\n", databasePassword)
	fmt.Fprintf(os.Stdout, "HOOKFORGE_GRAFANA_ADMIN_PASSWORD=%s\n", grafanaPassword)
	return nil
}

func randomOpaqueSecret(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate setup secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func provisionTenant(ctx context.Context, service *auth.Service, args []string) error {
	flags := flag.NewFlagSet("provision-tenant", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	slug := flags.String("slug", "", "lowercase tenant slug")
	name := flags.String("name", "", "tenant display name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token, err := service.ProvisionTenant(ctx, *slug, *name)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Tenant %q provisioned. Save this API key now; it is not stored in plaintext:\n%s\n", *slug, token)
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func newLogger() *slog.Logger {
	level := new(slog.LevelVar)
	switch os.Getenv("HOOKFORGE_LOG_LEVEL") {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func workerIdentity() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "hookforge"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
