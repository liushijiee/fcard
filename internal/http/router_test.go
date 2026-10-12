package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicAndProtectedRoutes(t *testing.T) {
	router := New(nil, nil, func(context.Context) error { return errors.New("sensitive database failure") }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/health/live", 200},
		{"GET", "/health/ready", 503},
		{"GET", "/api/v1/admin/users", 401},
		{"POST", "/api/v1/admin/users", 401},
		{"PATCH", "/api/v1/admin/users/test", 401},
		{"GET", "/api/v1/admin/audit-logs", 401},
		{"POST", "/api/v1/admin/users/test/password/reset", 401},
		{"GET", "/api/v1/user/auth/me", 401},
		{"POST", "/api/v1/user/auth/change-password", 401},
		{"POST", "/api/v1/user/auth/logout", 401},
		{"GET", "/api/v1/user/cards", 401},
		{"POST", "/api/v1/user/cards", 401},
		{"GET", "/api/v1/user/cards/config", 401},
		{"GET", "/api/v1/user/card-operations/test", 401},
		{"POST", "/api/v1/user/card-operations/test/retry", 401},
		{"POST", "/api/v1/admin/company/cardholder", 401},
		{"GET", "/api/v1/admin/company/cardholder", 401},
		{"GET", "/unknown", 404},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("X-Request-ID", "not-a-valid-id")
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status || recorder.Header().Get("X-Request-ID") == "" {
			t.Errorf("%s %s: unexpected response %d", test.method, test.path, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "sensitive") {
			t.Fatal("internal error leaked")
		}
	}
}

func TestInvalidLoginJSON(t *testing.T) {
	router := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, test := range []struct {
		body, contentType string
		status            int
	}{
		{`{"email":"x"}`, "text/plain", 415},
		{`{`, "application/json", 400},
		{`{"unexpected":"secret"}`, "application/json", 400},
		{`{} {}`, "application/json", 400},
		{strings.Repeat("x", 17000), "application/json", 400},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/login", strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("unexpected response: %d", recorder.Code)
		}
	}
}

func TestPagination(t *testing.T) {
	for _, query := range []string{"?page=0", "?page_size=101", "?page=x", "?page=2147483647&page_size=100"} {
		recorder := httptest.NewRecorder()
		request, _ := gin.CreateTestContext(recorder)
		request.Request = httptest.NewRequest(http.MethodGet, "/"+query, nil)
		if _, _, err := pagination(request); err == nil {
			t.Errorf("invalid pagination accepted: %s", query)
		}
	}
}
