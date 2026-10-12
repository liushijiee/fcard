package storage

import (
	"database/sql"
	"errors"

	"fcard/migrations"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var ErrSchemaNotReady = errors.New("database migration version is not ready")

func Migrate(databaseURL string) error {
	connection, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer connection.Close()
	connection.SetMaxOpenConns(1)
	driver, err := postgres.WithInstance(connection, &postgres.Config{})
	if err != nil {
		return err
	}
	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return err
	}
	migration, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return err
	}
	defer migration.Close()
	err = migration.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
