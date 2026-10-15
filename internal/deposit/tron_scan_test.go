package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testScanSource struct {
	genesis  string
	head     int64
	blocks   map[int64]TronBlock
	receipts map[string]TronReceipt
	fail     string
	calls    []string
}

func testScanConfig() TronVerifierConfig {
	return TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), Contract: testTronContract(), Decimals: 6}
}

func testMixedScanSource() *testScanSource {
	source := &testScanSource{genesis: testTronGenesis(), head: 102, blocks: map[int64]TronBlock{}, receipts: map[string]TronReceipt{}}
	for height := int64(99); height <= 102; height++ {
		source.blocks[height] = testTronBlock(height)
	}
	block := source.blocks[100]
	types := []string{"TransferContract", "TransferAssetContract", "TriggerSmartContract", "TriggerSmartContract"}
	hashes := []string{strings.Repeat("aa", 32), strings.Repeat("bb", 32), testTronHash(), strings.Repeat("cc", 32)}
	for index, contractType := range types {
		result := "SUCCESS"
		receipt := testTronReceipt()
		receipt.ID = hashes[index]
		if index < 2 {
			receipt.Receipt.Result = ""
			receipt.Logs = nil
		}
		if index == 2 {
			receipt.Logs[0].Data = strings.Repeat("f", 64)
			receipt.Logs = append(receipt.Logs, testTronLog())
			receipt.Logs[1].Data = strings.Repeat("0", 64)
		}
		if index == 3 {
			result = "REVERT"
			receipt.Result, receipt.Receipt.Result, receipt.Logs = "FAILED", "REVERT", nil
		}
		block.Transactions = append(block.Transactions, TronBlockTransaction{TxID: receipt.ID, Ret: []TronExecutionResult{{ContractRet: result}}, RawData: &TronTransactionRawData{Contracts: []TronContractType{{Type: contractType}}}})
		source.receipts[receipt.ID] = receipt
	}
	source.blocks[100] = block
	return source
}

func (source *testScanSource) SourceID() string { return "https://nile.trongrid.io" }

func (source *testScanSource) step(ctx context.Context, call string) error {
	source.calls = append(source.calls, call)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if source.fail == call {
		return ErrChainUnavailable
	}
	return nil
}

func (source *testScanSource) GenesisID(ctx context.Context) (string, error) {
	return source.genesis, source.step(ctx, "genesis")
}

func (source *testScanSource) SolidHead(ctx context.Context) (TronBlock, error) {
	return source.blocks[source.head], source.step(ctx, "head")
}

func (source *testScanSource) SolidBlock(ctx context.Context, height int64) (TronBlock, error) {
	return source.blocks[height], source.step(ctx, "block")
}

func (source *testScanSource) SolidReceipt(ctx context.Context, hash string) (TronReceipt, error) {
	return source.receipts[hash], source.step(ctx, hash)
}

