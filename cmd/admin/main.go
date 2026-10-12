package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"time"

	"fcard/internal/auth"
	"fcard/internal/config"
	"fcard/internal/domain"
	"fcard/internal/storage"
)

func main() {
	email := flag.String("email", "", "administrator email")
	disable := flag.Bool("disable", false, "disable administrator and revoke all sessions")
	flag.Parse()
	if err := run(*email, *disable); err != nil {
		slog.Error("admin initialization failed", "reason", err.Error())
		os.Exit(1)
	}
}

func run(email string, disable bool) error {
	password := os.Getenv("ADMIN_PASSWORD")
	os.Unsetenv("ADMIN_PASSWORD")
	if email == "" || (!disable && (len(password) < 12 || len(password) > 72)) {
		return errors.New("provide --email and ADMIN_PASSWORD (12 to 72 bytes)")
	}
	settings, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := storage.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return errors.New("database connection failed")
	}
	defer pool.Close()
	if err := storage.Ready(ctx, pool); err != nil {
		return errors.New("run database migrations first")
	}
	service, err := auth.New(pool, settings.SessionTTL)
	if err != nil {
		return errors.New("authentication initialization failed")
	}
	if disable {
		if err := service.DisableAdmin(ctx, email); err != nil {
			return errors.New("could not disable administrator; verify email and database state")
		}
		slog.Info("administrator disabled; sessions revoked")
		return nil
	}
	admin, err := service.CreateAdmin(ctx, email, password)
	if errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrConflict) {
		return err
	}
	if err != nil {
		return errors.New("could not create administrator")
	}
	slog.Info("administrator created", "admin_id", admin.ID)
	return nil
}
