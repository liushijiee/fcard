package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CallbackWorker struct {
	leases *JobLeaseStore
}

type CallbackRunConfig struct {
	PollInterval  time.Duration
	StepTimeout   time.Duration
	FailureBudget int
}

func NewCallbackWorker(leases *JobLeaseStore) (*CallbackWorker, error) {
	if leases == nil || leases.pool == nil {
		return nil, domain.ErrInvalid
	}
	return &CallbackWorker{leases: leases}, nil
}

func persistedNotification(inbox db.DepositCallbackInbox) (epusdt.Notification, error) {
	notification := epusdt.Notification{Environment: inbox.ChainEnvironment, EventID: inbox.EventID, KeyVersion: inbox.KeyVersion, PID: inbox.MerchantPid, TradeID: inbox.TradeID, OrderID: inbox.GatewayOrderID, AmountUnits: uint64(inbox.RequestedUnits), ActualAmountUnits: uint64(inbox.ActualUnits), ReceiveAddress: inbox.ReceiveAddress, Token: inbox.Token, BlockTransactionID: inbox.TxHash, Status: 2, RawPayload: inbox.RawPayload}
	digest := sha256.Sum256(inbox.RawPayload)
	facts, err := notificationFactsHash(notification)
	if err != nil || facts != inbox.FactsHash || hex.EncodeToString(digest[:]) != inbox.PayloadHash {
		return epusdt.Notification{}, ErrInvalidChainEvidence
	}
	return notification, nil
}