func testBlockReader(t testing.TB, source TronScanSource) *TronBlockReader {
	t.Helper()
	reader, err := NewTronBlockReader(testScanConfig(), source)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestTronBlockReaderMixedEvidence(t *testing.T) {
	source := testMixedScanSource()
	batch, err := testBlockReader(t, source).ReadNext(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Version != 2 || len(batch.Transactions) != 4 || batch.Observation.Source != source.SourceID() || batch.Observation.At.IsZero() {
		t.Fatal("incomplete block evidence")
	}
	expected := []string{tronNativeSuccess, tronNativeSuccess, tronContractSuccess, tronContractFailed}
	for index, disposition := range expected {
		if batch.Transactions[index].Disposition != disposition {
			t.Fatalf("wrong classification at %d", index)
		}
		if index != 2 && len(batch.Transactions[index].Transfers) != 0 {
			t.Fatal("non-payment evidence contained transfers")
		}
	}
	if len(batch.Transactions[2].Transfers) != 2 {
		t.Fatal("lost contract logs")
	}
	_, digest, _, err := prepareTronScanBatch(testScanConfig(), batch)
	if err != nil {
		t.Fatal(err)
	}
	firstObservation := batch.Observation
	changed := *firstObservation
	changed.At = changed.At.Add(time.Second)
	changed.Head = testTronBlock(103)
	batch.Observation = &changed
	_, nextDigest, _, err := prepareTronScanBatch(testScanConfig(), batch)
	if err != nil || digest != nextDigest {
		t.Fatal("observation time/head changed replay identity")
	}
	batch.Transactions[2].Transfers[0].Amount = "1"
	if _, _, _, err := prepareTronScanBatch(testScanConfig(), batch); !errors.Is(err, ErrInvalidChainEvidence) {
		t.Fatal("edited transfer accepted")
	}
}

func TestTronBlockReaderRejectsIncompleteOrConflictingEvidence(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*testScanSource)
		expected error
	}{
		{"wrong genesis", func(source *testScanSource) { source.genesis = strings.Repeat("ef", 32) }, ErrWrongChain},
		{"missing receipt", func(source *testScanSource) { delete(source.receipts, testTronHash()) }, ErrInvalidChainEvidence},
		{"receipt unavailable", func(source *testScanSource) { source.fail = testTronHash() }, ErrChainUnavailable},
		{"native logs", func(source *testScanSource) {
			receipt := source.receipts[strings.Repeat("aa", 32)]
			receipt.Logs = []TronLog{testTronLog()}
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"native missing ret", func(source *testScanSource) { source.blocks[100].Transactions[0].Ret = nil }, ErrInvalidChainEvidence},
		{"multiple contract types", func(source *testScanSource) {
			source.blocks[100].Transactions[0].RawData.Contracts = append(source.blocks[100].Transactions[0].RawData.Contracts, TronContractType{Type: "TriggerSmartContract"})
		}, ErrInvalidChainEvidence},
		{"unknown type", func(source *testScanSource) {
			source.blocks[100].Transactions[0].RawData.Contracts[0].Type = "CustomContract"
		}, ErrUnsupportedTronTransaction},
		{"unknown failure", func(source *testScanSource) { source.blocks[100].Transactions[3].Ret[0].ContractRet = "UNKNOWN" }, ErrInvalidChainEvidence},
		{"failed logs", func(source *testScanSource) {
			receipt := source.receipts[strings.Repeat("cc", 32)]
			receipt.Logs = []TronLog{testTronLog()}
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"failed result mismatch", func(source *testScanSource) {
			receipt := source.receipts[strings.Repeat("cc", 32)]
			receipt.Receipt.Result = "OUT_OF_ENERGY"
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"failed missing top result", func(source *testScanSource) {
			receipt := source.receipts[strings.Repeat("cc", 32)]
			receipt.Result = ""
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"wrong receipt block", func(source *testScanSource) {
			receipt := source.receipts[testTronHash()]
			receipt.BlockNumber = testInt64(101)
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"wrong receipt time", func(source *testScanSource) {
			receipt := source.receipts[testTronHash()]
			receipt.BlockTimeStamp = testInt64(testTronTimestamp + 1)
			source.receipts[receipt.ID] = receipt
		}, ErrInvalidChainEvidence},
		{"wrong parent", func(source *testScanSource) {
			block := source.blocks[100]
			block.Header.RawData.ParentHash = testTronBlockID(99)[:16] + strings.Repeat("ee", 24)
			source.blocks[100] = block
		}, ErrInvalidChainEvidence},
		{"adjacent solid head disagrees", func(source *testScanSource) {
			source.head = 101
			block := source.blocks[101]
			block.Header.RawData.ParentHash = testTronBlockID(100)[:16] + strings.Repeat("ee", 24)
			source.blocks[101] = block
		}, ErrInvalidChainEvidence},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := testMixedScanSource()
			test.change(source)
			batch, err := testBlockReader(t, source).ReadNext(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
			if !errors.Is(err, test.expected) || len(batch.Transactions) != 0 {
				t.Fatalf("partial or invalid evidence accepted: %v", err)
			}
		})
	}
}

func TestTronBlockReaderHeadAndCancellation(t *testing.T) {
	source := testMixedScanSource()
	reader := testBlockReader(t, source)
	ctx := context.Background()
	if _, err := reader.ReadNext(ctx, ScanCheckpoint{Height: 102, BlockID: testTronBlockID(102)}); !errors.Is(err, ErrScanCaughtUp) {
		t.Fatal(err)
	}
	if _, err := reader.ReadNext(ctx, ScanCheckpoint{Height: 103, BlockID: testTronBlockID(103)}); !errors.Is(err, ErrNotSolidified) {
		t.Fatal(err)
	}
	if _, err := reader.ReadNext(ctx, ScanCheckpoint{Height: 99, BlockID: testTronBlockID(98)}); !errors.Is(err, ErrScanConflict) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reader.ReadNext(canceled, ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := NewTronBlockReader(testScanConfig(), nil); err == nil {
		t.Fatal("nil source accepted")
	}
}

func TestTronScanDefaultNormalization(t *testing.T) {
	source := testMixedScanSource()
	checkpoint := ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}
	first, err := testBlockReader(t, source).ReadNext(context.Background(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	source.blocks[100].Transactions[0].Ret[0].Ret = "SUCESS"
	receipt := source.receipts[strings.Repeat("aa", 32)]
	receipt.Result, receipt.Receipt.Result = "SUCESS", "DEFAULT"
	receipt.Logs = []TronLog{}
	source.receipts[receipt.ID] = receipt
	second, err := testBlockReader(t, source).ReadNext(context.Background(), checkpoint)
	if err != nil || !sameScanCore(first, second) {
		t.Fatal("equivalent protobuf defaults changed the scan evidence", err)
	}
}

func TestTronScanKnownFailuresPreserveNoPaymentEvidence(t *testing.T) {
	for _, failure := range []string{"REVERT", "BAD_JUMP_DESTINATION", "OUT_OF_MEMORY", "PRECOMPILED_CONTRACT", "STACK_TOO_SMALL", "STACK_TOO_LARGE", "ILLEGAL_OPERATION", "STACK_OVERFLOW", "OUT_OF_ENERGY", "OUT_OF_TIME", "JVM_STACK_OVER_FLOW", "TRANSFER_FAILED", "INVALID_CODE"} {
		t.Run(failure, func(t *testing.T) {
			source := testMixedScanSource()
			transaction := source.blocks[100].Transactions[3]
			transaction.Ret[0].ContractRet = failure
			receipt := source.receipts[transaction.TxID]
			receipt.Receipt.Result = failure
			evidence, err := scanTransactionEvidence(testScanConfig(), source.blocks[100], transaction, receipt)
			if err != nil || evidence.Disposition != tronContractFailed || len(evidence.Transfers) != 0 {
				t.Fatal("failed execution was lost or treated as payment", err)
			}
			evidence.Transfers = []TronTransfer{{Contract: testTronContract(), To: testTronRecipientHex(), Amount: "20010002"}}
			expected := TronPaymentExpectation{Environment: TronEnvironmentNile, GenesisID: evidence.GenesisID, TxHash: evidence.TxHash, Contract: testTronContract(), Recipient: testTronRecipientHex(), Units: 20010002, NotBefore: evidence.BlockTime.Add(-time.Minute), ExpiresAt: evidence.BlockTime.Add(time.Minute)}
			if _, err := MatchTronPayment(evidence, expected); !errors.Is(err, ErrTransferMismatch) {
				t.Fatal("failed disposition reached payment matching")
			}
		})
	}
}

func TestReviewedResourceDelegationHasNoPaymentEvidence(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*TronBlockTransaction, *TronReceipt)
		valid  bool
	}{
		{"reviewed success", func(*TronBlockTransaction, *TronReceipt) {}, true},
		{"reviewed stake success", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "FreezeBalanceV2Contract"
		}, true},
		{"reviewed undelegation success", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "UnDelegateResourceContract"
		}, true},
		{"reviewed witness vote success", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.RawData.Contracts[0].Type = "VoteWitnessContract" }, true},
		{"reviewed unstake success", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "UnfreezeBalanceV2Contract"
		}, true},
		{"reviewed account creation", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.RawData.Contracts[0].Type = "AccountCreateContract" }, true},
		{"reviewed witness withdrawal", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "WithdrawBalanceContract"
		}, true},
		{"reviewed expired unstake withdrawal", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "WithdrawExpireUnfreezeContract"
		}, true},
		{"reviewed account permission update", func(tx *TronBlockTransaction, _ *TronReceipt) {
			tx.RawData.Contracts[0].Type = "AccountPermissionUpdateContract"
		}, true},
		{"missing block success", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.Ret = nil }, false},
		{"failed block", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.Ret[0].ContractRet = "REVERT" }, false},
		{"receipt failure", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Result = "FAILED" }, false},
		{"TVM failure", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Receipt.Result = "REVERT" }, false},
		{"unexpected token logs", func(_ *TronBlockTransaction, receipt *TronReceipt) { receipt.Logs = []TronLog{testTronLog()} }, false},
		{"unreviewed native", func(tx *TronBlockTransaction, _ *TronReceipt) { tx.RawData.Contracts[0].Type = "FutureNativeContract" }, false},
	} {
		t.Run(change.name, func(t *testing.T) {
			source := testMixedScanSource()
			tx := source.blocks[100].Transactions[0]
			tx.RawData.Contracts[0].Type = "DelegateResourceContract"
			receipt := source.receipts[tx.TxID]
			change.mutate(&tx, &receipt)
			evidence, err := scanTransactionEvidence(testScanConfig(), source.blocks[100], tx, receipt)
			if !change.valid {
				if err == nil {
					t.Fatal("untrusted native evidence accepted")
				}
				return
			}
			if err != nil || evidence.Disposition != tronNativeSuccess || len(evidence.Transfers) != 0 {
				t.Fatal("native operation became payment evidence", err)
			}
		})
	}
}

