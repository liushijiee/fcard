package deposit

import (
	"context"
	"encoding/json"
	"errors"

	"fcard/internal/storage/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ChainCandidatePublisher struct {
	pool   *pgxpool.Pool
	config TronVerifierConfig
}

func NewChainCandidatePublisher(pool *pgxpool.Pool, config TronVerifierConfig) (*ChainCandidatePublisher, error) {
	validated, err := NewTronScanStore(pool, config, "candidate-validation")
	if err != nil {
		return nil, err
	}
	return &ChainCandidatePublisher{pool: pool, config: validated.config}, nil
}

func (publisher *ChainCandidatePublisher) publish(ctx context.Context, candidate db.ListDepositChainCandidatesRow) (bool, error) {
	transaction, err := publisher.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackDeposit(transaction)
	queries := db.New(publisher.pool).WithTx(transaction)
	order, err := queries.LockDeposit(ctx, candidate.OrderID)
	if err != nil {
		return false, err
	}
	key := order.ID.String() + "/" + candidate.TxHash
	_, err = queries.GetDepositChainJob(ctx, db.GetDepositChainJobParams{OrderID: optionalDepositUUID(order.ID), DedupeKey: key})
	if err == nil {
		return false, transaction.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	payload, _ := json.Marshal(chainJobPayload{TxHash: candidate.TxHash})
	if err := queries.CreateDepositJob(ctx, db.CreateDepositJobParams{ID: uuid.New(), Kind: "VERIFY_CHAIN", DedupeKey: key, OrderID: optionalDepositUUID(order.ID), Payload: payload}); err != nil {
		return false, err
	}
	if err := depositEvent(ctx, queries, order.ID, uuid.Nil, "SYSTEM", uuid.Nil, "CHAIN_CANDIDATE_QUEUED", map[string]string{"tx_hash": candidate.TxHash, "source": "scan"}); err != nil {
		return false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (publisher *ChainCandidatePublisher) ScanOnce(ctx context.Context) (int, error) {
	candidates, err := db.New(publisher.pool).ListDepositChainCandidates(ctx, db.ListDepositChainCandidatesParams{ChainEnvironment: publisher.config.Environment, GenesisID: publisher.config.GenesisID, Network: "tron", Contract: publisher.config.Contract})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, candidate := range candidates {
		queued, err := publisher.publish(ctx, candidate)
		if err != nil {
			return count, err
		}
		if queued {
			count++
		}
	}
	return count, nil
}
