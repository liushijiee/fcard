// nile-review audits contiguous solidified blocks without writing application data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fcard/internal/deposit"
	"github.com/joho/godotenv"
)

type recordingSource struct {
	*deposit.TronRPC
	directory string
}

func (source *recordingSource) save(name string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(source.directory, name), encoded, 0600)
}

func (source *recordingSource) SolidBlock(ctx context.Context, height int64) (deposit.TronBlock, error) {
	block, err := source.TronRPC.SolidBlock(ctx, height)
	if err == nil {
		err = source.save(fmt.Sprintf("block-%d.json", height), block)
	}
	return block, err
}

func (source *recordingSource) SolidReceipt(ctx context.Context, hash string) (deposit.TronReceipt, error) {
	receipt, err := source.TronRPC.SolidReceipt(ctx, hash)
	if err == nil {
		err = source.save("receipt-"+hash+".json", receipt)
	}
	return receipt, err
}

func (source *recordingSource) SolidReceipts(ctx context.Context, height int64) ([]deposit.TronReceipt, error) {
	receipts, err := source.TronRPC.SolidReceipts(ctx, height)
	if err != nil {
		return nil, err
	}
	for _, receipt := range receipts {
		if err := source.save("receipt-"+receipt.ID+".json", receipt); err != nil {
			return nil, err
		}
	}
	return receipts, nil
}

func main() {
	count := flag.Int("blocks", 10, "number of consecutive solid blocks to validate")
	window := flag.Int64("inspect-from", 0, "read-only diagnostic window starting at an existing scanner's next height; never moves a cursor")
	directory := flag.String("output", ".cache/nile-live-review", "local evidence directory")
	flag.Parse()
	if *count < 1 || *count > 100 {
		panic("blocks must be 1..100")
	}
	if err := run(*count, *directory, *window); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(count int, directory string, window int64) error {
	if err := godotenv.Load(".env.deposits"); err != nil {
		return errors.New("private deposit config unavailable")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	rpc, err := deposit.NewTronRPC(deposit.TronRPCConfig{Environment: "nile", BaseURL: "https://nile.trongrid.io"})
	if err != nil {
		return err
	}
	source := &recordingSource{TronRPC: rpc, directory: directory}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	genesis, err := rpc.GenesisID(ctx)
	if err != nil || genesis != deposit.NileGenesisID {
		return errors.New("Nile genesis verification failed")
	}
	var checkpoint deposit.ScanCheckpoint
	checkpointFile := filepath.Join(directory, "checkpoint.json")
	encoded, err := os.ReadFile(checkpointFile)
	if err == nil {
		if json.Unmarshal(encoded, &checkpoint) != nil {
			return errors.New("invalid saved checkpoint")
		}
	} else if os.IsNotExist(err) {
		head, err := rpc.SolidHead(ctx)
		if err != nil || head.Header.RawData.Number == nil {
			return errors.New("solid head unavailable")
		}
		anchor, err := source.SolidBlock(ctx, *head.Header.RawData.Number-3)
		if err != nil {
			return err
		}
		checkpoint = deposit.ScanCheckpoint{Height: *anchor.Header.RawData.Number, BlockID: anchor.ID}
		if err := source.save("checkpoint.json", checkpoint); err != nil {
			return err
		}
	} else {
		return err
	}
	if window > 0 {
		anchor, err := source.SolidBlock(ctx, window-1)
		if err != nil {
			return err
		}
		checkpoint = deposit.ScanCheckpoint{Height: window - 1, BlockID: anchor.ID}
	}
	// Review uses the runtime's configuration validation, but never starts it.
	os.Setenv("DEPOSIT_RUNTIME_ENABLED", "true")
	os.Setenv("DEPOSIT_CREATE_ENABLED", "false")
	os.Setenv("DEPOSIT_ASSET_REVIEWED", "true")
	os.Setenv("DEPOSIT_SCAN_START_HEIGHT", fmt.Sprint(checkpoint.Height))
	os.Setenv("DEPOSIT_SCAN_START_BLOCK_ID", checkpoint.BlockID)
	settings, err := deposit.SettingsFromEnvironment()
	if err != nil {
		return err
	}
	reader, err := deposit.NewTronBlockReader(settings.Order.Chain, source)
	if err != nil {
		return err
	}
	fmt.Printf("review checkpoint=%d id=%s; no orders or database writes\n", checkpoint.Height, checkpoint.BlockID)
	if window > 0 {
		batches, err := reader.ReadRange(ctx, checkpoint, min(count, 32))
		if err != nil {
			return err
		}
		fmt.Printf("diagnostic window verified: %d blocks; database cursor unchanged\n", len(batches))
		return nil
	}
	for index := 0; index < count; {
		step, cancel := context.WithTimeout(ctx, 2*time.Minute)
		batch, err := reader.ReadNext(step, checkpoint)
		cancel()
		if errors.Is(err, deposit.ErrScanCaughtUp) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("review stopped at block %d: %w; inspect saved block/receipts", checkpoint.Height+1, err)
		}
		if err := source.save(fmt.Sprintf("batch-%d.json", checkpoint.Height+1), batch); err != nil {
			return err
		}
		types := map[string]int{}
		for _, transaction := range batch.Block.Transactions {
			for _, contract := range transaction.RawData.Contracts {
				types[contract.Type]++
			}
		}
		encoded, _ := json.Marshal(types)
		fmt.Printf("validated block=%d transactions=%d types=%s\n", checkpoint.Height+1, len(batch.Transactions), encoded)
		checkpoint = deposit.ScanCheckpoint{Height: checkpoint.Height + 1, BlockID: batch.Block.ID}
		index++
	}
	return source.save("review-result.json", map[string]any{"checkpoint": settings.Checkpoint, "last": checkpoint, "blocks": count, "at": time.Now().UTC()})
}
