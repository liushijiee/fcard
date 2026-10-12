package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/storage"
	"fcard/internal/user"
	"fcard/internal/userauth"
	"fcard/migrations"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const temporaryPassword = "Sandbox-temporary-password-123"
const permanentPassword = "Sandbox-permanent-password-456"

type userFixture struct {
	pool       *pgxpool.Pool
	router     *gin.Engine
	service    *userauth.Service
	users      *user.Service
	admin      auth.Principal
	adminToken string
}

func newUserFixture(t *testing.T) userFixture {
	t.Helper()
	pool := integrationDatabase(t)
	ctx := context.Background()
	adminService, err := auth.New(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminService.CreateAdmin(ctx, "shared@example.com", permanentPassword); err != nil {
		t.Fatal(err)
	}
	login, err := adminService.Login(ctx, "shared@example.com", permanentPassword, "127.0.0.1", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	principal, err := adminService.Authenticate(ctx, login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	service, err := userauth.New(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	users := user.New(pool)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := New(adminService, users, func(ctx context.Context) error { return storage.Ready(ctx, pool) }, logger, service)
	api := &API{userAuth: service, logger: logger}
	router.GET("/test/user-ready", api.requireUserSession(), api.requireUserReady(), func(request *gin.Context) { request.Status(200) })
	return userFixture{pool: pool, router: router, service: service, users: users, admin: principal, adminToken: login.AccessToken}
}

func (fixture userFixture) create(t *testing.T, email string) user.User {
	t.Helper()
	profile, err := fixture.users.Create(context.Background(), fixture.admin.Admin.ID, uuid.NewString(), user.CreateInput{Email: email, DisplayName: "Sandbox user"})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func (fixture userFixture) reset(t *testing.T, id uuid.UUID, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"temporary_password": password})
	response := send(fixture.router, "POST", "/api/v1/admin/users/"+id.String()+"/password/reset", fixture.adminToken, string(body))
	expectStatus(t, response, 200)
	if strings.Contains(response.Body.String(), password) {
		t.Fatal("password echoed")
	}
}

func (fixture userFixture) login(t *testing.T, email, password string) userauth.LoginResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return dataAs[userauth.LoginResult](t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", string(body)), 200)
}

func TestUserAuthLifecycle(t *testing.T) {
	fixture := newUserFixture(t)
	profile := fixture.create(t, "shared@example.com")
	if profile.LoginEnabled || profile.MustChangePassword {
		t.Fatal("credentials automatically initialized")
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"shared@example.com","password":"Sandbox-temporary-password-123"}`), 401)
	fixture.reset(t, profile.ID, temporaryPassword)
	joined := dataAs[user.User](t, send(fixture.router, "GET", "/api/v1/admin/users/"+profile.ID.String(), fixture.adminToken, ""), 200)
	if joined.ID != profile.ID || !joined.LoginEnabled || !joined.MustChangePassword {
		t.Fatal("credential state incorrect")
	}
	login := fixture.login(t, profile.Email, temporaryPassword)
	second := fixture.login(t, profile.Email, temporaryPassword)
	if !login.User.MustChangePassword {
		t.Fatal("first login unrestricted")
	}
	me := dataAs[userauth.Profile](t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 200)
	if me.ID != profile.ID || me.ID == fixture.admin.Admin.ID {
		t.Fatal("identity merged")
	}
	expectStatus(t, send(fixture.router, "GET", "/api/v1/admin/users", login.AccessToken, ""), 401)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", fixture.adminToken, ""), 401)
	blocked := send(fixture.router, "GET", "/test/user-ready", login.AccessToken, "")
	expectStatus(t, blocked, 403)
	if !strings.Contains(blocked.Body.String(), "password_change_required") {
		t.Fatal("incorrect authorization code")
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"Sandbox-temporary-password-123","new_password":"short"}`), 400)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"Sandbox-temporary-password-123","new_password":"Sandbox-temporary-password-123"}`), 400)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"Sandbox-temporary-password-123","new_password":"Sandbox-permanent-password-456","user_id":"other"}`), 400)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"wrong","new_password":"Sandbox-permanent-password-456"}`), 401)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"Sandbox-temporary-password-123","new_password":"Sandbox-permanent-password-456"}`), 200)
	for _, token := range []string{login.AccessToken, second.AccessToken} {
		expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", token, ""), 401)
	}
	login = fixture.login(t, profile.Email, permanentPassword)
	if login.User.MustChangePassword {
		t.Fatal("permanent password still restricted")
	}
	expectStatus(t, send(fixture.router, "GET", "/test/user-ready", login.AccessToken, ""), 200)
	other := fixture.create(t, "other@example.com")
	fixture.reset(t, other.ID, temporaryPassword)
	otherLogin := fixture.login(t, other.Email, temporaryPassword)
	otherMe := dataAs[userauth.Profile](t, send(fixture.router, "GET", "/api/v1/user/auth/me?user_id="+profile.ID.String(), otherLogin.AccessToken, ""), 200)
	if otherMe.ID != other.ID {
		t.Fatal("client selected another identity")
	}
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/users/"+profile.ID.String(), otherLogin.AccessToken, ""), 404)
	fixture.reset(t, profile.ID, temporaryPassword)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 401)
	if _, err := fixture.pool.Exec(context.Background(), "DELETE FROM login_rate_limits"); err != nil {
		t.Fatal(err)
	}
	login = fixture.login(t, profile.Email, temporaryPassword)
	path := "/api/v1/admin/users/" + profile.ID.String()
	expectStatus(t, send(fixture.router, "PATCH", path, fixture.adminToken, `{"email":"new@example.com"}`), 200)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 401)
	login = fixture.login(t, "new@example.com", temporaryPassword)
	expectStatus(t, send(fixture.router, "PATCH", path, fixture.adminToken, `{"status":"DISABLED"}`), 200)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 401)
	expectStatus(t, send(fixture.router, "POST", path+"/password/reset", fixture.adminToken, `{"temporary_password":"Sandbox-temporary-password-123"}`), 409)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"new@example.com","password":"Sandbox-temporary-password-123"}`), 401)
	expectStatus(t, send(fixture.router, "PATCH", path, fixture.adminToken, `{"status":"ACTIVE"}`), 200)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 401)
	login = fixture.login(t, "new@example.com", temporaryPassword)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/logout", login.AccessToken, `{}`), 200)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 401)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/admin/auth/me", fixture.adminToken, ""), 200)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", otherLogin.AccessToken, ""), 200)
	if _, err := fixture.pool.Exec(context.Background(), "UPDATE user_sessions SET expires_at=now()-interval '1 minute' WHERE user_id=$1", other.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", otherLogin.AccessToken, ""), 401)
	var unsafe int
	if err := fixture.pool.QueryRow(context.Background(), "SELECT count(*) FROM user_security_events WHERE changes::text LIKE '%password-123%' OR changes::text LIKE '%password-456%'").Scan(&unsafe); err != nil || unsafe != 0 {
		t.Fatal("unsafe events")
	}
	var digest []byte
	if err := fixture.pool.QueryRow(context.Background(), "SELECT token_hash FROM user_sessions WHERE user_id=$1 LIMIT 1", profile.ID).Scan(&digest); err != nil || len(digest) != 32 {
		t.Fatal("invalid session hash")
	}
}

func TestUserAuthRateIsolation(t *testing.T) {
	fixture := newUserFixture(t)
	for range 5 {
		expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"shared@example.com","password":"wrong"}`), 401)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"shared@example.com","password":"wrong"}`), 429)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/admin/auth/login", "", `{"email":"shared@example.com","password":"Sandbox-permanent-password-456"}`), 200)
	ipHash := sha256.Sum256([]byte("user:ip:127.0.0.1"))
	if _, err := fixture.pool.Exec(context.Background(), "UPDATE login_rate_limits SET attempts=20 WHERE key_hash=$1", ipHash[:]); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"blocked-new@example.com","password":"wrong"}`), 429)
	emailHash := sha256.Sum256([]byte("user:email:blocked-new@example.com"))
	var records int
	if err := fixture.pool.QueryRow(context.Background(), "SELECT count(*) FROM login_rate_limits WHERE key_hash=$1", emailHash[:]).Scan(&records); err != nil || records != 0 {
		t.Fatal("blocked source created additional identity records")
	}
}

