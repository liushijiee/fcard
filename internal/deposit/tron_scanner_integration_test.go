package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/storage"
	"fcard/migrations"
)

type lostCommitReplyStore struct {
	*TronChainStore
	loseReply bool
}

func (store *lostCommitReplyStore) CommitSolidBlock(ctx context.Context, batch SolidBlockBatch) (bool, error) {
	committed, err := store.TronChainStore.CommitSolidBlock(ctx, batch)
	if err == nil && store.loseReply {
		store.loseReply = false
		return false, ErrChainUnavailable
	}
	return committed, err
}

func TestTronScannerPersistenceIntegration(t *testing.T) {
	pool := chainIntegrationDatabase(t)
	ctx := context.Background()
	store, err := NewTronScanStore(pool, testScanConfig(), "solid-scan-v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Initialize(ctx, ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); err != nil {
		t.Fatal(err)
	}
	source := testMixedScanSource()
	wrapped := &lostCommitReplyStore{TronChainStore: store, loseReply: true}
	scanner := testScanner(t, source, wrapped)
	if _, err := scanner.ScanOnce(ctx); !errors.Is(err, ErrChainUnavailable) {
		t.Fatal("lost commit reply not surfaced", err)
	}
	cursor, err := store.Load(ctx)
	if err != nil || cursor.BlockHeight != 100 {
		t.Fatal("successful commit was lost")
	}
	calls := len(source.calls)
	if committed, err := scanner.ScanOnce(ctx); err != nil || committed || len(source.calls) != calls {
		t.Fatal("unknown commit replay fetched or duplicated block", err)
	}
	blocks, events := chainCounts(t, pool)
	if blocks != 1 || events != 2 {
		t.Fatal("native/failed transaction incorrectly generated payment event")
	}
	var persistedBytes []byte
	if err := pool.QueryRow(ctx, "SELECT evidence_json FROM chain_scan_blocks WHERE block_height=100").Scan(&persistedBytes); err != nil {
		t.Fatal(err)
	}
	var persisted SolidBlockBatch
	if json.Unmarshal(persistedBytes, &persisted) != nil || len(persisted.Transactions) != 4 || persisted.Transactions[3].Disposition != tronContractFailed || persisted.Observation.At.IsZero() {
		t.Fatal("lost negative evidence or source metadata")
	}

	legacy, err := NewTronChainStore(pool, testScanConfig(), "solid-scan-v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Load(ctx); !errors.Is(err, ErrScanConflict) {
		t.Fatal("legacy reader silently adopted v2 policy")
	}

	receipt := testTronReceipt()
	receipt.ID, receipt.BlockNumber = strings.Repeat("ef", 32), testInt64(101)
	block := source.blocks[101]
	block.Transactions = []TronBlockTransaction{{TxID: receipt.ID, Ret: []TronExecutionResult{{ContractRet: "SUCCESS"}}, RawData: &TronTransactionRawData{Contracts: []TronContractType{{Type: "TriggerSmartContract"}}}}}
	source.blocks[101], source.receipts[receipt.ID] = block, receipt
	source.fail = receipt.ID
	if _, err := scanner.ScanOnce(ctx); !errors.Is(err, ErrChainUnavailable) {
		t.Fatal("missing receipt not retried", err)
	}
	cursor, err = store.Load(ctx)
	if err != nil || cursor.BlockHeight != 100 {
		t.Fatal("failed receipt advanced durable cursor")
	}
	source.fail = ""
	if committed, err := scanner.ScanOnce(ctx); err != nil || !committed {
		t.Fatal("backfill did not retry failed height", err)
	}

	restarted, err := NewTronScanStore(pool, testScanConfig(), "solid-scan-v2")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = restarted.Initialize(ctx, ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
	if err != nil || cursor.BlockHeight != 101 {
		t.Fatal("restart reset cursor")
	}
	next, err := testBlockReader(t, source).ReadNext(ctx, ScanCheckpoint{Height: 101, BlockID: testTronBlockID(101)})
	if err != nil {
		t.Fatal(err)
	}
	alternative := next
	observation := *next.Observation
	observation.At = observation.At.Add(time.Second)
	alternative.Observation = &observation
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	failures := make(chan error, 2)
	for _, batch := range []SolidBlockBatch{next, alternative} {
		wait.Add(1)
		go func(batch SolidBlockBatch) {
			defer wait.Done()
			committed, err := restarted.CommitSolidBlock(ctx, batch)
			results <- committed
			failures <- err
		}(batch)
	}
	wait.Wait()
	close(results)
	close(failures)
	writes := 0
	for committed := range results {
		if committed {
			writes++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatal("equivalent observations advanced twice or conflicted")
	}
	blocks, events = chainCounts(t, pool)
	if blocks != 3 || events != 3 {
		t.Fatal("backfill lost or duplicated events")
	}
	if committed, err := testScanner(t, source, restarted).ScanOnce(ctx); err != nil || committed {
		t.Fatal("caught-up scan did not idle", err)
	}
	if committed, err := restarted.CommitSolidBlock(ctx, persisted); err != nil || committed {
		t.Fatal("historical v2 replay failed", err)
	}

	down, err := migrations.Files.ReadFile("000007_chain_scan_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("unsafe v2 downgrade accepted")
	}
	if err := storage.Ready(ctx, pool); err != nil {
		t.Fatal("raw rejected downgrade damaged ready schema", err)
	}
}
