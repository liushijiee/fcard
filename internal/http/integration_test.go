package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/storage"
	"fcard/internal/user"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL or run scripts/test-integration.ps1 to test a disposable database")
	}
	ctx := context.Background()
	maintenance, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot connect to integration-test database server")
	}
	databaseName := "fcard_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := maintenance.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		maintenance.Close(ctx)
		t.Fatal("test database creation failed; TEST_DATABASE_URL requires CREATEDB permission")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := maintenance.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("failed to remove disposable test database", databaseName)
		}
		maintenance.Close(cleanup)
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	parsed.Path = "/" + databaseName
	if err := storage.Migrate(parsed.String()); err != nil {
		t.Fatalf("test migrations failed: %v", err)
	}
	if err := storage.Migrate(parsed.String()); err != nil {
		t.Fatal("migrations are not repeatable")
	}
	pool, err := storage.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal("test database pool failed")
	}
	t.Cleanup(pool.Close)
	if err := storage.Ready(ctx, pool); err != nil {
		t.Fatal("migrated database is not ready")
	}
	return pool
}

func send(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func dataAs[Result any](t *testing.T, recorder *httptest.ResponseRecorder, expected int) Result {
	t.Helper()
	if recorder.Code != expected {
		t.Fatalf("expected status %d, got %d: %s", expected, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data      Result `json:"data"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.RequestID == "" {
		t.Fatal("invalid response envelope")
	}
	return envelope.Data
}

func expectStatus(t *testing.T, recorder *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if recorder.Code != expected {
		t.Fatalf("expected status %d, got %d: %s", expected, recorder.Code, recorder.Body.String())
	}
}

func TestM1Integration(t *testing.T) {
	pool := integrationDatabase(t)
	ctx := context.Background()
	authService, err := auth.New(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	userService := user.New(pool)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := New(authService, userService, func(ctx context.Context) error { return storage.Ready(ctx, pool) }, logger, nil)
	admin, err := authService.CreateAdmin(ctx, "ADMIN@example.com", "Integration-only-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authService.CreateAdmin(ctx, "admin@example.com", "Integration-only-password-123"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate administrator accepted")
	}
	if _, err := authService.CreateAdmin(ctx, "weak@example.com", "short"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("weak password accepted")
	}
	loginBody := `{"email":"admin@example.com","password":"Integration-only-password-123"}`
	loginResponse := send(router, "POST", "/api/v1/admin/auth/login", "", loginBody)
	login := dataAs[auth.LoginResult](t, loginResponse, 200)
	token := login.AccessToken
	if login.Admin.Email != "admin@example.com" || login.ExpiresAt.Before(time.Now()) || strings.Contains(loginResponse.Body.String(), "password_hash") {
		t.Fatal("invalid or unsafe login response")
	}
	var storedHash []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM admin_sessions WHERE admin_id=$1", admin.ID).Scan(&storedHash); err != nil || string(storedHash) == token {
		t.Fatal("session token was not hashed")
	}
	dataAs[auth.Admin](t, send(router, "GET", "/api/v1/admin/auth/me", token, ""), 200)
	expectStatus(t, send(router, "GET", "/api/v1/admin/users", "", ""), 401)
	expectStatus(t, send(router, "GET", "/api/v1/admin/users", "invalid-token", ""), 401)

	t.Run("user lifecycle and validation", func(t *testing.T) {
		created := dataAs[user.User](t, send(router, "POST", "/api/v1/admin/users", token, `{"email":"USER@example.com","display_name":" Test User "}`), 201)
		if created.Email != "user@example.com" || created.DisplayName != "Test User" {
			t.Fatal("user normalization failed")
		}
		expectStatus(t, send(router, "POST", "/api/v1/admin/users", token, `{"email":"user@example.com","display_name":"Duplicate"}`), 409)
		expectStatus(t, send(router, "POST", "/api/v1/admin/users", token, `{"email":"invalid","display_name":"Test"}`), 400)
		expectStatus(t, send(router, "POST", "/api/v1/admin/users", token, `{"email":"valid@example.com","display_name":" "}`), 400)
		expectStatus(t, send(router, "PATCH", "/api/v1/admin/users/"+created.ID.String(), token, `{}`), 400)
		expectStatus(t, send(router, "PATCH", "/api/v1/admin/users/"+created.ID.String(), token, `{"status":"INVALID"}`), 400)
		disabled := dataAs[user.User](t, send(router, "PATCH", "/api/v1/admin/users/"+created.ID.String(), token, `{"status":"DISABLED"}`), 200)
		if disabled.Status != "DISABLED" {
			t.Fatal("user not disabled")
		}
		dataAs[user.User](t, send(router, "GET", "/api/v1/admin/users/"+created.ID.String(), token, ""), 200)
		expectStatus(t, send(router, "GET", "/api/v1/admin/users/"+uuid.NewString(), token, ""), 404)
		expectStatus(t, send(router, "GET", "/api/v1/admin/users/invalid", token, ""), 400)
		page := dataAs[user.Page[user.User]](t, send(router, "GET", "/api/v1/admin/users?page=1&page_size=20", token, ""), 200)
		if page.Total != 1 || len(page.Items) != 1 {
			t.Fatal("unexpected user page")
		}
		expectStatus(t, send(router, "GET", "/api/v1/admin/users?page_size=101", token, ""), 400)
		audit := dataAs[user.Page[user.Audit]](t, send(router, "GET", "/api/v1/admin/audit-logs", token, ""), 200)
		if audit.Total < 3 {
			t.Fatal("missing audit events")
		}
	})

	t.Run("audit failure rolls back user creation", func(t *testing.T) {
		_, err := userService.Create(ctx, uuid.New(), uuid.NewString(), user.CreateInput{Email: "rollback@example.com", DisplayName: "Rollback"})
		if err == nil {
			t.Fatal("expected audit foreign key failure")
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='rollback@example.com'").Scan(&count); err != nil || count != 0 {
			t.Fatal("user creation was not rolled back")
		}
	})

	t.Run("concurrent duplicate creation", func(t *testing.T) {
		statuses := make(chan int, 2)
		var workers sync.WaitGroup
		for range 2 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				statuses <- send(router, "POST", "/api/v1/admin/users", token, `{"email":"concurrent@example.com","display_name":"Concurrent"}`).Code
			}()
		}
		workers.Wait()
		close(statuses)
		counts := map[int]int{}
		for status := range statuses {
			counts[status]++
		}
		if counts[201] != 1 || counts[409] != 1 {
			t.Fatalf("unexpected statuses: %v", counts)
		}
	})

	t.Run("login rate limiting", func(t *testing.T) {
		for range 5 {
			expectStatus(t, send(router, "POST", "/api/v1/admin/auth/login", "", `{"email":"unknown@example.com","password":"wrong"}`), 401)
		}
		recorder := send(router, "POST", "/api/v1/admin/auth/login", "", `{"email":"unknown@example.com","password":"wrong"}`)
		expectStatus(t, recorder, 429)
		if recorder.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry hint")
		}
	})

	t.Run("session expiry and disabled administrator", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "UPDATE admin_sessions SET expires_at=now()-interval '1 minute' WHERE admin_id=$1", admin.ID); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, send(router, "GET", "/api/v1/admin/auth/me", token, ""), 401)
		newLogin := dataAs[auth.LoginResult](t, send(router, "POST", "/api/v1/admin/auth/login", "", loginBody), 200)
		token = newLogin.AccessToken
		if err := authService.DisableAdmin(ctx, admin.Email); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, send(router, "GET", "/api/v1/admin/auth/me", token, ""), 401)
		expectStatus(t, send(router, "POST", "/api/v1/admin/auth/login", "", loginBody), 401)
		if _, err := pool.Exec(ctx, "UPDATE admins SET status='ACTIVE' WHERE id=$1", admin.ID); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, send(router, "GET", "/api/v1/admin/auth/me", token, ""), 401)
		token = dataAs[auth.LoginResult](t, send(router, "POST", "/api/v1/admin/auth/login", "", loginBody), 200).AccessToken
		expectStatus(t, send(router, "POST", "/api/v1/admin/auth/logout", token, `{}`), 200)
		expectStatus(t, send(router, "GET", "/api/v1/admin/auth/me", token, ""), 401)
	})

	t.Run("dirty migration readiness", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "UPDATE schema_migrations SET dirty=true"); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, send(router, "GET", "/health/ready", "", ""), 503)
		if _, err := pool.Exec(ctx, "UPDATE schema_migrations SET dirty=false"); err != nil {
			t.Fatal(err)
		}
	})
}
