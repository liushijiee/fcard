package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fcard/internal/auth"
	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"
	"fcard/internal/userauth"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	store     *OrderStore
	creator   *CreateWorker
	admission func() bool
}

type Order struct {
	ID             uuid.UUID  `json:"id"`
	UserID         uuid.UUID  `json:"user_id"`
	Status         string     `json:"status"`
	Environment    string     `json:"environment"`
	Network        string     `json:"network"`
	Contract       string     `json:"contract"`
	Amount         string     `json:"amount"`
	ActualAmount   string     `json:"actual_amount,omitempty"`
	ReceiveAddress string     `json:"receive_address,omitempty"`
	PaymentURL     string     `json:"payment_url,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ReviewReason   string     `json:"review_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type OrderPage struct {
	Items    []Order `json:"items"`
	Total    int64   `json:"total"`
	Page     int32   `json:"page"`
	PageSize int32   `json:"page_size"`
}

type RecoveryInput struct {
	ClaimToken        uuid.UUID          `json:"claim_token"`
	Reason            string             `json:"reason"`
	EvidenceReference string             `json:"evidence_reference"`
	Quote             epusdt.Transaction `json:"quote"`
}

func NewService(store *OrderStore, creator *CreateWorker) (*Service, error) {
	if store == nil || creator == nil || creator.store != store {
		return nil, domain.ErrInvalid
	}
	return &Service{store: store, creator: creator}, nil
}

func orderView(saved db.DepositOrder) Order {
	view := Order{ID: saved.ID, UserID: saved.UserID, Status: saved.Status, Environment: saved.ChainEnvironment, Network: saved.Network, Contract: saved.Contract, Amount: depositAmount(saved.RequestedUnits), ReviewReason: saved.ReviewReason, CreatedAt: saved.CreatedAt}
	if saved.TradeID.Valid {
		var quote epusdt.Transaction
		if json.Unmarshal(saved.QuoteJson, &quote) == nil {
			view.ReceiveAddress, view.PaymentURL = quote.ReceiveAddress, quote.PaymentURL
		}
		view.ActualAmount = depositAmount(saved.ActualUnits.Int64)
		view.ExpiresAt = &saved.ExpiresAt.Time
	}
	return view
}

func (service *Service) Enabled() bool {
	return service.store.config.Enabled && service.Ready()
}

func (service *Service) Ready() bool { return service.admission == nil || service.admission() }

func (service *Service) MaxOrderAmount() string {
	if service.store.config.MaxOrderUnits > 0 {
		return depositAmount(service.store.config.MaxOrderUnits)
	}
	return depositAmount(int64(maxPaymentUnits))
}

func (service *Service) Prepare(ctx context.Context, principal userauth.Principal, key, amount string) (Order, error) {
	if !service.Enabled() {
		return Order{}, ErrDepositsDisabled
	}
	order, err := service.store.prepare(ctx, principal, key, amount, 5)
	return orderView(order), err
}

func (service *Service) Get(ctx context.Context, principal userauth.Principal, id uuid.UUID) (Order, error) {
	order, err := service.store.Get(ctx, principal, id)
	return orderView(order), err
}

func depositPageValid(page, size int32) bool {
	return page > 0 && size > 0 && size <= 100 && int64(page-1)*int64(size) <= 2147483647
}