func TestTronBlockReaderSolidityRPCContract(t *testing.T) {
	source := testMixedScanSource()
	blockJSON, err := json.Marshal(source.blocks[100])
	if err != nil {
		t.Fatal(err)
	}
	if rejectDuplicateRPCKeys(blockJSON) != nil {
		t.Fatal("invalid JSON fixture")
	}
	var decoded TronBlock
	if json.Unmarshal(blockJSON, &decoded) != nil || len(decoded.Transactions[0].Ret) != 1 || decoded.Transactions[0].RawData.Contracts[0].Type != "TransferContract" {
		t.Fatal("lost actual ret/raw_data.contract JSON fields")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Num   int64  `json:"num"`
			Value string `json:"value"`
		}
		if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&input) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/walletsolidity/getblockbynum":
			if input.Num == 0 {
				json.NewEncoder(writer).Encode(testTronBlock(0))
				return
			}
			json.NewEncoder(writer).Encode(source.blocks[input.Num])
		case "/walletsolidity/getnowblock":
			json.NewEncoder(writer).Encode(source.blocks[source.head])
		case "/walletsolidity/gettransactioninfobyid":
			json.NewEncoder(writer).Encode(source.receipts[input.Value])
		case "/walletsolidity/gettransactioninfobyblocknum":
			all := []TronReceipt{}
			for _, transaction := range source.blocks[input.Num].Transactions {
				all = append(all, source.receipts[transaction.TxID])
			}
			json.NewEncoder(writer).Encode(all)
		default:
			t.Error("scanner used a non-Solidity endpoint")
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	batch, err := testBlockReader(t, testTronRPC(t, server.URL)).ReadNext(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
	if err != nil || len(batch.Transactions) != 4 || batch.Observation.Source != server.URL {
		t.Fatalf("Solidity HTTP scan failed: %v", err)
	}
}

