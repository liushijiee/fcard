package user

import (
	"context"
	"encoding/json"
	"time"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID                 uuid.UUID `json:"id"`
	Email              string    `json:"email"`
	DisplayName        string    `json:"display_name"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LoginEnabled       bool      `json:"login_enabled"`
	MustChangePassword bool      `json:"must_change_password"`
}

type CreateInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type UpdateInput struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
	Status      *string `json:"status"`
}

type Audit struct {
	ID        uuid.UUID       `json:"id"`
	AdminID   uuid.UUID       `json:"admin_id"`
	Action    string          `json:"action"`
	TargetID  uuid.UUID       `json:"target_id"`
	RequestID string          `json:"request_id"`
	Changes   json.RawMessage `json:"changes"`
	CreatedAt time.Time       `json:"created_at"`
}

type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int32 `json:"page"`
	PageSize int32 `json:"page_size"`
}

type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, queries: db.New(pool)}
}

func (service *Service) Create(ctx context.Context, adminID uuid.UUID, requestID string, input CreateInput) (User, error) {
	email, err := domain.NormalizeEmail(input.Email)
	if err != nil {
		return User{}, err
	}
	name, err := domain.NormalizeName(input.DisplayName)
	if err != nil {
		return User{}, err
	}
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	created, err := queries.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Email: email, DisplayName: name})
	if err != nil {
		return User{}, domain.DatabaseError(err)
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: adminID, Action: "user.create", TargetID: created.ID, RequestID: requestID, Changes: []byte(`{"fields":["email","display_name","status"]}`)}); err != nil {
		return User{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return User{}, err
	}
	return userDTO(created), nil
}

func (service *Service) Get(ctx context.Context, userID uuid.UUID) (User, error) {
	result, err := service.queries.GetUser(ctx, userID)
	if err != nil {
		return User{}, domain.DatabaseError(err)
	}
	value := userDTO(result.User)
	value.LoginEnabled, value.MustChangePassword = result.LoginEnabled, result.MustChangePassword
	return value, nil
}

func (service *Service) Update(ctx context.Context, adminID uuid.UUID, requestID string, userID uuid.UUID, input UpdateInput) (User, error) {
	if input.Email == nil && input.DisplayName == nil && input.Status == nil {
		return User{}, domain.ErrInvalid
	}
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	existing, err := queries.LockUser(ctx, userID)
	if err != nil {
		return User{}, domain.DatabaseError(err)
	}
	fields := make([]string, 0, 3)
	originalEmail := existing.Email
	if input.Email != nil {
		existing.Email, err = domain.NormalizeEmail(*input.Email)
		if err != nil {
			return User{}, err
		}
		fields = append(fields, "email")
	}
	if input.DisplayName != nil {
		existing.DisplayName, err = domain.NormalizeName(*input.DisplayName)
		if err != nil {
			return User{}, err
		}
		fields = append(fields, "display_name")
	}
	if input.Status != nil {
		if *input.Status != "ACTIVE" && *input.Status != "DISABLED" {
			return User{}, domain.ErrInvalid
		}
		existing.Status = *input.Status
		fields = append(fields, "status")
	}
	updated, err := queries.UpdateUser(ctx, db.UpdateUserParams{ID: userID, Email: existing.Email, DisplayName: existing.DisplayName, Status: existing.Status})
	if err != nil {
		return User{}, domain.DatabaseError(err)
	}
	if originalEmail != existing.Email || existing.Status == "DISABLED" {
		if err := queries.InvalidateUserCredential(ctx, userID); err != nil {
			return User{}, err
		}
		if err := queries.RevokeUserSessions(ctx, userID); err != nil {
			return User{}, err
		}
		if err := recordInvalidation(ctx, queries, adminID, userID, requestID); err != nil {
			return User{}, err
		}
	}
	state, err := queries.GetCredentialState(ctx, userID)
	if err != nil {
		return User{}, err
	}
	changes, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return User{}, err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: adminID, Action: "user.update", TargetID: userID, RequestID: requestID, Changes: changes}); err != nil {
		return User{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return User{}, err
	}
	value := userDTO(updated)
	value.LoginEnabled, value.MustChangePassword = state.LoginEnabled, state.MustChangePassword
	return value, nil
}

func (service *Service) List(ctx context.Context, page, size int32) (Page[User], error) {
	if !validPagination(page, size) {
		return Page[User]{}, domain.ErrInvalid
	}
	results, err := service.queries.ListUsers(ctx, db.ListUsersParams{Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return Page[User]{}, err
	}
	total, err := service.queries.CountUsers(ctx)
	if err != nil {
		return Page[User]{}, err
	}
	items := make([]User, 0, len(results))
	for _, result := range results {
		value := userDTO(result.User)
		value.LoginEnabled, value.MustChangePassword = result.LoginEnabled, result.MustChangePassword
		items = append(items, value)
	}
	return Page[User]{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func (service *Service) ListAudit(ctx context.Context, page, size int32) (Page[Audit], error) {
	if !validPagination(page, size) {
		return Page[Audit]{}, domain.ErrInvalid
	}
	results, err := service.queries.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return Page[Audit]{}, err
	}
	total, err := service.queries.CountAuditLogs(ctx)
	if err != nil {
		return Page[Audit]{}, err
	}
	items := make([]Audit, 0, len(results))
	for _, result := range results {
		items = append(items, Audit{ID: result.ID, AdminID: result.AdminID, Action: result.Action, TargetID: result.TargetID, RequestID: result.RequestID, Changes: result.Changes, CreatedAt: result.CreatedAt})
	}
	return Page[Audit]{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func validPagination(page, size int32) bool {
	return page > 0 && size > 0 && size <= 100 && int64(page-1)*int64(size) <= 2147483647
}

func userDTO(user db.User) User {
	return User{ID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Status: user.Status, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt}
}

func recordInvalidation(ctx context.Context, queries *db.Queries, adminID, userID uuid.UUID, requestID string) error {
	return queries.CreateUserSecurityEvent(ctx, db.CreateUserSecurityEventParams{ID: uuid.New(), UserID: pgtype.UUID{Bytes: userID, Valid: true}, ActorType: "ADMIN", ActorID: pgtype.UUID{Bytes: adminID, Valid: true}, Action: "user.access.invalidate", RequestID: requestID, Changes: []byte(`{"sessions_revoked":true}`)})
}
