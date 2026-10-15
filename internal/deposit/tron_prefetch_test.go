package deposit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func prefetchFixture() *prefetchedScanSource {
	fixture := testMixedScanSource()
	all := map[int64][]TronReceipt{}
	for height, block := range fixture.blocks {
		all[height] = []TronReceipt{}
		for _, transaction := range block.Transactions {
			all[height] = append(all[height], fixture.receipts[transaction.TxID])
		}
	}
	return &prefetchedScanSource{source: fixture.SourceID(), genesis: fixture.genesis, head: fixture.blocks[fixture.head], blocks: fixture.blocks, receipts: all}
}

func TestPrefetchRangePreservesContiguousEvidence(t *testing.T) {
	source := prefetchFixture()
	reader := testBlockReader(t, source)
	all, err := reader.ReadRange(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}, 16)
	if err != nil || len(all) != 3 {
		t.Fatal("range not returned", err)
	}
	for index, batch := range all {
		if *batch.Block.Header.RawData.Number != int64(100+index) {
			t.Fatal("height order changed")
		}
		if _, _, _, err := prepareTronScanBatch(testScanConfig(), batch); err != nil {
			t.Fatal("range lost complete evidence", err)
		}
	}
	for _, change := range []struct {
		name   string
		mutate func(*prefetchedScanSource)
	}{
		{"later parent conflict", func(s *prefetchedScanSource) {
			block := s.blocks[101]
			block.Header.RawData.ParentHash = testTronBlockID(99)
			s.blocks[101] = block
		}},
		{"missing receipt", func(s *prefetchedScanSource) { s.receipts[100] = s.receipts[100][:1] }},
		{"foreign genesis", func(s *prefetchedScanSource) { s.genesis = "wrong" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s := prefetchFixture()
			change.mutate(s)
			all, err := testBlockReader(t, s).ReadRange(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}, 16)
			if err == nil || len(all) != 0 {
				t.Fatal("untrusted window returned partial evidence")
			}
		})
	}
}

func TestPrefetchCommitResponseLossReplaysPendingEvidence(t *testing.T) {
	source := prefetchFixture()
	repository := newTestScanRepository()
	repository.failAfter = true
	scanner, err := NewTronScanner(testBlockReader(t, source), repository, TronScannerOptions{CatchUpBatchSize: 16, PollInterval: time.Second, RequestTimeout: 10 * time.Second, MaxBackoff: time.Second, MaxConsecutiveFailures: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ScanOnce(context.Background()); !errors.Is(err, ErrChainUnavailable) {
		t.Fatal(err)
	}
	source.genesis = "changed after commit"
	if _, err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal("pending window re-fetched or lost", err)
	}
	if repository.cursor.BlockHeight != 102 || len(repository.batches) != 3 || string(repository.submitted[0]) != string(repository.submitted[1]) {
		t.Fatal("commit retry changed evidence or skipped a height")
	}
}
