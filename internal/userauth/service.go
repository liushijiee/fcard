package userauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Profile struct {
	ID                 uuid.UUID `json:"id"`
	Email              string    `json:"email"`
	DisplayName        string    `json:"display_name"`
	Status             string    `json:"status"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}

type Principal struct {
	User        Profile
	SessionID   uuid.UUID
	AuthVersion int64
}

type LoginResult struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        Profile   `json:"user"`
}

type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	ttl     time.Duration
	dummy   []byte
}

func New(pool *pgxpool.Pool, ttl time.Duration) (*Service, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte("not-a-real-user-password"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, queries: db.New(pool), ttl: ttl, dummy: hash}, nil
}

func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 72 }

func (service *Service) ResetPassword(ctx context.Context, admin auth.Principal, userID uuid.UUID, password, requestID string) error {
	if !validPassword(password) {
		return domain.ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	profile, err := queries.LockUser(ctx, userID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	if profile.Status != "ACTIVE" {
		return domain.ErrInvalidState
	}
	if err := queries.ResetUserCredential(ctx, db.ResetUserCredentialParams{UserID: userID, PasswordHash: string(hash)}); err != nil {
		return err
	}
	if err := queries.RevokeUserSessions(ctx, userID); err != nil {
		return err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: admin.Admin.ID, Action: "user.password.reset", TargetID: userID, RequestID: requestID, Changes: []byte(`{"must_change_password":true,"sessions_revoked":true}`)}); err != nil {
		return err
	}
	if err := event(ctx, queries, userID, "ADMIN", admin.Admin.ID, "user.password.reset", requestID, []byte(`{}`)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) Login(ctx context.Context, email, password, ip, requestID string) (LoginResult, error) {
	normalized, validationErr := domain.NormalizeEmail(email)
	if validationErr != nil {
		normalized = "invalid-address"
	}
	if err := service.checkRate(ctx, "user:ip:"+ip, 20); err != nil {
		return LoginResult{}, err
	}
	if err := service.checkRate(ctx, "user:email:"+normalized, 5); err != nil {
		return LoginResult{}, err
	}
	result, err := service.queries.GetUserLogin(ctx, normalized)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, err
	}
	hash := service.dummy
	if err == nil {
		hash = []byte(result.UserCredential.PasswordHash)
	}
	compareErr := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if validationErr != nil || err != nil || compareErr != nil || result.User.Status != "ACTIVE" {
		emailHash, ipHash := sha256.Sum256([]byte(normalized)), sha256.Sum256([]byte(ip))
		changes, _ := json.Marshal(map[string]string{"identity_hash": hex.EncodeToString(emailHash[:]), "source_hash": hex.EncodeToString(ipHash[:])})
		if err := event(ctx, service.queries, uuid.Nil, "ANONYMOUS", uuid.Nil, "user.login.failed", requestID, changes); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, domain.ErrUnauthorized
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	profile, err := queries.LockUser(ctx, result.User.ID)
	if err != nil {
		return LoginResult{}, err
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if profile.Status != "ACTIVE" || profile.Email != normalized || credential.PasswordHash != result.UserCredential.PasswordHash || credential.AuthVersion != result.UserCredential.AuthVersion {
		return LoginResult{}, domain.ErrUnauthorized
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return LoginResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	digest := sha256.Sum256([]byte(token))
	expires := time.Now().UTC().Add(service.ttl)
	if err := queries.CreateUserSession(ctx, db.CreateUserSessionParams{ID: uuid.New(), UserID: profile.ID, TokenHash: digest[:], AuthVersion: credential.AuthVersion, ExpiresAt: expires}); err != nil {
		return LoginResult{}, err
	}
	if err := event(ctx, queries, profile.ID, "USER", profile.ID, "user.login", requestID, []byte(`{}`)); err != nil {
		return LoginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{AccessToken: token, TokenType: "Bearer", ExpiresAt: expires, User: profileDTO(profile, credential.MustChangePassword)}, nil
}

func (service *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return Principal{}, domain.ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	result, err := service.queries.GetUserSession(ctx, digest[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, domain.ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	return Principal{User: profileDTO(result.User, result.MustChangePassword), SessionID: result.SessionID, AuthVersion: result.AuthVersion}, nil
}

func (service *Service) ChangePassword(ctx context.Context, principal Principal, current, next, requestID string) error {
	if !validPassword(next) || current == next {
		return domain.ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	profile, err := queries.LockUser(ctx, principal.User.ID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if err != nil {
		return err
	}
	if profile.Status != "ACTIVE" || credential.AuthVersion != principal.AuthVersion {
		return domain.ErrUnauthorized
	}
	if _, err := queries.LockActiveUserSession(ctx, db.LockActiveUserSessionParams{ID: principal.SessionID, UserID: profile.ID}); errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	} else if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(credential.PasswordHash), []byte(current)) != nil {
		return domain.ErrUnauthorized
	}
	if err := queries.ChangeUserCredential(ctx, db.ChangeUserCredentialParams{UserID: profile.ID, PasswordHash: string(hash)}); err != nil {
		return err
	}
	if err := queries.RevokeUserSessions(ctx, profile.ID); err != nil {
		return err
	}
	if err := event(ctx, queries, profile.ID, "USER", profile.ID, "user.password.change", requestID, []byte(`{"sessions_revoked":true}`)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) Logout(ctx context.Context, principal Principal, requestID string) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	profile, err := queries.LockUser(ctx, principal.User.ID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if err != nil {
		return err
	}
	if profile.Status != "ACTIVE" || credential.AuthVersion != principal.AuthVersion {
		return domain.ErrUnauthorized
	}
	if _, err := queries.LockActiveUserSession(ctx, db.LockActiveUserSessionParams{ID: principal.SessionID, UserID: profile.ID}); errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	} else if err != nil {
		return err
	}
	if err := queries.RevokeUserSession(ctx, db.RevokeUserSessionParams{ID: principal.SessionID, UserID: principal.User.ID}); err != nil {
		return err
	}
	if err := event(ctx, queries, principal.User.ID, "USER", principal.User.ID, "user.logout", requestID, []byte(`{}`)); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

func event(ctx context.Context, queries *db.Queries, userID uuid.UUID, actorType string, actorID uuid.UUID, action, requestID string, changes []byte) error {
	return queries.CreateUserSecurityEvent(ctx, db.CreateUserSecurityEventParams{ID: uuid.New(), UserID: pgtype.UUID{Bytes: userID, Valid: userID != uuid.Nil}, ActorType: actorType, ActorID: pgtype.UUID{Bytes: actorID, Valid: actorID != uuid.Nil}, Action: action, RequestID: requestID, Changes: changes})
}

func profileDTO(profile db.User, mustChange bool) Profile {
	return Profile{ID: profile.ID, Email: profile.Email, DisplayName: profile.DisplayName, Status: profile.Status, MustChangePassword: mustChange, CreatedAt: profile.CreatedAt}
}
