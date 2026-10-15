package deposit

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ReadRange fetches a bounded window in parallel and validates it in strict
// height order using ReadNext. No batch is returned if any block is untrusted.
func (reader *TronBlockReader) ReadRange(ctx context.Context, checkpoint ScanCheckpoint, limit int) ([]SolidBlockBatch, error) {
	if limit < 1 || limit > 32 {
		return nil, ErrInvalidChainEvidence
	}
	bulk, ok := reader.source.(interface {
		SolidReceipts(context.Context, int64) ([]TronReceipt, error)
	})
	if !ok {
		batch, err := reader.ReadNext(ctx, checkpoint)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", checkpoint.Height+1, err)
		}
		return []SolidBlockBatch{batch}, nil
	}
	genesis, err := reader.source.GenesisID(ctx)
	if err != nil {
		return nil, err
	}
	if genesis != reader.config.GenesisID {
		return nil, ErrWrongChain
	}
	head, err := reader.source.SolidHead(ctx)
	if err != nil {
		return nil, err
	}
	height, err := tronBlockHeight(head)
	if err != nil {
		return nil, err
	}
	if height < checkpoint.Height {
		return nil, ErrNotSolidified
	}
	if height == checkpoint.Height {
		if head.ID != checkpoint.BlockID {
			return nil, ErrScanConflict
		}
		return nil, ErrScanCaughtUp
	}
	anchor, err := reader.source.SolidBlock(ctx, checkpoint.Height)
	if err != nil {
		return nil, err
	}
	count := min(int(height-checkpoint.Height), limit)
	snapshot := &prefetchedScanSource{source: reader.source.SourceID(), genesis: genesis, head: head, blocks: map[int64]TronBlock{checkpoint.Height: anchor}, receipts: map[int64][]TronReceipt{}}
	errorsByHeight := make([]error, count)
	var workers sync.WaitGroup
	var mutex sync.Mutex
	jobs := make(chan int, count)
	for index := 0; index < count; index++ {
		jobs <- index
	}
	close(jobs)
	for worker := 0; worker < min(4, count); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				next := checkpoint.Height + int64(index) + 1
				block, err := reader.source.SolidBlock(ctx, next)
				var receipts []TronReceipt
				if err == nil && len(block.Transactions) > 0 {
					receipts, err = bulk.SolidReceipts(ctx, next)
				}
				mutex.Lock()
				errorsByHeight[index] = err
				snapshot.blocks[next] = block
				snapshot.receipts[next] = receipts
				mutex.Unlock()
			}
		}()
	}
	workers.Wait()
	for _, err := range errorsByHeight {
		if err != nil {
			return nil, err
		}
	}
	local, err := NewTronBlockReader(reader.config, snapshot)
	if err != nil {
		return nil, err
	}
	batches := make([]SolidBlockBatch, 0, count)
	for index := 0; index < count; index++ {
		batch, err := local.ReadNext(ctx, checkpoint)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", checkpoint.Height+1, err)
		}
		batches = append(batches, batch)
		checkpoint = ScanCheckpoint{Height: checkpoint.Height + 1, BlockID: batch.Block.ID}
	}
	return batches, nil
}

type prefetchedScanSource struct {
	source   string
	genesis  string
	head     TronBlock
	blocks   map[int64]TronBlock
	receipts map[int64][]TronReceipt
}

func (source *prefetchedScanSource) SourceID() string { return source.source }
func (source *prefetchedScanSource) GenesisID(context.Context) (string, error) {
	return source.genesis, nil
}
func (source *prefetchedScanSource) SolidHead(context.Context) (TronBlock, error) {
	return source.head, nil
}
func (source *prefetchedScanSource) SolidBlock(_ context.Context, height int64) (TronBlock, error) {
	block, ok := source.blocks[height]
	if !ok {
		return TronBlock{}, ErrInvalidChainEvidence
	}
	return block, nil
}
func (source *prefetchedScanSource) SolidReceipts(_ context.Context, height int64) ([]TronReceipt, error) {
	all, ok := source.receipts[height]
	if !ok {
		return nil, ErrInvalidChainEvidence
	}
	return all, nil
}
func (source *prefetchedScanSource) SolidReceipt(context.Context, string) (TronReceipt, error) {
	return TronReceipt{}, errors.New("prefetched source requires complete block receipts")
}
