package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

const brokerageTx = "4840bbfb5a498d54caf5a6d023c9f67b7b76d804c7560cf1c1270b342cbd1f3a"

func recordedBrokerageSource(t *testing.T) *testScanSource {
	t.Helper()
	encoded, err := os.ReadFile("testdata/nile-block-71809560.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Block    TronBlock     `json:"block"`
		Receipts []TronReceipt `json:"receipts"`
	}
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Block.ID != "000000000447ba18c98d78ac93fb6f78ea4b52a178096f5326aeb684f88fa0bd" || len(fixture.Block.Transactions) != 4 || len(fixture.Receipts) != 4 {
		t.Fatal("recorded public block changed")
	}
	// Only the target block and receipts are recorded public RPC data. The
	// preceding anchor's header is synthetic for the reader continuity check.
	anchor := fixture.Block
	anchor.ID = fixture.Block.Header.RawData.ParentHash
	anchor.Header.RawData.Number = testInt64(71809559)
	anchor.Header.RawData.ParentHash = testTronBlockID(71809558)
	anchor.Header.RawData.Timestamp = testInt64(*fixture.Block.Header.RawData.Timestamp - 3000)
	anchor.Transactions = nil
	source := &testScanSource{genesis: testTronGenesis(), head: 71809560,
		blocks: map[int64]TronBlock{71809559: anchor, 71809560: fixture.Block}, receipts: map[string]TronReceipt{}}
	for _, receipt := range fixture.Receipts {
		source.receipts[receipt.ID] = receipt
	}
	return source
}

func TestRecordedBrokerageBlockAdvancesScanner(t *testing.T) {
	source := recordedBrokerageSource(t)
	repository := newTestScanRepository()
	repository.cursor.BlockHeight = 71809559
	repository.cursor.BlockID = source.blocks[71809559].ID
	committed, err := testScanner(t, source, repository).ScanOnce(context.Background())
	if err != nil || !committed || repository.cursor.BlockHeight != 71809560 || len(repository.batches) != 1 {
		t.Fatalf("recorded block did not advance scanner: %v", err)
	}
	batch := repository.batches[0]
	if len(batch.Transactions) != 4 {
		t.Fatal("scanner dropped transactions")
	}
	if _, _, _, err := prepareTronScanBatch(testScanConfig(), batch); err != nil {
		t.Fatal("recorded batch failed storage validation", err)
	}
	for _, evidence := range batch.Transactions {
		if evidence.Disposition != tronNativeSuccess || len(evidence.Transfers) != 0 {
			t.Fatal("native transaction became payment evidence")
		}
	}
}

func TestBrokerageRejectsUntrustedEvidenceWithoutAdvancing(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*TronBlockTransaction, *TronReceipt)
	}{
		{"missing block result", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.Ret = nil }},
		{"failed block result", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.Ret[0].ContractRet = "REVERT" }},
		{"failed transaction result", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.Ret[0].Ret = "FAILED" }},
		{"failed receipt", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Result = "FAILED" }},
		{"TVM failure", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Receipt.Result = "REVERT" }},
		{"unexpected token log", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Logs = []TronLog{testTronLog()} }},
		{"wrong receipt height", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.BlockNumber = testInt64(71809561) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := recordedBrokerageSource(t)
			found := false
			for index := range source.blocks[71809560].Transactions {
				tx := &source.blocks[71809560].Transactions[index]
				if tx.TxID == brokerageTx {
					found = true
					receipt := source.receipts[brokerageTx]
					scenario.mutate(tx, &receipt)
					source.receipts[brokerageTx] = receipt
				}
			}
			if !found {
				t.Fatal("recorded brokerage transaction missing")
			}
			repository := newTestScanRepository()
			repository.cursor.BlockHeight = 71809559
			repository.cursor.BlockID = source.blocks[71809559].ID
			committed, err := testScanner(t, source, repository).ScanOnce(context.Background())
			if !errors.Is(err, ErrInvalidChainEvidence) || committed || repository.calls != 0 || repository.cursor.BlockHeight != 71809559 {
				t.Fatalf("untrusted brokerage evidence advanced scanner: %v", err)
			}
		})
	}
}
