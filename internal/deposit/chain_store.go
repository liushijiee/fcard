package deposit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"fcard/internal/storage/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrScanConflict = errors.New("chain scan checkpoint or evidence conflict")
var scannerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type ScanCheckpoint struct {
	Height  int64
	BlockID string
}

type SolidBlockBatch struct {
	Version      int                  `json:",omitempty"`
	Observation  *TronScanObservation `json:",omitempty"`
	Block        TronBlock
	Transactions []TronTransactionEvidence
}

type TronChainStore struct {
	pool            *pgxpool.Pool
	config          TronVerifierConfig
	scanner         string
	evidenceVersion int32
}

func NewTronChainStore(pool *pgxpool.Pool, config TronVerifierConfig, scanner string) (*TronChainStore, error) {
	genesis, genesisErr := canonicalTronHash(config.GenesisID)
	contract, contractErr := canonicalTronAddress(config.Contract)
	if pool == nil || !scannerNamePattern.MatchString(scanner) || config.Environment != TronEnvironmentNile || config.Decimals != 6 || genesisErr != nil || contractErr != nil || !strings.HasPrefix(genesis, "0000000000000000") || genesis == strings.Repeat("0", 64) || contract == "41"+strings.Repeat("0", 40) {
		return nil, ErrInvalidChainEvidence
	}
	config.GenesisID, config.Contract = genesis, contract
	return &TronChainStore{pool: pool, config: config, scanner: scanner, evidenceVersion: 1}, nil
}

func NewTronScanStore(pool *pgxpool.Pool, config TronVerifierConfig, scanner string) (*TronChainStore, error) {
	store, err := NewTronChainStore(pool, config, scanner)
	if err != nil {
		return nil, err
	}
	store.evidenceVersion = 2
	return store, nil
}

func (store *TronChainStore) cursorKey() db.GetChainCursorParams {
	return db.GetChainCursorParams{ChainEnvironment: store.config.Environment, GenesisID: store.config.GenesisID, Network: "tron", Scanner: store.scanner}
}

func (store *TronChainStore) compatible(cursor db.ChainScanCursor) bool {
	return cursor.Contract == store.config.Contract && cursor.EvidenceVersion == store.evidenceVersion
}

func (store *TronChainStore) Initialize(ctx context.Context, start ScanCheckpoint) (db.ChainScanCursor, error) {
	hash, err := canonicalTronHash(start.BlockID)
	if start.Height < 0 || err != nil || start.Height == 0 && hash != store.config.GenesisID {
		return db.ChainScanCursor{}, ErrInvalidChainEvidence
	}
	decoded, _ := hex.DecodeString(hash)
	if binary.BigEndian.Uint64(decoded[:8]) != uint64(start.Height) {
		return db.ChainScanCursor{}, ErrInvalidChainEvidence
	}
	queries := db.New(store.pool)
	key := store.cursorKey()
	err = queries.InitializeChainCursor(ctx, db.InitializeChainCursorParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, Contract: store.config.Contract, EvidenceVersion: store.evidenceVersion, StartHeight: start.Height, StartBlockID: hash})
	if err != nil {
		return db.ChainScanCursor{}, err
	}
	cursor, err := store.Load(ctx)
	if err == nil && (cursor.StartHeight != start.Height || cursor.StartBlockID != hash) {
		return db.ChainScanCursor{}, ErrScanConflict
	}
	return cursor, err
}

func (store *TronChainStore) Load(ctx context.Context) (db.ChainScanCursor, error) {
	cursor, err := db.New(store.pool).GetChainCursor(ctx, store.cursorKey())
	if err == nil && !store.compatible(cursor) {
		return db.ChainScanCursor{}, ErrScanConflict
	}
	return cursor, err
}

