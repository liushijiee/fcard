package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ChainWorker struct{ leases *JobLeaseStore }

type chainJobPayload struct {
	TxHash  string `json:"tx_hash"`
	InboxID string `json:"inbox_id,omitempty"`
}

func NewChainWorker(leases *JobLeaseStore) (*ChainWorker, error) {
	if leases == nil || leases.pool == nil {
		return nil, domain.ErrInvalid
	}
	return &ChainWorker{leases: leases}, nil
}

func decodeChainJob(job db.DepositJob, order db.DepositOrder) (string, error) {
	var payload chainJobPayload
	decoder := json.NewDecoder(bytes.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil {
		return "", domain.ErrInvalid
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF {
		return "", domain.ErrInvalid
	}
	hash, err := canonicalTronHash(payload.TxHash)
	if err != nil || payload.TxHash != hash || job.DedupeKey != order.ID.String()+"/"+hash || !job.OrderID.Valid || uuid.UUID(job.OrderID.Bytes) != order.ID {
		return "", domain.ErrInvalid
	}
	if job.InboxID.Valid && payload.InboxID != uuid.UUID(job.InboxID.Bytes).String() || !job.InboxID.Valid && payload.InboxID != "" {
		return "", domain.ErrInvalid
	}
	return hash, nil
}

func savedTransactionEvidence(ctx context.Context, queries *db.Queries, order db.DepositOrder, hash string) (TronTransactionEvidence, error) {
	proof, err := queries.GetDepositTransactionProof(ctx, db.GetDepositTransactionProofParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, TxHash: hash, Contract: order.Contract})
	if errors.Is(err, pgx.ErrNoRows) {
		return TronTransactionEvidence{}, ErrDepositProofPending
	}
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	var batch SolidBlockBatch
	if proof.Variants != 1 || proof.JournalBlockID != proof.BlockID || !proof.JournalBlockTime.Equal(proof.BlockTime) || json.Unmarshal(proof.EvidenceJson, &batch) != nil {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	validator := &TronChainStore{config: TronVerifierConfig{Environment: order.ChainEnvironment, GenesisID: order.GenesisID, Contract: order.Contract, Decimals: int(order.Decimals)}, evidenceVersion: proof.EvidenceVersion}
	_, digest, height, err := validator.prepare(batch)
	if err != nil || digest != proof.EvidenceHash || height != proof.BlockHeight || batch.Block.ID != proof.BlockID {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	for _, evidence := range batch.Transactions {
		if evidence.TxHash != hash {
			continue
		}
		if evidence.ReceiptHash != proof.ReceiptHash || !evidence.BlockTime.Equal(proof.BlockTime) {
			return TronTransactionEvidence{}, ErrInvalidChainEvidence
		}
		rows, err := queries.ListDepositTransactionTransfers(ctx, db.ListDepositTransactionTransfersParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, TxHash: hash, Contract: order.Contract})
		if err != nil {
			return TronTransactionEvidence{}, err
		}
		if len(rows) != len(evidence.Transfers) {
			return TronTransactionEvidence{}, ErrInvalidChainEvidence
		}
		for index, event := range evidence.Transfers {
			row := rows[index]
			if row.EventIndex != int32(event.EventIndex) || row.Contract != event.Contract || row.FromAddress != event.From || row.ToAddress != event.To || row.RawUnits != event.Amount || row.BlockHeight != height || row.BlockID != proof.BlockID || !row.BlockTime.Equal(evidence.BlockTime) || row.ReceiptHash != evidence.ReceiptHash {
				return TronTransactionEvidence{}, ErrInvalidChainEvidence
			}
		}
		return evidence, nil
	}
	return TronTransactionEvidence{}, ErrInvalidChainEvidence
}

func (worker *ChainWorker) Process(ctx context.Context, ref LeaseRef) (db.DepositJob, error) {
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
	if observed.Kind != "VERIFY_CHAIN" || !observed.OrderID.Valid {
		return db.DepositJob{}, domain.ErrInvalid
	}
	order, err := queries.LockDeposit(ctx, uuid.UUID(observed.OrderID.Bytes))
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := lockCreationScope(ctx, queries, order); err != nil {
		return db.DepositJob{}, err
	}
	job, err := lockJobLease(ctx, queries, ref)
	if err != nil {
		return db.DepositJob{}, err
	}
	if job.Kind != observed.Kind || job.OrderID != observed.OrderID {
		return db.DepositJob{}, ErrDepositLeaseLost
	}
	hash, payloadErr := decodeChainJob(job, order)
	state, reason, action, delay := "DONE", "", "CHAIN_VERIFIED", time.Duration(0)
	var receipt db.DepositReceipt
	if payloadErr != nil {
		state, reason, action = "MANUAL_REVIEW", "CHAIN_PAYLOAD_INVALID", "CHAIN_REVIEW"
	} else if !order.ActualUnits.Valid || order.Status == "REVIEW_REQUIRED" || order.Status == "FAILED" {
		state, reason, action = "MANUAL_REVIEW", "ORDER_REVIEW_REQUIRED", "CHAIN_REVIEW"
	} else {
		evidence, evidenceErr := savedTransactionEvidence(ctx, queries, order, hash)
		var resultErr error
		switch {
		case errors.Is(evidenceErr, ErrDepositProofPending):
			resultErr = evidenceErr
		case errors.Is(evidenceErr, ErrInvalidChainEvidence):
			state, reason, action = "MANUAL_REVIEW", "CHAIN_EVIDENCE_INVALID", "CHAIN_REVIEW"
		case evidenceErr != nil:
			return db.DepositJob{}, evidenceErr
		default:
			matched, matchErr := MatchTronPayment(evidence, TronPaymentExpectation{Environment: order.ChainEnvironment, GenesisID: order.GenesisID, TxHash: hash, Contract: order.Contract, Recipient: order.ReceiveAddress.String, Units: uint64(order.ActualUnits.Int64), NotBefore: order.CreateStartedAt.Time, ExpiresAt: order.ExpiresAt.Time})
			eventIndex := int32(-1)
			if matchErr == nil || errors.Is(matchErr, ErrPaymentWindow) {
				eventIndex = int32(matched.EventIndex)
			}
			receipt, resultErr = confirmDeposit(ctx, queries, order, TransferRef{TxHash: hash, EventIndex: eventIndex}, &evidence)
		}
		switch {
		case errors.Is(resultErr, ErrDepositProofPending), errors.Is(resultErr, ErrDepositAttributionPending):
			state, reason, action, delay = "PENDING", "CHAIN_PROOF_PENDING", "CHAIN_DEFERRED", worker.leases.retryDelay(job.Attempts)
			if errors.Is(resultErr, ErrDepositAttributionPending) {
				reason = "ATTRIBUTION_PENDING"
			}
			if job.Attempts >= worker.leases.config.MaxAttempts {
				state, action, delay = "MANUAL_REVIEW", "CHAIN_REVIEW", 0
			}
		case errors.Is(resultErr, ErrDepositReview):
			state, reason, action = "MANUAL_REVIEW", "PAYMENT_REVIEW_REQUIRED", "CHAIN_REVIEW"
		case errors.Is(resultErr, domain.ErrConflict):
			state, reason, action = "MANUAL_REVIEW", "ORDER_ALREADY_CONFIRMED", "CHAIN_REVIEW"
		case errors.Is(resultErr, domain.ErrInvalidState):
			state, reason, action = "MANUAL_REVIEW", "ORDER_STATE_INVALID", "CHAIN_REVIEW"
		case resultErr != nil:
			return db.DepositJob{}, resultErr
		}
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, action, map[string]any{"job_id": job.ID.String(), "tx_hash": hash, "attempt": job.Attempts, "reason": reason, "receipt_id": receipt.ID.String()}); err != nil {
		return db.DepositJob{}, err
	}
	finished, err := setLeasedJob(ctx, queries, ref, state, job.OrderID, reason, delay)
	if err != nil {
		return db.DepositJob{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositJob{}, err
	}
	return finished, nil
}

func (worker *ChainWorker) ScanOnce(ctx context.Context) (bool, error) {
	if _, err := worker.leases.Recover(ctx, "VERIFY_CHAIN"); err != nil {
		return false, err
	}
	job, err := worker.leases.Claim(ctx, "VERIFY_CHAIN")
	if errors.Is(err, ErrNoDepositJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ref := LeaseRef{JobID: job.ID, Token: uuid.UUID(job.LeaseToken.Bytes)}
	if _, err := worker.Process(ctx, ref); err != nil {
		if !errors.Is(err, ErrDepositLeaseLost) && ctx.Err() == nil {
			if _, retryErr := worker.leases.Retry(ctx, ref, "CHAIN_PROCESSING_ERROR"); retryErr != nil {
				return true, errors.Join(err, retryErr)
			}
		}
		return true, err
	}
	return true, nil
}

func (worker *ChainWorker) Run(ctx context.Context, config CallbackRunConfig) error {
	return runVerificationWorker(ctx, worker.leases, config, worker.ScanOnce)
}
