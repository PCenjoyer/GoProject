package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PCenjoyer/GoProject/internal/api"
	"github.com/PCenjoyer/GoProject/internal/config"
	"github.com/PCenjoyer/GoProject/internal/database"
	"github.com/PCenjoyer/GoProject/internal/delivery"
	"github.com/PCenjoyer/GoProject/internal/observability"
	"github.com/PCenjoyer/GoProject/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
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

	pool, err := database.Open(rootCtx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(rootCtx, pool, logger); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return nil
	}

	metrics := observability.NewMetrics()
	apiHandler := metrics.HTTPMiddleware(api.New(store.NewPostgres(pool), logger).Routes())
	workerID := workerIdentity()
	runner := delivery.NewRunner(delivery.Config{
		WorkerCount:         cfg.WorkerCount,
		PollInterval:        cfg.PollInterval,
		DeliveryTimeout:     cfg.DeliveryTimeout,
		LeaseDuration:       cfg.DeliveryTimeout + 10*time.Second,
		MaxAttempts:         cfg.MaxAttempts,
		EndpointConcurrency: cfg.EndpointConcurrency,
	}, delivery.NewPostgresStore(pool), logger, workerID)
	runner.SetMetrics(metrics)
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		runner.Run(rootCtx)
	}()
	mux := http.NewServeMux()
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
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
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
		IdleTimeout:       60 * time.Second,
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
