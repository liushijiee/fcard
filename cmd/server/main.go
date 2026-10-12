package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fcard/internal/airwallex"
	"fcard/internal/auth"
	"fcard/internal/card"
	"fcard/internal/config"
	httpapi "fcard/internal/http"
	"fcard/internal/storage"
	"fcard/internal/storage/db"
	"fcard/internal/user"
	"fcard/internal/userauth"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := storage.Open(startup, settings.DatabaseURL)
	cancel()
	if err != nil {
		return errors.New("database connection failed; check local database configuration")
	}
	defer pool.Close()
	if err := storage.Ready(ctx, pool); err != nil {
		return errors.New("database not migrated; run go run ./cmd/migrate first")
	}
	authService, err := auth.New(pool, settings.SessionTTL)
	if err != nil {
		return errors.New("authentication initialization failed")
	}
	userAuth, err := userauth.New(pool, settings.SessionTTL)
	if err != nil {
		return errors.New("user authentication initialization failed")
	}
	cardService := card.New(pool, airwallex.New(settings.Airwallex), card.Options{Enabled: settings.IssuingEnabled, LimitCents: settings.CardLimitCents, ProgramPurpose: settings.CardProgramPurpose})
	workerDone := make(chan struct{})
	defer func() { stop(); <-workerDone }()
	go func() { defer close(workerDone); cardService.Run(ctx, logger) }()
	router := httpapi.New(authService, user.New(pool), func(ctx context.Context) error {
		return storage.Ready(ctx, pool)
	}, logger, userAuth, cardService)
	server := &http.Server{
		Addr:              settings.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := db.New(pool).CleanupSecurityRecords(cleanup)
				cancel()
				if err != nil {
					logger.Warn("security record cleanup failed")
				}
			}
		}
	}()
	logger.Info("server listening", "address", settings.HTTPAddr, "environment", "sandbox")
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP listener failed; check HTTP_ADDR and port availability")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return errors.New("HTTP shutdown timed out")
		}
	}
	return nil
}
