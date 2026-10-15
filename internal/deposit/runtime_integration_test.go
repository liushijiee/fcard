package deposit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestDepositRuntimeLifecycleIntegration(test *testing.T) {
	for _, unsupported := range []bool{false, true} {
		name := "cancel"
		if unsupported {
			name = "unsupported_transaction"
		}
		test.Run(name, func(test *testing.T) {
			pool := chainIntegrationDatabase(test)
			service, _ := managementTestService(test, pool)
			service.store.config.Enabled = false
			store, err := NewTronScanStore(pool, testScanConfig(), "runtime-synthetic")
			if err != nil {
				test.Fatal(err)
			}
			if _, err := store.Initialize(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); err != nil {
				test.Fatal(err)
			}
			blocks := map[int64]TronBlock{}
			for _, height := range []int64{99, 100} {
				block := testTronBlock(height)
				block.Header.RawData.Timestamp = testInt64(time.Now().Add(time.Duration(height-100) * time.Second).UnixMilli())
				blocks[height] = block
			}
			if unsupported {
				block := blocks[100]
				block.Transactions = []TronBlockTransaction{{TxID: testTronHash(), Ret: []TronExecutionResult{{ContractRet: "SUCCESS"}}, RawData: &TronTransactionRawData{Contracts: []TronContractType{{Type: "UnsupportedSyntheticContract"}}}}}
				blocks[100] = block
			}
			source := &testScanSource{genesis: testTronGenesis(), head: 100, blocks: blocks, receipts: map[string]TronReceipt{testTronHash(): testTronReceipt()}}
			leases := jobTestLeases(test, pool)
			callbacks, err := NewCallbackWorker(leases)
			if err != nil {
				test.Fatal(err)
			}
			chain, err := NewChainWorker(leases)
			if err != nil {
				test.Fatal(err)
			}
			candidates, err := NewChainCandidatePublisher(pool, testScanConfig())
			if err != nil {
				test.Fatal(err)
			}
			runtime := &Runtime{Service: service, creator: service.creator, scanner: testScanner(test, source, store), callbacks: callbacks, chain: chain, candidates: candidates}
			service.admission = runtime.AdmissionReady
			service.creator.admission = runtime.AdmissionReady
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runtime.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
			if !unsupported {
				for !runtime.AdmissionReady() && ctx.Err() == nil {
					time.Sleep(10 * time.Millisecond)
				}
				if !runtime.AdmissionReady() || service.Enabled() {
					test.Fatal("runtime gate or disabled create switch invalid")
				}
				cancel()
			}
			select {
			case err := <-done:
				if unsupported && err == nil {
					test.Fatal("unsafe scanner did not stop runtime")
				}
				if !unsupported && !errors.Is(err, context.Canceled) {
					test.Fatal(err)
				}
			case <-time.After(12 * time.Second):
				test.Fatal("workers failed to drain")
			}
			if runtime.AdmissionReady() || service.Enabled() {
				test.Fatal("stopped runtime admitted create")
			}
		})
	}
}