func TestUserAuthAtomicity(t *testing.T) {
	fixture := newUserFixture(t)
	profile := fixture.create(t, "atomic@example.com")
	fixture.reset(t, profile.ID, temporaryPassword)
	login := fixture.login(t, profile.Email, temporaryPassword)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events ADD CONSTRAINT test_reject_event CHECK (action <> 'user.password.change' AND action <> 'user.password.reset') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/change-password", login.AccessToken, `{"current_password":"Sandbox-temporary-password-123","new_password":"Sandbox-permanent-password-456"}`), 500)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/admin/users/"+profile.ID.String()+"/password/reset", fixture.adminToken, `{"temporary_password":"Sandbox-permanent-password-456"}`), 500)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 200)
	var version, audits int
	if err := fixture.pool.QueryRow(ctx, "SELECT auth_version FROM user_credentials WHERE user_id=$1", profile.ID).Scan(&version); err != nil || version != 1 {
		t.Fatal("credential update not rolled back")
	}
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='user.password.reset' AND target_id=$1", profile.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("audit not rolled back")
	}
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events DROP CONSTRAINT test_reject_event"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE audit_logs ADD CONSTRAINT test_reject_reset CHECK (action <> 'user.password.reset') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/admin/users/"+profile.ID.String()+"/password/reset", fixture.adminToken, `{"temporary_password":"Sandbox-permanent-password-456"}`), 500)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 200)
}