func (worker *CallbackWorker) Process(ctx context.Context, ref LeaseRef) (db.DepositJob, error) {
	transaction, err := worker.leases.pool.Begin(ctx)
	if err != nil {
		return db.DepositJob{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(worker.leases.pool).WithTx(transaction)
	observed, err := queries.GetDepositJob(ctx, ref.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	if err != nil {
		return db.DepositJob{}, err
	}
	if observed.Kind != "VERIFY_CALLBACK" || !observed.InboxID.Valid {
		return db.DepositJob{}, domain.ErrInvalid
	}
	inbox, err := queries.GetDepositInbox(ctx, uuid.UUID(observed.InboxID.Bytes))
	if err != nil {
		return db.DepositJob{}, err
	}
	order, findErr := queries.FindDepositCallbackOrder(ctx, db.FindDepositCallbackOrderParams{ChainEnvironment: inbox.ChainEnvironment, MerchantPid: inbox.MerchantPid, GatewayOrderID: inbox.GatewayOrderID})
	if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
		return db.DepositJob{}, findErr
	}
	if findErr == nil {
		order, err = queries.LockDeposit(ctx, order.ID)
		if err != nil {
			return db.DepositJob{}, err
		}
	}
	current, err := lockJobLease(ctx, queries, ref)
	if err != nil {
		return db.DepositJob{}, err
	}
	if current.Kind != observed.Kind || current.InboxID != observed.InboxID {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	notification, evidenceErr := persistedNotification(inbox)
	state, reason, action, delay := "DONE", "", "CALLBACK_MATCHED", time.Duration(0)
	if evidenceErr != nil {
		state, reason, action = "MANUAL_REVIEW", "INBOX_EVIDENCE_INVALID", "CALLBACK_REVIEW"
	} else if errors.Is(findErr, pgx.ErrNoRows) || !order.TradeID.Valid && (order.Status == "CREATING" || order.Status == "CREATE_UNKNOWN") {
		state, reason, action, delay = "PENDING", "QUOTE_PENDING", "CALLBACK_DEFERRED", worker.leases.retryDelay(current.Attempts)
		if errors.Is(findErr, pgx.ErrNoRows) {
			reason = "ORDER_PENDING"
		}
		if current.Attempts >= worker.leases.config.MaxAttempts {
			state, action, delay = "MANUAL_REVIEW", "CALLBACK_REVIEW", 0
		}
	} else if !order.TradeID.Valid {
		state, reason, action = "MANUAL_REVIEW", "QUOTE_UNAVAILABLE", "CALLBACK_REVIEW"
	} else {
		var quote epusdt.Transaction
		if json.Unmarshal(order.QuoteJson, &quote) != nil {
			return db.DepositJob{}, ErrInvalidChainEvidence
		}
		quote, address, units, expires, err := normalizeDepositQuote(order, quote)
		if err != nil || address != order.ReceiveAddress.String || units != order.ActualUnits.Int64 || !expires.Equal(order.ExpiresAt.Time) {
			return db.DepositJob{}, ErrInvalidChainEvidence
		}
		matchErr := epusdt.MatchNotification(notification, epusdt.ExpectedPayment{Environment: order.ChainEnvironment, PID: order.MerchantPid, Network: order.Network, Transaction: quote})
		switch {
		case matchErr != nil:
			state, reason, action = "MANUAL_REVIEW", "QUOTE_MISMATCH", "CALLBACK_REVIEW"
		case order.Status == "REVIEW_REQUIRED" || order.Status == "FAILED":
			state, reason, action = "MANUAL_REVIEW", "ORDER_REVIEW_REQUIRED", "CALLBACK_REVIEW"
		case order.Status == "CONFIRMED":
			receipt, err := queries.GetDepositReceipt(ctx, order.ID)
			if err != nil {
				return db.DepositJob{}, err
			}
			if receipt.TxHash != notification.BlockTransactionID {
				state, reason, action = "MANUAL_REVIEW", "CONFIRMED_TX_MISMATCH", "CALLBACK_REVIEW"
			}
		default:
			payload, _ := json.Marshal(map[string]string{"tx_hash": notification.BlockTransactionID, "inbox_id": inbox.ID.String()})
			if err := queries.CreateDepositJob(ctx, db.CreateDepositJobParams{ID: uuid.New(), Kind: "VERIFY_CHAIN", DedupeKey: order.ID.String() + "/" + notification.BlockTransactionID, OrderID: optionalDepositUUID(order.ID), InboxID: optionalDepositUUID(inbox.ID), Payload: payload}); err != nil {
				return db.DepositJob{}, err
			}
		}
	}
	if err := depositEvent(ctx, queries, order.ID, inbox.ID, "SYSTEM", uuid.Nil, action, map[string]any{"job_id": ref.JobID.String(), "attempt": current.Attempts, "reason": reason}); err != nil {
		return db.DepositJob{}, err
	}
	finished, err := setLeasedJob(ctx, queries, ref, state, optionalDepositUUID(order.ID), reason, delay)
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositJob{}, err
	}
	return finished, nil
}

func (worker *CallbackWorker) Run(ctx context.Context, config CallbackRunConfig) error {
	return runVerificationWorker(ctx, worker.leases, config, worker.ScanOnce)
}

func runVerificationWorker(ctx context.Context, leases *JobLeaseStore, config CallbackRunConfig, scan func(context.Context) (bool, error)) error {
	if config.PollInterval < time.Second || config.PollInterval > time.Minute || config.StepTimeout < time.Second || config.StepTimeout > leases.config.Lease || config.FailureBudget < 1 || config.FailureBudget > 20 {
		return domain.ErrInvalid
	}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		step, cancel := context.WithTimeout(ctx, config.StepTimeout)
		processed, err := scan(step)
		cancel()
		if parentErr := ctx.Err(); parentErr != nil {
			return parentErr
		}
		if err != nil {
			failures++
			if failures >= config.FailureBudget {
				return err
			}
		} else {
			failures = 0
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (worker *CallbackWorker) ScanOnce(ctx context.Context) (bool, error) {
	if _, err := worker.leases.Recover(ctx, "VERIFY_CALLBACK"); err != nil {
		return false, err
	}
	job, err := worker.leases.Claim(ctx, "VERIFY_CALLBACK")
	if errors.Is(err, ErrNoDepositJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ref := LeaseRef{JobID: job.ID, Token: uuid.UUID(job.LeaseToken.Bytes)}
	if _, err := worker.Process(ctx, ref); err != nil {
		if !errors.Is(err, ErrDepositLeaseLost) && ctx.Err() == nil {
			if _, retryErr := worker.leases.Retry(ctx, ref, "CALLBACK_PROCESSING_ERROR"); retryErr != nil {
				return true, errors.Join(err, retryErr)
			}
		}
		return true, err
	}
	return true, nil
}
