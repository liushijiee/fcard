package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"fcard/internal/storage/db"
)

type testScanRepository struct {
	cursor     db.ChainScanCursor
	batches    []SolidBlockBatch
	failBefore bool
	failAfter  bool
	calls      int
	submitted  [][]byte
}

func newTestScanRepository() *testScanRepository {
	config := testScanConfig()
	return &testScanRepository{cursor: db.ChainScanCursor{ChainEnvironment: config.Environment, GenesisID: config.GenesisID, Network: "tron", Contract: config.Contract, EvidenceVersion: 2, BlockHeight: 99, BlockID: testTronBlockID(99)}}
}

func (repository *testScanRepository) Load(ctx context.Context) (db.ChainScanCursor, error) {
	return repository.cursor, ctx.Err()
}

func (repository *testScanRepository) CommitSolidBlock(ctx context.Context, batch SolidBlockBatch) (bool, error) {
	repository.calls++
	encoded, _ := json.Marshal(batch)
	repository.submitted = append(repository.submitted, encoded)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if repository.failBefore {
		repository.failBefore = false
		return false, ErrChainUnavailable
	}
	height := *batch.Block.Header.RawData.Number
	for _, previous := range repository.batches {
		if *previous.Block.Header.RawData.Number == height {
			if !sameScanCore(previous, batch) {
				return false, ErrScanConflict
			}
			return false, nil
		}
	}
	if repository.cursor.BlockHeight != height-1 || repository.cursor.BlockID != batch.Block.Header.RawData.ParentHash {
		return false, ErrScanConflict
	}
	repository.batches = append(repository.batches, batch)
	repository.cursor.BlockHeight, repository.cursor.BlockID = height, batch.Block.ID
	if repository.failAfter {
		repository.failAfter = false
		return false, ErrChainUnavailable
	}
	return true, nil
}

func testScanner(t testing.TB, source TronScanSource, repository TronScanRepository) *TronScanner {
	t.Helper()
	scanner, err := NewTronScanner(testBlockReader(t, source), repository, TronScannerOptions{PollInterval: 10 * time.Millisecond, RequestTimeout: time.Second, MaxBackoff: 40 * time.Millisecond, MaxConsecutiveFailures: 3})
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func TestTronScannerCommitUnknownReplaysIdenticalPendingBatch(t *testing.T) {
	for _, afterCommit := range []bool{false, true} {
		name := "before-commit"
		if afterCommit {
			name = "after-commit-response-lost"
		}
		t.Run(name, func(t *testing.T) {
			source := testMixedScanSource()
			repository := newTestScanRepository()
			repository.failBefore, repository.failAfter = !afterCommit, afterCommit
			scanner := testScanner(t, source, repository)
			ctx := context.Background()
			if _, err := scanner.ScanOnce(ctx); !errors.Is(err, ErrChainUnavailable) {
				t.Fatal(err)
			}
			count := len(source.calls)
			source.blocks[100].Transactions[0].Ret[0].ContractRet = "UNKNOWN"
			if _, err := scanner.ScanOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if len(source.calls) != count || string(repository.submitted[0]) != string(repository.submitted[1]) || len(repository.batches) != 1 || repository.cursor.BlockHeight != 100 {
				t.Fatal("unknown commit re-fetched evidence, changed observation or duplicated block")
			}
			restarted := testScanner(t, source, repository)
			if committed, err := restarted.ScanOnce(ctx); err != nil || !committed || repository.cursor.BlockHeight != 101 {
				t.Fatalf("restart did not resume: %v", err)
			}
		})
	}
}

func TestTronScannerDoesNotSkipReceiptFailure(t *testing.T) {
	source := testMixedScanSource()
	source.fail = testTronHash()
	repository := newTestScanRepository()
	scanner := testScanner(t, source, repository)
	ctx := context.Background()
	if _, err := scanner.ScanOnce(ctx); !errors.Is(err, ErrChainUnavailable) || repository.calls != 0 || repository.cursor.BlockHeight != 99 {
		t.Fatal("partial block advanced")
	}
	source.fail = ""
	if committed, err := scanner.ScanOnce(ctx); err != nil || !committed || repository.cursor.BlockHeight != 100 {
		t.Fatalf("failed block was not retried: %v", err)
	}
	repository.cursor.EvidenceVersion = 1
	if _, err := scanner.ScanOnce(ctx); !errors.Is(err, ErrScanConflict) {
		t.Fatal("scanner accepted legacy scope")
	}
}

func TestTronScannerRunStopsOrRetriesWithinBudget(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Run("transient retry budget", func(t *testing.T) {
		source := testMixedScanSource()
		source.fail = testTronHash()
		repository := newTestScanRepository()
		scanner := testScanner(t, source, repository)
		if err := scanner.Run(context.Background(), logger); !errors.Is(err, ErrChainUnavailable) {
			t.Fatal(err)
		}
		attempts := 0
		for _, call := range source.calls {
			if call == testTronHash() {
				attempts++
			}
		}
		if attempts != 3 || repository.calls != 0 {
			t.Fatal("retry budget or no-advance violated")
		}
	})
	t.Run("unknown type stops immediately", func(t *testing.T) {
		source := testMixedScanSource()
		source.blocks[100].Transactions[0].RawData.Contracts[0].Type = "CustomContract"
		repository := newTestScanRepository()
		if err := testScanner(t, source, repository).Run(context.Background(), logger); !errors.Is(err, ErrUnsupportedTronTransaction) || repository.calls != 0 {
			t.Fatal("unknown transaction skipped")
		}
	})
	t.Run("caught up cancellation", func(t *testing.T) {
		source := testMixedScanSource()
		repository := newTestScanRepository()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := testScanner(t, source, repository).Run(ctx, logger); !errors.Is(err, context.DeadlineExceeded) || repository.cursor.BlockHeight != 102 {
			t.Fatalf("poll cancellation or backfill failed: %v", err)
		}
	})
	if _, err := NewTronScanner(nil, newTestScanRepository(), TronScannerOptions{}); err == nil {
		t.Fatal("unbounded options accepted")
	}
}