func (service *Service) List(ctx context.Context, principal userauth.Principal, page, size int32) (OrderPage, error) {
	if !depositPageValid(page, size) {
		return OrderPage{}, domain.ErrInvalid
	}
	transaction, err := service.store.pool.Begin(ctx)
	if err != nil {
		return OrderPage{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(service.store.pool).WithTx(transaction)
	if err := authorizeDeposit(ctx, queries, principal); err != nil {
		return OrderPage{}, err
	}
	rows, err := queries.ListUserDeposits(ctx, db.ListUserDepositsParams{UserID: principal.User.ID, Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return OrderPage{}, err
	}
	total, err := queries.CountUserDeposits(ctx, principal.User.ID)
	if err != nil {
		return OrderPage{}, err
	}
	result := OrderPage{Items: make([]Order, 0, len(rows)), Total: total, Page: page, PageSize: size}
	for _, row := range rows {
		result.Items = append(result.Items, orderView(row))
	}
	return result, transaction.Commit(ctx)
}

func authorizeDepositAdmin(ctx context.Context, queries *db.Queries, principal auth.Principal) error {
	if principal.Admin.ID == uuid.Nil || principal.SessionID == uuid.Nil {
		return domain.ErrUnauthorized
	}
	_, err := queries.LockDepositAdminSession(ctx, db.LockDepositAdminSessionParams{ID: principal.SessionID, ID_2: principal.Admin.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	}
	return err
}

func managementReason(reason, reference, requestID string) bool {
	parsed, err := uuid.Parse(requestID)
	return err == nil && parsed != uuid.Nil && len(strings.TrimSpace(reason)) >= 8 && len(reason) <= 512 && len(strings.TrimSpace(reference)) >= 8 && len(reference) <= 512 && !strings.ContainsAny(reason+reference, "\r\n\x00")
}

func (service *Service) RecoverQuote(ctx context.Context, principal auth.Principal, id uuid.UUID, input RecoveryInput, requestID string) (Order, error) {
	address, err := canonicalTronAddress(input.Quote.ReceiveAddress)
	if err != nil || !service.creator.wallets[address] || !managementReason(input.Reason, input.EvidenceReference, requestID) {
		return Order{}, domain.ErrInvalid
	}
	order, err := service.store.bindQuote(ctx, id, input.ClaimToken, input.Quote, func(ctx context.Context, queries *db.Queries, order db.DepositOrder) error {
		if err := authorizeDepositAdmin(ctx, queries, principal); err != nil {
			return err
		}
		if order.Status != "CREATING" && order.Status != "CREATE_UNKNOWN" && !order.TradeID.Valid {
			return domain.ErrInvalidState
		}
		config := service.store.config
		if order.ChainEnvironment != config.Chain.Environment || order.GenesisID != config.Chain.GenesisID || order.Contract != config.Chain.Contract || !service.creator.client.MatchesMerchant(order.ChainEnvironment, order.MerchantPid, order.GatewayOrigin) {
			return domain.ErrInvalid
		}
		encoded, _ := json.Marshal(input.Quote)
		digest := sha256.Sum256(encoded)
		return depositEvent(ctx, queries, order.ID, uuid.Nil, "ADMIN", principal.Admin.ID, "CREATE_QUOTE_RECOVERED", map[string]string{"reason": input.Reason, "evidence_reference": input.EvidenceReference, "request_id": requestID, "quote_hash": hex.EncodeToString(digest[:])})
	})
	return orderView(order), err
}

func (service *Service) Requeue(ctx context.Context, principal auth.Principal, jobID uuid.UUID, reason, requestID string) error {
	if !managementReason(reason, "verification review", requestID) {
		return domain.ErrInvalid
	}
	transaction, err := service.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(service.store.pool).WithTx(transaction)
	if err := authorizeDepositAdmin(ctx, queries, principal); err != nil {
		return err
	}
	job, err := queries.LockDepositJob(ctx, jobID)
	if err != nil {
		return domain.DatabaseError(err)
	}
	if job.Kind == "CREATE" || job.Status != "MANUAL_REVIEW" {
		return domain.ErrInvalidState
	}
	if _, err := queries.RequeueDepositVerification(ctx, jobID); err != nil {
		return err
	}
	if err := depositEvent(ctx, queries, uuid.UUID(job.OrderID.Bytes), uuid.UUID(job.InboxID.Bytes), "ADMIN", principal.Admin.ID, "VERIFICATION_REQUEUED", map[string]any{"job_id": jobID, "kind": job.Kind, "previous_attempts": job.Attempts, "reason": reason, "request_id": requestID}); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

type Management struct {
	Orders             OrderPage                         `json:"orders"`
	ReviewJobs         []db.ListDepositReviewJobsRow     `json:"review_jobs"`
	UnmatchedTransfers []db.ChainTransfer                `json:"unmatched_transfers"`
	Counts             db.GetDepositOperationalCountsRow `json:"counts"`
}

func (service *Service) Management(ctx context.Context, principal auth.Principal, page, size int32) (Management, error) {
	if !depositPageValid(page, size) {
		return Management{}, domain.ErrInvalid
	}
	transaction, err := service.store.pool.Begin(ctx)
	if err != nil {
		return Management{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(service.store.pool).WithTx(transaction)
	if err := authorizeDepositAdmin(ctx, queries, principal); err != nil {
		return Management{}, err
	}
	rows, err := queries.ListManagedDeposits(ctx, db.ListManagedDepositsParams{Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return Management{}, err
	}
	total, err := queries.CountManagedDeposits(ctx)
	if err != nil {
		return Management{}, err
	}
	result := Management{Orders: OrderPage{Items: make([]Order, 0, len(rows)), Total: total, Page: page, PageSize: size}}
	for _, row := range rows {
		result.Orders.Items = append(result.Orders.Items, orderView(row))
	}
	result.ReviewJobs, err = queries.ListDepositReviewJobs(ctx)
	if err != nil {
		return Management{}, err
	}
	chain := service.store.config.Chain
	result.UnmatchedTransfers, err = queries.ListDepositUnmatchedTransfers(ctx, db.ListDepositUnmatchedTransfersParams{ChainEnvironment: chain.Environment, GenesisID: chain.GenesisID, Contract: chain.Contract})
	if err != nil {
		return Management{}, err
	}
	result.Counts, err = queries.GetDepositOperationalCounts(ctx)
	if err != nil {
		return Management{}, err
	}
	return result, transaction.Commit(ctx)
}

type ManagedOrder struct {
	Order          Order                          `json:"order"`
	ClaimToken     *uuid.UUID                     `json:"claim_token,omitempty"`
	GatewayOrderID string                         `json:"gateway_order_id"`
	Events         []db.ListDepositOrderEventsRow `json:"events"`
}

func (service *Service) ManagedOrder(ctx context.Context, principal auth.Principal, id uuid.UUID) (ManagedOrder, error) {
	transaction, err := service.store.pool.Begin(ctx)
	if err != nil {
		return ManagedOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(service.store.pool).WithTx(transaction)
	if err := authorizeDepositAdmin(ctx, queries, principal); err != nil {
		return ManagedOrder{}, err
	}
	order, err := queries.GetManagedDeposit(ctx, id)
	if err != nil {
		return ManagedOrder{}, domain.DatabaseError(err)
	}
	result := ManagedOrder{Order: orderView(order), GatewayOrderID: order.GatewayOrderID}
	if order.CreateToken.Valid {
		token := uuid.UUID(order.CreateToken.Bytes)
		result.ClaimToken = &token
	}
	result.Events, err = queries.ListDepositOrderEvents(ctx, optionalDepositUUID(id))
	if err != nil {
		return ManagedOrder{}, err
	}
	return result, transaction.Commit(ctx)
}

func (service *Service) Expire(ctx context.Context) (int, error) {
	ids, err := db.New(service.store.pool).ListExpiringDeposits(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		changed := false
		transaction, err := service.store.pool.Begin(ctx)
		if err != nil {
			return count, err
		}
		queries := db.New(service.store.pool).WithTx(transaction)
		order, err := queries.LockDeposit(ctx, id)
		if err == nil && (order.Status == "WAITING" || order.Status == "VERIFYING") && order.ExpiresAt.Valid && !order.ExpiresAt.Time.After(time.Now()) {
			_, err = queries.SetDepositState(ctx, db.SetDepositStateParams{ID: id, Status: "EXPIRED"})
			if err == nil {
				err = depositEvent(ctx, queries, id, uuid.Nil, "SYSTEM", uuid.Nil, "ORDER_EXPIRED", map[string]string{"policy": "retain_chain_verification"})
			}
			if err == nil {
				changed = true
			}
		}
		if err == nil {
			err = transaction.Commit(ctx)
		}
		rollbackDeposit(transaction)
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}
