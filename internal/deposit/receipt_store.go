package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"fcard/internal/domain"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrDepositProofPending = errors.New("deposit chain evidence is not persisted yet")
var ErrDepositAttributionPending = errors.New("unresolved deposit creation prevents safe attribution")
var ErrDepositReview = errors.New("deposit requires manual review")

type TransferRef struct {
	TxHash     string
	EventIndex int32
}

func savedDepositEvidence(ctx context.Context, queries *db.Queries, order db.DepositOrder, ref TransferRef) (TronTransactionEvidence, error) {
	transfer, err := queries.GetDepositTransfer(ctx, db.GetDepositTransferParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, TxHash: ref.TxHash, EventIndex: ref.EventIndex})
	if errors.Is(err, pgx.ErrNoRows) {
		return TronTransactionEvidence{}, ErrDepositProofPending
	}
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	proof, err := queries.GetDepositBlockProof(ctx, db.GetDepositBlockProofParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, BlockHeight: transfer.BlockHeight, BlockID: transfer.BlockID, Contract: order.Contract})
	if errors.Is(err, pgx.ErrNoRows) {
		return TronTransactionEvidence{}, ErrDepositProofPending
	}
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	var batch SolidBlockBatch
	if json.Unmarshal(proof.EvidenceJson, &batch) != nil {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	validator := &TronChainStore{config: TronVerifierConfig{Environment: order.ChainEnvironment, GenesisID: order.GenesisID, Contract: order.Contract, Decimals: int(order.Decimals)}, evidenceVersion: proof.EvidenceVersion}
	_, digest, height, err := validator.prepare(batch)
	if err != nil || digest != proof.EvidenceHash || height != transfer.BlockHeight || batch.Block.ID != transfer.BlockID {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	for _, evidence := range batch.Transactions {
		if evidence.TxHash != ref.TxHash {
			continue
		}
		if evidence.ReceiptHash != transfer.ReceiptHash || !evidence.BlockTime.Equal(transfer.BlockTime) {
			return TronTransactionEvidence{}, ErrInvalidChainEvidence
		}
		for _, event := range evidence.Transfers {
			if event.EventIndex == int(ref.EventIndex) && event.Contract == transfer.Contract && event.From == transfer.FromAddress && event.To == transfer.ToAddress && event.Amount == transfer.RawUnits {
				return evidence, nil
			}
		}
	}
	return TronTransactionEvidence{}, ErrInvalidChainEvidence
}

func (store *OrderStore) Confirm(ctx context.Context, orderID uuid.UUID, ref TransferRef) (db.DepositReceipt, error) {
	hash, err := canonicalTronHash(ref.TxHash)
	if err != nil || ref.EventIndex < 0 {
		return db.DepositReceipt{}, domain.ErrInvalid
	}
	ref.TxHash = hash
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return db.DepositReceipt{}, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(store.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, orderID)
	if err != nil {
		return db.DepositReceipt{}, domain.DatabaseError(err)
	}
	receipt, resultErr := confirmDeposit(ctx, queries, order, ref, nil)
	if resultErr != nil && !errors.Is(resultErr, ErrDepositReview) {
		return db.DepositReceipt{}, resultErr
	}
	if resultErr == nil {
		if err := queries.FinishDepositChainJobs(ctx, db.FinishDepositChainJobsParams{OrderID: optionalDepositUUID(order.ID), DedupeKey: order.ID.String() + "/" + hash}); err != nil {
			return db.DepositReceipt{}, err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return db.DepositReceipt{}, err
	}
	return receipt, resultErr
}

func confirmDeposit(ctx context.Context, queries *db.Queries, order db.DepositOrder, ref TransferRef, provided *TronTransactionEvidence) (db.DepositReceipt, error) {
	hash := ref.TxHash
	previous, err := queries.GetDepositReceipt(ctx, order.ID)
	if err == nil {
		if previous.TxHash != hash || previous.EventIndex != ref.EventIndex {
			return db.DepositReceipt{}, domain.ErrConflict
		}
		return previous, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.DepositReceipt{}, err
	}
	if order.Status == "REVIEW_REQUIRED" {
		return db.DepositReceipt{}, ErrDepositReview
	}
	if !order.ActualUnits.Valid || order.Status != "WAITING" && order.Status != "VERIFYING" && order.Status != "EXPIRED" {
		return db.DepositReceipt{}, domain.ErrInvalidState
	}
	if err := lockCreationScope(ctx, queries, order); err != nil {
		return db.DepositReceipt{}, err
	}
	unresolved, err := queries.CountUnresolvedDepositCreates(ctx, db.CountUnresolvedDepositCreatesParams{ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, Contract: order.Contract})
	if err != nil {
		return db.DepositReceipt{}, err
	}
	if unresolved != 0 {
		return db.DepositReceipt{}, ErrDepositAttributionPending
	}
	var evidence TronTransactionEvidence
	if provided != nil {
		evidence = *provided
	} else {
		evidence, err = savedDepositEvidence(ctx, queries, order, ref)
		if err != nil {
			return db.DepositReceipt{}, err
		}
	}
	matched, matchErr := MatchTronPayment(evidence, TronPaymentExpectation{Environment: order.ChainEnvironment, GenesisID: order.GenesisID, TxHash: hash, Contract: order.Contract, Recipient: order.ReceiveAddress.String, Units: uint64(order.ActualUnits.Int64), NotBefore: order.CreateStartedAt.Time, ExpiresAt: order.ExpiresAt.Time})
	reason := ""
	if matchErr != nil || matched.EventIndex != int(ref.EventIndex) {
		reason = "TRANSFER_MISMATCH"
		if errors.Is(matchErr, ErrPaymentWindow) {
			reason = "PAYMENT_WINDOW"
		}
		if errors.Is(matchErr, ErrAmbiguousTransfer) {
			reason = "AMBIGUOUS_TRANSFER"
		}
	}
	overlaps, err := lockReservation(ctx, queries, order)
	if err != nil {
		return db.DepositReceipt{}, err
	}
	if overlaps != 0 {
		reason = "RESERVATION_OVERLAP"
	}
	owner, err := queries.LockUser(ctx, order.UserID)
	if err != nil {
		return db.DepositReceipt{}, err
	}
	if owner.Status != "ACTIVE" {
		reason = "USER_DISABLED"
	}
	if reason != "" {
		if _, err := queries.SetDepositState(ctx, db.SetDepositStateParams{ID: order.ID, Status: "REVIEW_REQUIRED", ReviewReason: reason}); err != nil {
			return db.DepositReceipt{}, err
		}
		if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, "CONFIRMATION_REVIEW", map[string]any{"tx_hash": hash, "event_index": ref.EventIndex, "reason": reason}); err != nil {
			return db.DepositReceipt{}, err
		}
		return db.DepositReceipt{}, ErrDepositReview
	}
	if matched.Amount != strconv.FormatInt(order.ActualUnits.Int64, 10) {
		return db.DepositReceipt{}, ErrInvalidChainEvidence
	}
	receipt, err := queries.CreateDepositReceipt(ctx, db.CreateDepositReceiptParams{ID: uuid.New(), OrderID: order.ID, ChainEnvironment: order.ChainEnvironment, GenesisID: order.GenesisID, Network: order.Network, TxHash: hash, EventIndex: ref.EventIndex, ConfirmedUnits: order.ActualUnits.Int64, BlockTime: evidence.BlockTime, ReceiptHash: evidence.ReceiptHash})
	if err != nil {
		return db.DepositReceipt{}, domain.DatabaseError(err)
	}
	if _, err := queries.SetDepositState(ctx, db.SetDepositStateParams{ID: order.ID, Status: "CONFIRMED"}); err != nil {
		return db.DepositReceipt{}, err
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, "PAYMENT_CONFIRMED", map[string]any{"receipt_id": receipt.ID.String(), "tx_hash": hash, "event_index": ref.EventIndex, "units": receipt.ConfirmedUnits}); err != nil {
		return db.DepositReceipt{}, err
	}
	return receipt, nil
}
