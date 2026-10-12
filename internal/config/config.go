package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"fcard/internal/airwallex"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	SessionTTL         time.Duration
	IssuingEnabled     bool
	Airwallex          airwallex.Config
	CardLimitCents     int64
	CardProgramPurpose string
}

func Load() (Config, error) {
	for _, filename := range []string{".env.backend", ".env.postgres"} {
		if err := godotenv.Load(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("load %s: %w", filename, err)
		}
	}
	return FromEnvironment()
}

func FromEnvironment() (Config, error) {
	if value("APP_ENV", "sandbox") != "sandbox" {
		return Config{}, errors.New("M1 only supports APP_ENV=sandbox")
	}
	config := Config{HTTPAddr: value("HTTP_ADDR", "127.0.0.1:8081")}
	if _, _, err := net.SplitHostPort(config.HTTPAddr); err != nil {
		return Config{}, errors.New("HTTP_ADDR must contain host and port")
	}
	ttl, err := time.ParseDuration(value("SESSION_TTL", "8h"))
	if err != nil || ttl < time.Minute || ttl > 24*time.Hour {
		return Config{}, errors.New("SESSION_TTL must be between 1m and 24h")
	}
	config.SessionTTL = ttl
	enabled := value("AIRWALLEX_ISSUING_ENABLED", "false")
	if enabled != "true" && enabled != "false" {
		return Config{}, errors.New("AIRWALLEX_ISSUING_ENABLED must be true or false")
	}
	config.IssuingEnabled = enabled == "true"
	config.Airwallex = airwallex.Config{ClientID: os.Getenv("AIRWALLEX_CLIENT_ID"), APIKey: os.Getenv("AIRWALLEX_API_KEY"), APIVersion: value("AIRWALLEX_API_VERSION", "2026-08-21")}
	config.CardProgramPurpose = os.Getenv("AIRWALLEX_CARD_PROGRAM_PURPOSE")
	config.CardLimitCents, err = strconv.ParseInt(value("AIRWALLEX_CARD_LIMIT_CENTS", "1000"), 10, 64)
	if err != nil || config.CardLimitCents < 1 || config.CardLimitCents > 10000 {
		return Config{}, errors.New("AIRWALLEX_CARD_LIMIT_CENTS must be between 1 and 10000")
	}
	if config.IssuingEnabled {
		if config.Airwallex.ClientID == "" || config.Airwallex.APIKey == "" {
			return Config{}, errors.New("Airwallex client ID and API key are required when issuing is enabled")
		}
		if config.Airwallex.APIVersion != "2026-08-21" {
			return Config{}, errors.New("sandbox issuing currently requires AIRWALLEX_API_VERSION=2026-08-21")
		}
		if config.CardProgramPurpose != "COMMERCIAL" {
			return Config{}, errors.New("company virtual cards require AIRWALLEX_CARD_PROGRAM_PURPOSE=COMMERCIAL for your approved sandbox program")
		}
	}
	config.DatabaseURL = os.Getenv("DATABASE_URL")
	if config.DatabaseURL == "" {
		password := os.Getenv("POSTGRES_PASSWORD")
		if password == "" {
			return Config{}, errors.New("POSTGRES_PASSWORD or DATABASE_URL is required")
		}
		port := value("POSTGRES_PORT", "55434")
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return Config{}, errors.New("invalid POSTGRES_PORT")
		}
		databaseURL := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(value("POSTGRES_USER", "fcard"), password),
			Host:   net.JoinHostPort(value("POSTGRES_HOST", "127.0.0.1"), port),
			Path:   "/" + value("POSTGRES_DB", "fcard"),
		}
		databaseURL.RawQuery = url.Values{"sslmode": {value("DATABASE_SSLMODE", "disable")}}.Encode()
		config.DatabaseURL = databaseURL.String()
	}
	parsed, err := url.Parse(config.DatabaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Path == "" {
		return Config{}, errors.New("DATABASE_URL must be a PostgreSQL URL with a database name")
	}
	return config, nil
}

func value(name, fallback string) string {
	if result := os.Getenv(name); result != "" {
		return result
	}
	return fallback
}