func (store *TronChainStore) prepare(batch SolidBlockBatch) ([]byte, string, int64, error) {
	if store.evidenceVersion == 2 {
		return prepareTronScanBatch(store.config, batch)
	}
	if batch.Version != 0 || batch.Observation != nil {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	height, err := tronBlockHeight(batch.Block)
	if err != nil || height <= 0 || len(batch.Transactions) != len(batch.Block.Transactions) || batch.Block.ID != strings.ToLower(batch.Block.ID) || batch.Block.Header.RawData.ParentHash != strings.ToLower(batch.Block.Header.RawData.ParentHash) {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	timestamp := *batch.Block.Header.RawData.Timestamp
	for index, evidence := range batch.Transactions {
		if evidence.Disposition != "" || evidence.Environment != store.config.Environment || evidence.GenesisID != store.config.GenesisID || evidence.TxHash != batch.Block.Transactions[index].TxID || evidence.BlockNumber != height || evidence.BlockID != batch.Block.ID || !evidence.BlockTime.Equal(time.UnixMilli(timestamp)) || len(evidence.ReceiptJSON) > 4<<20 {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		hash, err := canonicalTronHash(evidence.TxHash)
		if err != nil || hash != evidence.TxHash {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		var receipt TronReceipt
		if json.Unmarshal(evidence.ReceiptJSON, &receipt) != nil || receipt.ID != hash || receipt.BlockNumber == nil || *receipt.BlockNumber != height || receipt.BlockTimeStamp == nil || *receipt.BlockTimeStamp != timestamp || receipt.Receipt.Result != "SUCCESS" || receipt.Result != "" && receipt.Result != "SUCESS" {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		encoded, err := json.Marshal(receipt)
		digest := sha256.Sum256(encoded)
		if err != nil || !bytes.Equal(encoded, evidence.ReceiptJSON) || hex.EncodeToString(digest[:]) != evidence.ReceiptHash {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		transfers, err := extractTronTransfers(receipt.Logs, store.config.Contract)
		if err != nil || !reflect.DeepEqual(transfers, evidence.Transfers) {
			return nil, "", 0, ErrInvalidChainEvidence
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > 16<<20 {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), height, nil
}

func (store *TronChainStore) CommitSolidBlock(ctx context.Context, batch SolidBlockBatch) (bool, error) {
	encoded, digest, height, err := store.prepare(batch)
	if err != nil {
		return false, err
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(cleanup)
	}()
	queries := db.New(store.pool).WithTx(transaction)
	key := store.cursorKey()
	cursor, err := queries.LockChainCursor(ctx, db.LockChainCursorParams(key))
	if err != nil {
		return false, err
	}
	if !store.compatible(cursor) {
		return false, ErrScanConflict
	}
	previous, err := queries.GetChainScanBlock(ctx, db.GetChainScanBlockParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, BlockHeight: height})
	if err == nil {
		matching := bytes.Equal(previous.EvidenceJson, encoded)
		if store.evidenceVersion == 2 {
			var persisted SolidBlockBatch
			matching = json.Unmarshal(previous.EvidenceJson, &persisted) == nil && sameScanCore(persisted, batch)
		}
		if previous.EvidenceHash != digest || !matching || cursor.BlockHeight < height {
			return false, ErrScanConflict
		}
		indexed, err := queries.ListChainScanTransactions(ctx, db.ListChainScanTransactionsParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, BlockHeight: height})
		if err != nil {
			return false, err
		}
		if len(indexed) != len(batch.Transactions) {
			return false, ErrScanConflict
		}
		byHash := make(map[string]db.ChainScanTransaction, len(indexed))
		for _, entry := range indexed {
			byHash[entry.TxHash] = entry
		}
		for _, evidence := range batch.Transactions {
			entry, found := byHash[evidence.TxHash]
			if !found || entry.BlockID != batch.Block.ID || !entry.BlockTime.Equal(evidence.BlockTime) || entry.ReceiptHash != evidence.ReceiptHash {
				return false, ErrScanConflict
			}
		}
		return false, transaction.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if cursor.BlockHeight != height-1 || cursor.BlockID != batch.Block.Header.RawData.ParentHash {
		return false, ErrScanConflict
	}
	blockTime := time.UnixMilli(*batch.Block.Header.RawData.Timestamp).UTC()
	err = queries.InsertChainScanBlock(ctx, db.InsertChainScanBlockParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, BlockHeight: height, BlockID: batch.Block.ID, ParentID: cursor.BlockID, BlockTime: blockTime, EvidenceHash: digest, EvidenceJson: encoded})
	if err != nil {
		return false, err
	}
	for _, evidence := range batch.Transactions {
		if err := queries.InsertChainScanTransaction(ctx, db.InsertChainScanTransactionParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, TxHash: evidence.TxHash, BlockHeight: height, BlockID: batch.Block.ID, BlockTime: blockTime, ReceiptHash: evidence.ReceiptHash}); err != nil {
			var databaseErr *pgconn.PgError
			if errors.As(err, &databaseErr) && databaseErr.Code == "23505" {
				return false, ErrScanConflict
			}
			return false, err
		}
		for _, transfer := range evidence.Transfers {
			_, err := queries.SaveChainTransfer(ctx, db.SaveChainTransferParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, TxHash: evidence.TxHash, EventIndex: int32(transfer.EventIndex), Contract: transfer.Contract, FromAddress: transfer.From, ToAddress: transfer.To, RawUnits: transfer.Amount, BlockHeight: height, BlockID: batch.Block.ID, BlockTime: blockTime, ReceiptHash: evidence.ReceiptHash})
			if errors.Is(err, pgx.ErrNoRows) {
				return false, ErrScanConflict
			}
			if err != nil {
				return false, err
			}
		}
	}
	affected, err := queries.AdvanceChainCursor(ctx, db.AdvanceChainCursorParams{ChainEnvironment: key.ChainEnvironment, GenesisID: key.GenesisID, Network: key.Network, Scanner: key.Scanner, NextHeight: height, NextID: batch.Block.ID, PreviousHeight: cursor.BlockHeight, PreviousID: cursor.BlockID})
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, ErrScanConflict
	}
	if err := transaction.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
