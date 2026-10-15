package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testStoreBatch(t testing.TB, receipt TronReceipt) SolidBlockBatch {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := extractTronTransfers(receipt.Logs, testTronContract())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	block := testTronBlock(*receipt.BlockNumber, receipt.ID)
	evidence := TronTransactionEvidence{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), TxHash: receipt.ID, BlockNumber: *receipt.BlockNumber, BlockID: block.ID, BlockTime: time.UnixMilli(*receipt.BlockTimeStamp).UTC(), ReceiptHash: hex.EncodeToString(digest[:]), ReceiptJSON: encoded, Transfers: transfers}
	return SolidBlockBatch{Block: block, Transactions: []TronTransactionEvidence{evidence}}
}

func TestChainStoreBatchValidation(t *testing.T) {
	store := &TronChainStore{config: TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), Contract: testTronContract(), Decimals: 6}}
	cases := []struct {
		name   string
		change func(*SolidBlockBatch)
	}{
		{"missing transaction evidence", func(batch *SolidBlockBatch) { batch.Transactions = nil }},
		{"extra transaction evidence", func(batch *SolidBlockBatch) { batch.Transactions = append(batch.Transactions, batch.Transactions[0]) }},
		{"wrong environment", func(batch *SolidBlockBatch) { batch.Transactions[0].Environment = "mainnet" }},
		{"wrong genesis", func(batch *SolidBlockBatch) { batch.Transactions[0].GenesisID = strings.Repeat("11", 32) }},
		{"wrong block height", func(batch *SolidBlockBatch) { batch.Transactions[0].BlockNumber++ }},
		{"wrong block ID", func(batch *SolidBlockBatch) { batch.Transactions[0].BlockID = testTronBlockID(99) }},
		{"wrong timestamp", func(batch *SolidBlockBatch) {
			batch.Transactions[0].BlockTime = batch.Transactions[0].BlockTime.Add(time.Second)
		}},
		{"wrong receipt hash", func(batch *SolidBlockBatch) { batch.Transactions[0].ReceiptHash = strings.Repeat("0", 64) }},
		{"edited receipt", func(batch *SolidBlockBatch) { batch.Transactions[0].ReceiptJSON = []byte(`{}`) }},
		{"edited amount", func(batch *SolidBlockBatch) { batch.Transactions[0].Transfers[0].Amount = "1" }},
		{"edited index", func(batch *SolidBlockBatch) { batch.Transactions[0].Transfers[0].EventIndex = -1 }},
		{"omitted event", func(batch *SolidBlockBatch) { batch.Transactions[0].Transfers = nil }},
		{"duplicate manifest transaction", func(batch *SolidBlockBatch) {
			batch.Block.Transactions = append(batch.Block.Transactions, batch.Block.Transactions[0])
		}},
		{"native execution result missing", func(batch *SolidBlockBatch) {
			receipt := testTronReceipt()
			receipt.Receipt.Result = ""
			receipt.Logs = nil
			*batch = testStoreBatch(t, receipt)
		}},
		{"failed execution", func(batch *SolidBlockBatch) {
			receipt := testTronReceipt()
			receipt.Result = "FAILED"
			receipt.Receipt.Result = "REVERT"
			*batch = testStoreBatch(t, receipt)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			batch := testStoreBatch(t, testTronReceipt())
			test.change(&batch)
			if _, _, _, err := store.prepare(batch); !errors.Is(err, ErrInvalidChainEvidence) {
				t.Fatalf("expected invalid evidence, got %v", err)
			}
		})
	}
	for _, batch := range []SolidBlockBatch{testStoreBatch(t, testTronReceipt()), {Block: testTronBlock(100)}} {
		if _, _, _, err := store.prepare(batch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewTronChainStore(nil, store.config, "scanner"); err == nil {
		t.Fatal("nil pool accepted")
	}
	if _, err := store.Initialize(context.Background(), ScanCheckpoint{Height: 100, BlockID: "invalid"}); !errors.Is(err, ErrInvalidChainEvidence) {
		t.Fatal("invalid checkpoint accepted")
	}
}
