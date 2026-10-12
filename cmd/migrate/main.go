package main

import (
	"log/slog"
	"os"

	"fcard/internal/config"
	"fcard/internal/storage"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		slog.Error("configuration invalid", "reason", err.Error())
		os.Exit(1)
	}
	if err := storage.Migrate(settings.DatabaseURL); err != nil {
		slog.Error("migration failed; check database access and migration state")
		os.Exit(1)
	}
	slog.Info("database migrations applied")
}