func TestUserAuthEventFailureRollback(t *testing.T) {
	fixture := newUserFixture(t)
	profile := fixture.create(t, "event-rollback@example.com")
	fixture.reset(t, profile.ID, temporaryPassword)
	login := fixture.login(t, profile.Email, temporaryPassword)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE user_security_events ADD CONSTRAINT test_reject_event CHECK (action NOT IN ('user.logout','user.login','user.access.invalidate')) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/logout", login.AccessToken, `{}`), 500)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 200)
	expectStatus(t, send(fixture.router, "POST", "/api/v1/user/auth/login", "", `{"email":"event-rollback@example.com","password":"Sandbox-temporary-password-123"}`), 500)
	expectStatus(t, send(fixture.router, "PATCH", "/api/v1/admin/users/"+profile.ID.String(), fixture.adminToken, `{"status":"DISABLED"}`), 500)
	expectStatus(t, send(fixture.router, "GET", "/api/v1/user/auth/me", login.AccessToken, ""), 200)
	var sessions, version int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM user_sessions WHERE user_id=$1", profile.ID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("failed login left a session")
	}
	if err := fixture.pool.QueryRow(ctx, "SELECT auth_version FROM user_credentials WHERE user_id=$1", profile.ID).Scan(&version); err != nil || version != 1 {
		t.Fatal("invalidation not rolled back")
	}
}

func TestUserAuthConcurrentChanges(t *testing.T) {
	fixture := newUserFixture(t)
	ctx := context.Background()
	profile := fixture.create(t, "concurrent-change@example.com")
	fixture.reset(t, profile.ID, temporaryPassword)
	first, second := fixture.login(t, profile.Email, temporaryPassword), fixture.login(t, profile.Email, temporaryPassword)
	statuses := make(chan int, 2)
	for _, token := range []string{first.AccessToken, second.AccessToken} {
		go func() {
			statuses <- send(fixture.router, "POST", "/api/v1/user/auth/change-password", token, `{"current_password":"Sandbox-temporary-password-123","new_password":"Sandbox-permanent-password-456"}`).Code
		}()
	}
	counts := map[int]int{}
	for range 2 {
		counts[<-statuses]++
	}
	if counts[200] != 1 || counts[401] != 1 {
		t.Fatalf("concurrent changes: %v", counts)
	}
	for _, operation := range []string{"reset", "disable", "email"} {
		t.Run(operation, func(t *testing.T) {
			person := fixture.create(t, operation+"-race@example.com")
			fixture.reset(t, person.ID, temporaryPassword)
			tx, err := fixture.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", person.ID); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				result userauth.LoginResult
				err    error
			}
			logins := make(chan outcome, 1)
			changes := make(chan error, 1)
			go func() {
				result, err := fixture.service.Login(ctx, person.Email, temporaryPassword, "race-ip", uuid.NewString())
				logins <- outcome{result, err}
			}()
			go func() {
				if operation == "reset" {
					changes <- fixture.service.ResetPassword(ctx, fixture.admin, person.ID, permanentPassword, uuid.NewString())
					return
				}
				status, email := "DISABLED", "changed-"+person.Email
				input := user.UpdateInput{Status: &status}
				if operation == "email" {
					input = user.UpdateInput{Email: &email}
				}
				_, err := fixture.users.Update(ctx, fixture.admin.Admin.ID, uuid.NewString(), person.ID, input)
				changes <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting int
				if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%name: LockUser :one%'").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting >= 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("concurrent workers did not reach user lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-changes; err != nil {
				t.Fatal(err)
			}
			result := <-logins
			if result.err != nil && !errors.Is(result.err, domain.ErrUnauthorized) {
				t.Fatal(result.err)
			}
			if result.err == nil {
				expectStatus(t, send(fixture.router, http.MethodGet, "/api/v1/user/auth/me", result.result.AccessToken, ""), 401)
			}
		})
	}
}

func TestUserAuthMigrationPreservesUsers(t *testing.T) {
	pool := integrationDatabase(t)
	ctx := context.Background()
	down, err := migrations.Files.ReadFile("000002_user_auth.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	slotDown, err := migrations.Files.ReadFile("000005_card_slots.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	companyDown, err := migrations.Files.ReadFile("000004_company_issuing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	cardDown, err := migrations.Files.ReadFile("000003_sandbox_cards.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(slotDown)+string(companyDown)+string(cardDown)+string(down)+" UPDATE schema_migrations SET version=1;"); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users(id,email,display_name) VALUES($1,'historical@example.com','Historical')", id); err != nil {
		t.Fatal(err)
	}
	if storage.Ready(ctx, pool) == nil {
		t.Fatal("old schema marked ready")
	}
	if err := storage.Migrate(pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	var actual uuid.UUID
	var credentials int
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email='historical@example.com'").Scan(&actual); err != nil || actual != id {
		t.Fatal("old identity changed")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_credentials").Scan(&credentials); err != nil || credentials != 0 {
		t.Fatal("default credentials created")
	}
	if err := storage.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
