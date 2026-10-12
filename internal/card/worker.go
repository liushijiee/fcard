package card

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"fcard/internal/airwallex"
	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var lastDigits = regexp.MustCompile(`[0-9]{4}$`)

func (service *Service) Run(ctx context.Context, logger *slog.Logger) {
	if service.enabled() != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := service.ProcessOne(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("sandbox card task failed; retained for inspection")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *Service) ProcessOne(ctx context.Context) error {
	if err := service.enabled(); err != nil {
		return err
	}
	if err := service.queries.ExpireCardOperations(ctx); err != nil {
		return err
	}
	operation, err := service.queries.ClaimCardOperation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var request airwallex.CreateRequest
	if json.Unmarshal(operation.RequestPayload, &request) != nil {
		return service.finish(operation, airwallex.Card{}, "UNKNOWN", "invalid_saved_request")
	}
	if err := service.checkQueued(work, operation); err != nil {
		status := "UNKNOWN"
		if operation.Attempts == 1 && (errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrPasswordChangeRequired)) {
			status = "FAILED"
		}
		return service.finish(operation, airwallex.Card{}, status, "account_changed")
	}
	holder, err := service.provider.GetCardholder(work, request.CardholderID)
	if err != nil {
		return service.finish(operation, airwallex.Card{}, "UNKNOWN", providerFailureCode(err))
	}
	// 暂停校验 holder.Status != "READY"，避免后台任务再次拦截同一发卡身份。
	if holder.ID != request.CardholderID || holder.Type != "INDIVIDUAL" || holder.LegalName() != request.CreatedBy {
		status := "UNKNOWN"
		if operation.Attempts == 1 {
			status = "FAILED"
		}
		return service.finish(operation, airwallex.Card{}, status, "cardholder_not_ready")
	}
	if err := service.checkQueued(work, operation); err != nil {
		status := "UNKNOWN"
		if operation.Attempts == 1 && (errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrPasswordChangeRequired)) {
			status = "FAILED"
		}
		return service.finish(operation, airwallex.Card{}, status, "account_changed")
	}
	issued, err := service.provider.CreateCard(work, json.RawMessage(operation.RequestPayload))
	if err != nil {
		status := "UNKNOWN"
		if operation.Attempts == 1 && airwallex.Definitive(err) {
			status = "FAILED"
		}
		return service.finish(operation, airwallex.Card{}, status, providerFailureCode(err))
	}
	if !matches(issued, request) {
		return service.finish(operation, airwallex.Card{}, "UNKNOWN", "provider_response_mismatch")
	}
	return service.finish(operation, issued, "SUCCEEDED", "")
}

func providerFailureCode(err error) string {
	var failure *airwallex.Error
	if errors.As(err, &failure) {
		if failure.Status >= 300 && failure.Status <= 599 {
			return "provider_http_" + strconv.Itoa(failure.Status)
		}
		switch failure.Code {
		case "network_error", "invalid_response", "invalid_token_response", "authentication_failed":
			return failure.Code
		}
	}
	return "provider_unavailable"
}

func (service *Service) checkQueued(ctx context.Context, operation db.ProviderOperation) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	profile, err := queries.LockUser(ctx, operation.UserID)
	if err != nil {
		return err
	}
	credential, err := queries.LockUserCredential(ctx, profile.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if profile.Status != "ACTIVE" || credential.AuthVersion != operation.AuthVersion {
		return domain.ErrUnauthorized
	}
	if credential.MustChangePassword {
		return domain.ErrPasswordChangeRequired
	}
	current, err := queries.GetUserCardOperation(ctx, db.GetUserCardOperationParams{ID: operation.ID, UserID: operation.UserID})
	if err != nil {
		return err
	}
	if current.Status != "PROCESSING" || current.Attempts != operation.Attempts {
		return domain.ErrInvalidState
	}
	return nil
}

func matches(issued airwallex.Card, request airwallex.CreateRequest) bool {
	parsed, err := uuid.Parse(issued.ID)
	if err != nil || parsed == uuid.Nil || issued.CardholderID != request.CardholderID || issued.RequestID != request.RequestID {
		return false
	}
	for key, value := range request.Metadata {
		if issued.Metadata[key] != value {
			return false
		}
	}
	switch issued.Status {
	case "PENDING", "FAILED", "INACTIVE", "ACTIVE", "LOST", "STOLEN", "CLOSED", "BLOCKED", "EXPIRED", "UNKNOWN":
		return true
	default:
		return false
	}
}

