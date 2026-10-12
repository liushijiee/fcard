package auth

import (
	"context"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
)

func (service *Service) DisableAdmin(ctx context.Context, email string) error {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return err
	}
	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer transaction.Rollback(ctx)
	queries := service.queries.WithTx(transaction)
	admin, err := queries.LockAdminByEmail(ctx, normalized)
	if err != nil {
		return domain.DatabaseError(err)
	}
	if err := queries.DisableAdmin(ctx, admin.ID); err != nil {
		return err
	}
	if err := queries.RevokeAdminSessions(ctx, admin.ID); err != nil {
		return err
	}
	if err := queries.CreateAudit(ctx, db.CreateAuditParams{ID: uuid.New(), AdminID: admin.ID, Action: "admin.disable", TargetID: admin.ID, RequestID: uuid.NewString(), Changes: []byte(`{"source":"local_cli","status":"DISABLED"}`)}); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}
