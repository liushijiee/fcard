package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InboxRecorder struct {
	pool     *pgxpool.Pool
	verifier *epusdt.CallbackVerifier
}

func notificationFactsHash(notification epusdt.Notification) (string, error) {
	hash, err := canonicalTronHash(notification.BlockTransactionID)
	if err != nil {
		return "", err
	}
	notification.BlockTransactionID = hash
	notification.RawPayload, notification.KeyVersion = nil, ""
	encoded, err := json.Marshal(notification)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func NewInboxRecorder(pool *pgxpool.Pool, verifier *epusdt.CallbackVerifier) (*InboxRecorder, error) {
	if pool == nil || verifier == nil {
		return nil, domain.ErrInvalid
	}
	return &InboxRecorder{pool: pool, verifier: verifier}, nil
}

func (recorder *InboxRecorder) Record(ctx context.Context, notification epusdt.Notification) error {
	verified, err := recorder.verifier.Verify(append([]byte(nil), notification.RawPayload...))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(verified, notification) {
		return epusdt.ErrInvalidNotification
	}
	factsHash, err := notificationFactsHash(verified)
	if err != nil {
		return err
	}
	payloadHash := sha256.Sum256(verified.RawPayload)
	txHash, err := canonicalTronHash(verified.BlockTransactionID)
	if err != nil {
		return err
	}
	transaction, err := recorder.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(recorder.pool).WithTx(transaction)
	inbox, err := queries.SaveDepositInbox(ctx, db.SaveDepositInboxParams{
		ID: uuid.New(), ChainEnvironment: verified.Environment, EventID: verified.EventID,
		FactsHash: factsHash, PayloadHash: hex.EncodeToString(payloadHash[:]),
		KeyVersion: verified.KeyVersion, MerchantPid: verified.PID, GatewayOrderID: verified.OrderID,
		TradeID: verified.TradeID, RequestedUnits: int64(verified.AmountUnits), ActualUnits: int64(verified.ActualAmountUnits),
		ReceiveAddress: verified.ReceiveAddress, Token: verified.Token, TxHash: txHash, RawPayload: verified.RawPayload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"inbox_id": inbox.ID.String()})
	if err := queries.CreateDepositJob(ctx, db.CreateDepositJobParams{ID: uuid.New(), Kind: "VERIFY_CALLBACK", DedupeKey: verified.Environment + "/" + verified.EventID, InboxID: optionalDepositUUID(inbox.ID), Payload: payload}); err != nil {
		return err
	}
	if inbox.Deliveries == 1 {
		if err := depositEvent(ctx, queries, uuid.Nil, inbox.ID, "SYSTEM", uuid.Nil, "CALLBACK_RECEIVED", map[string]string{"event_id": verified.EventID, "payload_hash": inbox.PayloadHash}); err != nil {
			return err
		}
	}
	return transaction.Commit(ctx)
}
