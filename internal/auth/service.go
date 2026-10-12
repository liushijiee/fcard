package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Admin struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Principal struct {
	Admin     Admin
	SessionID uuid.UUID
}

type LoginResult struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	Admin       Admin     `json:"admin"`
}

type Service struct {
	pool       *pgxpool.Pool
	queries    *db.Queries
	sessionTTL time.Duration
	dummyHash  []byte
}

func New(pool *pgxpool.Pool, ttl time.Duration) (*Service, error) {
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("not-a-real-admin-password"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, queries: db.New(pool), sessionTTL: ttl, dummyHash: dummyHash}, nil
}

func (service *Service) CreateAdmin(ctx context.Context, email, password string) (Admin, error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil || len(password) < 12 || len(password) > 72 {
		return Admin{}, domain.ErrInvalid
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Admin{}, err
	}
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return Admin{}, err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	created, err := queries.CreateAdmin(ctx, db.CreateAdminParams{ID: uuid.New(), Email: normalized, PasswordHash: string(passwordHash)})
	if err != nil {
		return Admin{}, domain.DatabaseError(err)
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: created.ID, Action: "admin.initialize", TargetID: created.ID, RequestID: uuid.NewString(), Changes: []byte(`{"source":"local_cli"}`)}); err != nil {
		return Admin{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Admin{}, err
	}
	return adminDTO(created), nil
}

func (service *Service) Login(ctx context.Context, email, password, clientIP, requestID string) (LoginResult, error) {
	normalized, validationErr := domain.NormalizeEmail(email)
	if validationErr != nil {
		normalized = "invalid-address"
	}
	if err := service.checkRate(ctx, "admin:ip:"+clientIP, 20); err != nil {
		return LoginResult{}, err
	}
	if err := service.checkRate(ctx, "admin:email:"+normalized, 5); err != nil {
		return LoginResult{}, err
	}
	admin, err := service.queries.GetAdminByEmail(ctx, normalized)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, err
	}
	passwordHash := service.dummyHash
	if err == nil {
		passwordHash = []byte(admin.PasswordHash)
	}
	compareErr := bcrypt.CompareHashAndPassword(passwordHash, []byte(password))
	if validationErr != nil || err != nil || compareErr != nil || admin.Status != "ACTIVE" {
		return LoginResult{}, domain.ErrUnauthorized
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return LoginResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(service.sessionTTL)
	sessionID := uuid.New()
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	lockedAdmin, err := queries.LockAdminByEmail(ctx, normalized)
	if err != nil {
		return LoginResult{}, err
	}
	if lockedAdmin.Status != "ACTIVE" || lockedAdmin.PasswordHash != admin.PasswordHash {
		return LoginResult{}, domain.ErrUnauthorized
	}
	if err := queries.CreateSession(ctx, db.CreateSessionParams{ID: sessionID, AdminID: admin.ID, TokenHash: tokenHash[:], ExpiresAt: expiresAt}); err != nil {
		return LoginResult{}, err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: admin.ID, Action: "admin.login", TargetID: sessionID, RequestID: requestID, Changes: []byte(`{}`)}); err != nil {
		return LoginResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{AccessToken: token, TokenType: "Bearer", ExpiresAt: expiresAt, Admin: adminDTO(admin)}, nil
}

func (service *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return Principal{}, domain.ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	result, err := service.queries.GetSessionAdmin(ctx, digest[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, domain.ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	return Principal{Admin: Admin{ID: result.ID, Email: result.Email, Status: result.Status, CreatedAt: result.CreatedAt}, SessionID: result.SessionID}, nil
}

func (service *Service) Logout(ctx context.Context, principal Principal, requestID string) error {
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	if err := queries.RevokeSession(ctx, principal.SessionID); err != nil {
		return err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: principal.Admin.ID, Action: "admin.logout", TargetID: principal.SessionID, RequestID: requestID, Changes: []byte(`{}`)}); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (service *Service) checkRate(ctx context.Context, key string, limit int32) error {
	digest := sha256.Sum256([]byte(key))
	count, err := service.queries.ConsumeLoginAttempt(ctx, digest[:])
	if err != nil {
		return err
	}
	if count > limit {
		return domain.ErrRateLimited
	}
	return nil
}

func adminDTO(admin db.Admin) Admin {
	return Admin{ID: admin.ID, Email: admin.Email, Status: admin.Status, CreatedAt: admin.CreatedAt}
}