type batchReceiptSource struct {
	*testScanSource
	alter func([]TronReceipt) []TronReceipt
}

func (source batchReceiptSource) SolidReceipts(_ context.Context, height int64) ([]TronReceipt, error) {
	all := []TronReceipt{}
	for _, transaction := range source.blocks[height].Transactions {
		all = append(all, source.receipts[transaction.TxID])
	}
	return source.alter(all), nil
}

func TestBatchReceiptsRequireExactBlockCoverage(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		alter func([]TronReceipt) []TronReceipt
		valid bool
	}{
		{"reordered complete", func(all []TronReceipt) []TronReceipt { all[0], all[1] = all[1], all[0]; return all }, true},
		{"missing receipt", func(all []TronReceipt) []TronReceipt { return all[:len(all)-1] }, false},
		{"duplicate receipt", func(all []TronReceipt) []TronReceipt { all[1] = all[0]; return all }, false},
		{"extra receipt", func(all []TronReceipt) []TronReceipt { return append(all, all[0]) }, false},
		{"foreign receipt", func(all []TronReceipt) []TronReceipt { all[0].ID = strings.Repeat("dd", 32); return all }, false},
		{"wrong block", func(all []TronReceipt) []TronReceipt { all[0].BlockNumber = testInt64(101); return all }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := batchReceiptSource{testMixedScanSource(), scenario.alter}
			batch, err := testBlockReader(t, source).ReadNext(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
			if scenario.valid {
				if err != nil || len(batch.Transactions) != 4 {
					t.Fatal("complete receipt batch rejected", err)
				}
			} else if err == nil || len(batch.Transactions) != 0 {
				t.Fatal("incomplete receipt batch accepted", err)
			}
		})
	}
}
