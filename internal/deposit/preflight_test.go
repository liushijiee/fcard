package deposit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNilePreflightOfflineContract(test *testing.T) {
	for _, scenario := range []string{"valid", "wrong_genesis", "wrong_checkpoint", "stale_head", "create_enabled"} {
		test.Run(scenario, func(test *testing.T) {
			settings := Settings{RuntimeEnabled: true, Order: OrderStoreConfig{Chain: TronVerifierConfig{Environment: "nile", GenesisID: NileGenesisID, Contract: testTronContract(), Decimals: 6}}, Checkpoint: ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}}
			head := testTronBlock(100)
			head.Header.RawData.Timestamp = testInt64(time.Now().Add(-time.Second).UnixMilli())
			source := &testScanSource{genesis: NileGenesisID, head: 100, blocks: map[int64]TronBlock{99: testTronBlock(99), 100: head}}
			switch scenario {
			case "wrong_genesis":
				source.genesis = testTronGenesis()
			case "wrong_checkpoint":
				settings.Checkpoint.BlockID = strings.Repeat("ab", 32)
			case "stale_head":
				head.Header.RawData.Timestamp = testInt64(time.Now().Add(-5 * time.Minute).UnixMilli())
				source.blocks[100] = head
			case "create_enabled":
				settings.CreateEnabled = true
			}
			result, err := preflightSource(context.Background(), settings, source)
			if scenario == "valid" {
				if err != nil || result.HeadHeight != 100 || result.GenesisID != NileGenesisID {
					test.Fatal(result, err)
				}
			} else if err == nil {
				test.Fatal("unsafe preflight accepted")
			}
			if scenario == "create_enabled" && len(source.calls) != 0 {
				test.Fatal("preflight made request with creation enabled")
			}
		})
	}
	if _, err := preflightSource(context.Background(), Settings{}, nil); err == nil {
		test.Fatal("disabled preflight accepted")
	}
}
