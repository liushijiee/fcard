package config

import (
	"net/url"
	"testing"
)

func environment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"DATABASE_URL", "APP_ENV", "HTTP_ADDR", "SESSION_TTL", "POSTGRES_PASSWORD", "POSTGRES_PORT", "POSTGRES_HOST", "POSTGRES_USER", "POSTGRES_DB", "DATABASE_SSLMODE", "AIRWALLEX_ISSUING_ENABLED", "AIRWALLEX_CLIENT_ID", "AIRWALLEX_API_KEY", "AIRWALLEX_API_VERSION", "AIRWALLEX_CARD_PROGRAM_PURPOSE", "AIRWALLEX_CARD_LIMIT_CENTS"} {
		t.Setenv(key, "")
	}
}

func TestDatabaseURLAndDefaults(t *testing.T) {
	environment(t)
	t.Setenv("POSTGRES_PASSWORD", "p@ss:/?#word")
	config, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if password != "p@ss:/?#word" || parsed.Host != "127.0.0.1:55434" || config.HTTPAddr != "127.0.0.1:8081" {
		t.Fatal("unexpected configuration")
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, test := range []struct{ name, key, value string }{
		{"production disabled", "APP_ENV", "production"},
		{"bad duration", "SESSION_TTL", "wrong"},
		{"short duration", "SESSION_TTL", "1s"},
		{"bad port", "POSTGRES_PORT", "0"},
		{"bad address", "HTTP_ADDR", "missing-port"},
		{"bad URL", "DATABASE_URL", "http://localhost/db"},
		{"invalid feature flag", "AIRWALLEX_ISSUING_ENABLED", "yes"},
		{"unsafe sandbox limit", "AIRWALLEX_CARD_LIMIT_CENTS", "10001"},
		{"zero sandbox limit", "AIRWALLEX_CARD_LIMIT_CENTS", "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment(t)
			t.Setenv("POSTGRES_PASSWORD", "test-only")
			t.Setenv(test.key, test.value)
			if _, err := FromEnvironment(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	t.Run("missing credentials", func(t *testing.T) {
		environment(t)
		if _, err := FromEnvironment(); err == nil {
			t.Fatal("missing credentials accepted")
		}
	})
}

func TestIssuingConfiguration(t *testing.T) {
	environment(t)
	t.Setenv("POSTGRES_PASSWORD", "test-only")
	t.Setenv("AIRWALLEX_ISSUING_ENABLED", "true")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("enabled without credentials")
	}
	t.Setenv("AIRWALLEX_CLIENT_ID", "test-client")
	t.Setenv("AIRWALLEX_API_KEY", "test-key")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("implicit card program accepted")
	}
	t.Setenv("AIRWALLEX_CARD_PROGRAM_PURPOSE", "CONSUMER")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("consumer program accepted for company virtual cards")
	}
	t.Setenv("AIRWALLEX_CARD_PROGRAM_PURPOSE", "COMMERCIAL")
	settings, err := FromEnvironment()
	if err != nil || !settings.IssuingEnabled || settings.CardLimitCents != 1000 {
		t.Fatal("valid sandbox configuration rejected")
	}
	t.Setenv("AIRWALLEX_API_VERSION", "2024-02-22")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("incompatible version accepted")
	}
}