func (service *Service) finish(operation db.ProviderOperation, issued airwallex.Card, status, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	if _, err := queries.LockUser(ctx, operation.UserID); err != nil {
		return err
	}
	changed, err := queries.FinishCardOperation(ctx, db.FinishCardOperationParams{ID: operation.ID, Status: status, ErrorCode: code, Attempts: operation.Attempts})
	if err != nil {
		return err
	}
	if changed != 1 {
		return domain.ErrInvalidState
	}
	if status == "SUCCEEDED" {
		if _, err := queries.UpdateIssuedCard(ctx, db.UpdateIssuedCardParams{ID: operation.CardID, UserID: operation.UserID, ProviderCardID: pgtype.Text{String: issued.ID, Valid: true}, Status: issued.Status, LastFour: lastDigits.FindString(issued.Number)}); err != nil {
			return err
		}
	} else if status == "FAILED" {
		if err := queries.FailSandboxCard(ctx, operation.CardID); err != nil {
			return err
		}
	}
	if err := releaseClosedSlot(ctx, queries, operation.UserID, operation.CardID, operation.ID.String()); err != nil {
		return err
	}
	if err := securityEvent(ctx, queries, operation.UserID, "card.create."+status, operation.ID.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) Sync(ctx context.Context, userID, id uuid.UUID) (Card, error) {
	if err := service.enabled(); err != nil {
		return Card{}, err
	}
	record, err := service.queries.GetUserCard(ctx, db.GetUserCardParams{ID: id, UserID: userID})
	if err != nil {
		return Card{}, domain.DatabaseError(err)
	}
	if !record.ProviderCardID.Valid {
		return Card{}, domain.ErrInvalidState
	}
	operationID, err := service.queries.GetCardOperationID(ctx, db.GetCardOperationIDParams{CardID: id, UserID: userID})
	if err != nil {
		return Card{}, err
	}
	operation, err := service.queries.GetUserCardOperation(ctx, db.GetUserCardOperationParams{ID: operationID, UserID: userID})
	if err != nil {
		return Card{}, err
	}
	issued, err := service.provider.GetCard(ctx, record.ProviderCardID.String)
	if err != nil {
		return Card{}, domain.ErrProviderUnavailable
	}
	var request airwallex.CreateRequest
	if json.Unmarshal(operation.RequestPayload, &request) != nil || issued.ID != record.ProviderCardID.String || !matches(issued, request) {
		return Card{}, domain.ErrProviderUnavailable
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Card{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	if _, err := queries.LockUser(ctx, userID); err != nil {
		return Card{}, err
	}
	record, err = queries.UpdateIssuedCard(ctx, db.UpdateIssuedCardParams{ID: id, UserID: userID, ProviderCardID: record.ProviderCardID, Status: issued.Status, LastFour: lastDigits.FindString(issued.Number)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, domain.ErrInvalidState
	}
	if err != nil {
		return Card{}, err
	}
	if err := releaseClosedSlot(ctx, queries, userID, id, operationID.String()); err != nil {
		return Card{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Card{}, err
	}
	return service.cardDTO(ctx, record)
}

func (service *Service) Reconcile(ctx context.Context, admin auth.Principal, userID, id uuid.UUID, providerID, requestID string) (Operation, error) {
	if err := service.enabled(); err != nil {
		return Operation{}, err
	}
	parsed, err := uuid.Parse(providerID)
	if err != nil || parsed == uuid.Nil {
		return Operation{}, domain.ErrInvalid
	}
	operation, err := service.queries.GetUserCardOperation(ctx, db.GetUserCardOperationParams{ID: id, UserID: userID})
	if err != nil {
		return Operation{}, domain.DatabaseError(err)
	}
	if operation.Status != "UNKNOWN" {
		return Operation{}, domain.ErrInvalidState
	}
	issued, err := service.provider.GetCard(ctx, parsed.String())
	if err != nil {
		return Operation{}, domain.ErrProviderUnavailable
	}
	var request airwallex.CreateRequest
	if json.Unmarshal(operation.RequestPayload, &request) != nil || issued.ID != parsed.String() || !matches(issued, request) {
		return Operation{}, domain.ErrInvalidState
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	queries := service.queries.WithTx(tx)
	if _, err := queries.LockUser(ctx, userID); err != nil {
		return Operation{}, err
	}
	changed, err := queries.ReconcileCardOperation(ctx, db.ReconcileCardOperationParams{ID: id, UserID: userID})
	if err != nil {
		return Operation{}, err
	}
	if changed != 1 {
		return Operation{}, domain.ErrInvalidState
	}
	if _, err := queries.UpdateIssuedCard(ctx, db.UpdateIssuedCardParams{ID: operation.CardID, UserID: userID, ProviderCardID: pgtype.Text{String: issued.ID, Valid: true}, Status: issued.Status, LastFour: lastDigits.FindString(issued.Number)}); err != nil {
		return Operation{}, err
	}
	if err := releaseClosedSlot(ctx, queries, userID, operation.CardID, id.String()); err != nil {
		return Operation{}, err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: admin.Admin.ID, Action: "card.create.reconcile", TargetID: operation.CardID, RequestID: requestID, Changes: []byte(`{"environment":"sandbox","verified":true}`)}); err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Operation{}, err
	}
	return service.Operation(ctx, userID, id)
}

func releaseClosedSlot(ctx context.Context, queries *db.Queries, userID, cardID uuid.UUID, requestID string) error {
	released, err := queries.ReleaseClosedCardSlot(ctx, db.ReleaseClosedCardSlotParams{ID: cardID, UserID: userID})
	if err != nil {
		return err
	}
	if released == 0 {
		return nil
	}
	return securityEvent(ctx, queries, userID, "card.slot.released", requestID)
}
