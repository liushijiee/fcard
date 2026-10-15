package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
)

type CreateWorker struct {
	store     *OrderStore
	client    *epusdt.Client
	wallets   map[string]bool
	admission func() bool
}

func NewCreateWorker(store *OrderStore, client *epusdt.Client, receivingWallets []string) (*CreateWorker, error) {
	if store == nil || store.pool == nil || client == nil || len(receivingWallets) == 0 || len(receivingWallets) > 100 {
		return nil, domain.ErrInvalid
	}
	if !client.MatchesMerchant(store.config.Chain.Environment, store.config.PID, store.config.GatewayOrigin) {
		return nil, domain.ErrInvalid
	}
	wallets := make(map[string]bool, len(receivingWallets))
	for _, raw := range receivingWallets {
		address, err := canonicalTronAddress(raw)
		if err != nil || len(raw) != 34 || raw[0] != 'T' || address == "410000000000000000000000000000000000000000" {
			return nil, domain.ErrInvalid
		}
		wallets[address] = true
	}
	return &CreateWorker{store: store, client: client, wallets: wallets}, nil
}

func (worker *CreateWorker) validateClaim(ctx context.Context, queries *db.Queries, order db.DepositOrder) error {
	config := worker.store.config
	if order.ChainEnvironment != config.Chain.Environment || order.GenesisID != config.Chain.GenesisID || order.Contract != config.Chain.Contract || order.Network != "tron" || order.Currency != "USDT" || order.Decimals != 6 || order.MerchantPid != config.PID || order.NotifyUrl != config.NotifyURL || order.RedirectUrl != config.RedirectURL || order.GatewayOrigin != config.GatewayOrigin || order.MaxQuoteSeconds != int32(config.MaxQuoteLifetime/time.Second) || order.QuarantineSeconds != int32(config.Quarantine/time.Second) || !worker.client.MatchesMerchant(order.ChainEnvironment, order.MerchantPid, order.GatewayOrigin) {
		return domain.ErrInvalid
	}
	job, err := queries.LockDepositCreateJob(ctx, optionalDepositUUID(order.ID))
	if err != nil {
		return err
	}
	request := createOrderRequest(order)
	var saved epusdt.CreateRequest
	decoder := json.NewDecoder(bytes.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	if job.Status != "PENDING" || job.Attempts != 0 || job.DedupeKey != order.ID.String() || job.InboxID.Valid || decoder.Decode(&saved) != nil || decoder.Decode(new(any)) != io.EOF || saved != request {
		return domain.ErrInvalidState
	}
	count, err := queries.CountUnresolvedDepositCreates(ctx, db.CountUnresolvedDepositCreatesParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, Contract: order.Contract})
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrDepositAttributionPending
	}
	return nil
}

func createOrderRequest(order db.DepositOrder) epusdt.CreateRequest {
	return epusdt.CreateRequest{OrderID: order.GatewayOrderID, Amount: depositAmount(order.RequestedUnits), NotifyURL: order.NotifyUrl, RedirectURL: order.RedirectUrl, Name: "Fcard USDT deposit"}
}

func (worker *CreateWorker) Process(ctx context.Context, orderID uuid.UUID) (db.DepositOrder, error) {
	if worker.admission != nil && !worker.admission() {
		return db.DepositOrder{}, ErrDepositsDisabled
	}
	order, err := worker.store.startCreation(ctx, orderID, worker.validateClaim)
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrInvalidState) {
			return worker.rejectPending(ctx, orderID, err)
		}
		return order, err
	}
	quote, sendErr := worker.client.Create(ctx, createOrderRequest(order))
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token := uuid.UUID(order.CreateToken.Bytes)
	if sendErr != nil {
		var failure *epusdt.Failure
		ambiguous := !errors.As(sendErr, &failure) || failure.Ambiguous
		return worker.store.CreateFailed(persistCtx, order.ID, token, ambiguous)
	}
	address, addressErr := canonicalTronAddress(quote.ReceiveAddress)
	if addressErr != nil || !worker.wallets[address] {
		unknown, persistErr := worker.store.CreateFailed(persistCtx, order.ID, token, true)
		return unknown, errors.Join(epusdt.ErrQuoteMismatch, persistErr)
	}
	bound, bindErr := worker.store.BindQuote(persistCtx, order.ID, token, quote)
	if bindErr == nil {
		return bound, nil
	}
	_, freezeErr := worker.store.CreateFailed(persistCtx, order.ID, token, true)
	return bound, errors.Join(bindErr, freezeErr)
}

func (worker *CreateWorker) rejectPending(ctx context.Context, id uuid.UUID, cause error) (db.DepositOrder, error) {
	transaction, err := worker.store.pool.Begin(ctx)
	if err != nil {
		return db.DepositOrder{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(worker.store.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, id)
	if err != nil {
		return db.DepositOrder{}, err
	}
	if order.CreateToken.Valid || order.Status != "CREATING" {
		return order, cause
	}
	reason := "CREATE_PREFLIGHT_INVALID"
	if errors.Is(cause, domain.ErrUnauthorized) {
		reason = "CREATE_AUTHORIZATION_CHANGED"
	}
	affected, err := queries.RejectPendingDepositCreate(ctx, db.RejectPendingDepositCreateParams{OrderID: optionalDepositUUID(id), LastError: reason})
	if err != nil {
		return db.DepositOrder{}, err
	}
	if affected != 1 {
		return order, cause
	}
	order, err = queries.SetDepositState(ctx, db.SetDepositStateParams{ID: id, Status: "REVIEW_REQUIRED", ReviewReason: reason})
	if err != nil {
		return db.DepositOrder{}, err
	}
	if err := depositEvent(ctx, queries, id, uuid.Nil, "SYSTEM", uuid.Nil, "CREATE_PREFLIGHT_REVIEW", map[string]string{"reason": reason}); err != nil {
		return db.DepositOrder{}, err
	}
	return order, transaction.Commit(ctx)
}

func (worker *CreateWorker) ScanOnce(ctx context.Context) (int, error) {
	if !worker.store.config.Enabled {
		return 0, ErrDepositsDisabled
	}
	jobs, err := db.New(worker.store.pool).ListPendingDepositCreates(ctx)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, job := range jobs {
		_, err := worker.Process(ctx, uuid.UUID(job.OrderID.Bytes))
		if errors.Is(err, ErrDepositAttributionPending) {
			continue
		}
		if errors.Is(err, domain.ErrInvalidState) {
			order, readErr := db.New(worker.store.pool).GetManagedDeposit(ctx, uuid.UUID(job.OrderID.Bytes))
			if readErr != nil {
				return processed, readErr
			}
			if order.CreateToken.Valid || order.Status != "CREATING" {
				continue
			}
		}
		if err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (worker *CreateWorker) RecoverStaleClaims(ctx context.Context, age time.Duration) (int, error) {
	if age < time.Minute || age > 24*time.Hour || age%time.Second != 0 {
		return 0, domain.ErrInvalid
	}
	chain := worker.store.config.Chain
	orders, err := db.New(worker.store.pool).ListStaleDepositCreates(ctx, db.ListStaleDepositCreatesParams{ChainEnvironment: chain.Environment, GenesisID: chain.GenesisID, Contract: chain.Contract, StaleSeconds: int32(age / time.Second)})
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, order := range orders {
		_, err := worker.store.CreateFailed(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), true)
		if errors.Is(err, domain.ErrInvalidState) {
			continue
		}
		if err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}
