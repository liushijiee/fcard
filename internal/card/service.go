package card

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"fcard/internal/airwallex"
	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/storage/db"
	"fcard/internal/user"
	"fcard/internal/userauth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Provider interface {
	GetCardholder(context.Context, string) (airwallex.Cardholder, error)
	CreateCard(context.Context, json.RawMessage) (airwallex.Card, error)
	GetCard(context.Context, string) (airwallex.Card, error)
}
type Options struct {
	Enabled        bool
	LimitCents     int64
	ProgramPurpose string
}
type Service struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	provider Provider
	options  Options
}

func New(pool *pgxpool.Pool, provider Provider, options Options) *Service {
	return &Service{pool: pool, queries: db.New(pool), provider: provider, options: options}
}

type Binding struct {
	Bound        bool   `json:"bound"`
	CardholderID string `json:"provider_cardholder_id,omitempty"`
	LegalName    string `json:"legal_name,omitempty"`
}
type Availability struct {
	Enabled           bool   `json:"enabled"`
	CompanyConfigured bool   `json:"company_configured"`
	LimitCents        int64  `json:"limit_cents"`
	Currency          string `json:"currency"`
	MaxCards          int    `json:"max_cards"`
	OccupiedSlots     int64  `json:"occupied_slots"`
}
type Card struct {
	ID          uuid.UUID `json:"id"`
	Nickname    string    `json:"nick_name"`
	Status      string    `json:"status"`
	LastFour    string    `json:"last_four"`
	LimitCents  int64     `json:"limit_cents"`
	Currency    string    `json:"currency"`
	OperationID uuid.UUID `json:"operation_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Operation struct {
	ID        uuid.UUID `json:"id"`
	CardID    uuid.UUID `json:"card_id"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code,omitempty"`
	Attempts  int32     `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type CreateInput struct {
	Nickname string `json:"nick_name"`
}

func (service *Service) enabled() error {
	if !service.options.Enabled || service.provider == nil {
		return domain.ErrIssuingDisabled
	}
	return nil
}
func (service *Service) Binding(ctx context.Context) (Binding, error) {
	holder, err := service.queries.GetCompanyIssuingProfile(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, nil
	}
	if err != nil {
		return Binding{}, err
	}
	return Binding{Bound: true, CardholderID: holder.ProviderCardholderID, LegalName: holder.LegalName}, nil
}
func (service *Service) Bind(ctx context.Context, admin auth.Principal, holderID, requestID string) (Binding, error) {
	if err := service.enabled(); err != nil {
		return Binding{}, err
	}
	parsed, err := uuid.Parse(holderID)
	if err != nil || parsed == uuid.Nil {
		return Binding{}, domain.ErrInvalid
	}
	holderID = parsed.String()
	holder, err := service.provider.GetCardholder(ctx, holderID)
	if err != nil {
		return Binding{}, domain.ErrProviderUnavailable
	}
	name, err := domain.NormalizeName(holder.LegalName())
	// 暂停校验 holder.Status != "READY"，实际发卡是否允许以 Airwallex 响应为准。
	if err != nil || holder.ID != holderID || holder.Type != "INDIVIDUAL" {
		return Binding{}, domain.ErrInvalidState
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	configured, err := queries.BindCompanyIssuingProfile(ctx, db.BindCompanyIssuingProfileParams{ProviderCardholderID: holderID, LegalName: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, domain.ErrInvalidState
	}
	if err != nil {
		return Binding{}, err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: admin.Admin.ID, Action: "company.issuing.configure", TargetID: admin.Admin.ID, RequestID: requestID, Changes: []byte(`{"provider":"airwallex","environment":"sandbox"}`)}); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Binding{}, err
	}
	return Binding{Bound: true, CardholderID: configured.ProviderCardholderID, LegalName: configured.LegalName}, nil
}

func checkPrincipal(ctx context.Context, queries *db.Queries, principal userauth.Principal) error {
	profile, err := queries.LockUser(ctx, principal.User.ID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if profile.Status != "ACTIVE" || credential.AuthVersion != principal.AuthVersion {
		return domain.ErrUnauthorized
	}
	if credential.MustChangePassword {
		return domain.ErrPasswordChangeRequired
	}
	if _, err := queries.LockActiveUserSession(ctx, db.LockActiveUserSessionParams{ID: principal.SessionID, UserID: profile.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrUnauthorized
		}
		return err
	}
	return nil
}
func (service *Service) Availability(ctx context.Context, principal userauth.Principal) (Availability, error) {
	_, err := service.queries.GetCompanyIssuingProfile(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Availability{}, err
	}
	occupied, countErr := service.queries.CountOutstandingCards(ctx, principal.User.ID)
	if countErr != nil {
		return Availability{}, countErr
	}
	return Availability{Enabled: service.options.Enabled, CompanyConfigured: err == nil, LimitCents: service.options.LimitCents, Currency: "USD", MaxCards: 1, OccupiedSlots: occupied}, nil
}
func (service *Service) Create(ctx context.Context, principal userauth.Principal, key, requestID string, input CreateInput) (Operation, error) {
	if err := service.enabled(); err != nil {
		return Operation{}, err
	}
	parsedKey, err := uuid.Parse(key)
	if err != nil || parsedKey == uuid.Nil {
		return Operation{}, domain.ErrInvalid
	}
	key = parsedKey.String()
	input.Nickname, err = domain.NormalizeName(input.Nickname)
	if err != nil {
		return Operation{}, err
	}
	serialized, _ := json.Marshal(input)
	digest := sha256.Sum256(serialized)
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	if err := checkPrincipal(ctx, queries, principal); err != nil {
		return Operation{}, err
	}
	existing, err := queries.GetCardOperationByKey(ctx, db.GetCardOperationByKeyParams{UserID: principal.User.ID, IdempotencyKey: key})
	if err == nil {
		if !bytes.Equal(existing.InputHash, digest[:]) {
			return Operation{}, domain.ErrIdempotencyConflict
		}
		return operationDTO(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, err
	}
	count, err := queries.CountOutstandingCards(ctx, principal.User.ID)
	if err != nil {
		return Operation{}, err
	}
	if count > 0 {
		return Operation{}, domain.ErrCardLimit
	}
	holder, err := queries.GetCompanyIssuingProfile(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, domain.ErrCardholderRequired
	}
	if err != nil {
		return Operation{}, err
	}
	cardID, operationID, providerRequestID := uuid.New(), uuid.New(), uuid.NewString()
	payload, err := json.Marshal(airwallex.BuildRequest(holder.ProviderCardholderID, holder.LegalName, service.options.ProgramPurpose, providerRequestID, input.Nickname, cardID.String(), operationID.String(), service.options.LimitCents))
	if err != nil {
		return Operation{}, err
	}
	if _, err := queries.CreateSandboxCard(ctx, db.CreateSandboxCardParams{ID: cardID, UserID: principal.User.ID, ProviderCardholderID: holder.ProviderCardholderID, NickName: input.Nickname, LimitCents: service.options.LimitCents}); err != nil {
		return Operation{}, err
	}
	operation, err := queries.CreateCardOperation(ctx, db.CreateCardOperationParams{ID: operationID, UserID: principal.User.ID, CardID: cardID, IdempotencyKey: key, InputHash: digest[:], ProviderRequestID: providerRequestID, RequestPayload: payload, AuthVersion: principal.AuthVersion})
	if err != nil {
		return Operation{}, err
	}
	if err := securityEvent(ctx, queries, principal.User.ID, "card.create.request", requestID); err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Operation{}, err
	}
	return operationDTO(operation), nil
}
func (service *Service) Operation(ctx context.Context, userID, id uuid.UUID) (Operation, error) {
	operation, err := service.queries.GetUserCardOperation(ctx, db.GetUserCardOperationParams{ID: id, UserID: userID})
	if err != nil {
		return Operation{}, domain.DatabaseError(err)
	}
	return operationDTO(operation), nil
}
func (service *Service) Retry(ctx context.Context, principal userauth.Principal, id uuid.UUID, requestID string) (Operation, error) {
	if err := service.enabled(); err != nil {
		return Operation{}, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	if err := checkPrincipal(ctx, queries, principal); err != nil {
		return Operation{}, err
	}
	previous, err := queries.GetUserCardOperation(ctx, db.GetUserCardOperationParams{ID: id, UserID: principal.User.ID})
	if err != nil {
		return Operation{}, domain.DatabaseError(err)
	}
	if previous.Status != "UNKNOWN" || previous.Attempts >= 5 {
		return Operation{}, domain.ErrInvalidState
	}
	operation, err := queries.RetryUserCardOperation(ctx, db.RetryUserCardOperationParams{ID: id, UserID: principal.User.ID, AuthVersion: principal.AuthVersion})
	if err != nil {
		return Operation{}, domain.ErrInvalidState
	}
	if err := securityEvent(ctx, queries, principal.User.ID, "card.create.retry", requestID); err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Operation{}, err
	}
	return operationDTO(operation), nil
}
func (service *Service) Get(ctx context.Context, userID, id uuid.UUID) (Card, error) {
	record, err := service.queries.GetUserCard(ctx, db.GetUserCardParams{ID: id, UserID: userID})
	if err != nil {
		return Card{}, domain.DatabaseError(err)
	}
	return service.cardDTO(ctx, record)
}
func (service *Service) List(ctx context.Context, userID uuid.UUID, page, size int32) (user.Page[Card], error) {
	if page < 1 || size < 1 || size > 100 || int64(page-1)*int64(size) > 2147483647 {
		return user.Page[Card]{}, domain.ErrInvalid
	}
	rows, err := service.queries.ListUserCards(ctx, db.ListUserCardsParams{UserID: userID, Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return user.Page[Card]{}, err
	}
	items := make([]Card, 0, len(rows))
	for _, record := range rows {
		item, err := service.cardDTO(ctx, record)
		if err != nil {
			return user.Page[Card]{}, err
		}
		items = append(items, item)
	}
	count, err := service.queries.CountUserCards(ctx, userID)
	return user.Page[Card]{Items: items, Total: count, Page: page, PageSize: size}, err
}
func (service *Service) cardDTO(ctx context.Context, record db.Card) (Card, error) {
	operationID, err := service.queries.GetCardOperationID(ctx, db.GetCardOperationIDParams{CardID: record.ID, UserID: record.UserID})
	return Card{ID: record.ID, Nickname: record.NickName, Status: record.Status, LastFour: record.LastFour, LimitCents: record.LimitCents, Currency: record.Currency, OperationID: operationID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, err
}
func operationDTO(record db.ProviderOperation) Operation {
	return Operation{ID: record.ID, CardID: record.CardID, Status: record.Status, ErrorCode: record.ErrorCode, Attempts: record.Attempts, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}
func securityEvent(ctx context.Context, queries *db.Queries, userID uuid.UUID, action, requestID string) error {
	return queries.CreateUserSecurityEvent(ctx, db.CreateUserSecurityEventParams{ID: uuid.New(), UserID: pgtype.UUID{Bytes: userID, Valid: true}, ActorType: "USER", ActorID: pgtype.UUID{Bytes: userID, Valid: true}, Action: action, RequestID: requestID, Changes: []byte(`{"environment":"sandbox"}`)})
}
