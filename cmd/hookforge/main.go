package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PCenjoyer/GoProject/internal/api"
	"github.com/PCenjoyer/GoProject/internal/config"
	"github.com/PCenjoyer/GoProject/internal/database"
	"github.com/PCenjoyer/GoProject/internal/delivery"
	"github.com/PCenjoyer/GoProject/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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

	apiHandler := api.New(store.NewPostgres(pool), logger).Routes()
	workerID := workerIdentity()
	runner := delivery.NewRunner(delivery.Config{
		WorkerCount:         cfg.WorkerCount,
		PollInterval:        cfg.PollInterval,
		DeliveryTimeout:     cfg.DeliveryTimeout,
		LeaseDuration:       cfg.DeliveryTimeout + 10*time.Second,
		MaxAttempts:         cfg.MaxAttempts,
		EndpointConcurrency: cfg.EndpointConcurrency,
	}, delivery.NewPostgresStore(pool), logger, workerID)
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
		ReadHeaderTimeout: 5_000_000_000,
		IdleTimeout:       60_000_000_000,
	}
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server started", "address", cfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case <-rootCtx.Done():
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	select {
	case <-workersDone:
		return nil
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
}

func workerIdentity() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "hookforge"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
