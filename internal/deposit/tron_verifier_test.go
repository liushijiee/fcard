package deposit

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testTronRecipient = "TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E"
const testTronTimestamp = int64(1791864000000)

func testTronHash() string         { return strings.Repeat("ab", 32) }
func testTronContract() string     { return "41" + strings.Repeat("33", 20) }
func testTronRecipientHex() string { return "41" + strings.Repeat("22", 20) }
func testTronGenesis() string      { return testTronBlock(0).ID }
func testInt64(value int64) *int64 { return &value }

func testTronBlock(height int64, hashes ...string) TronBlock {
	var block TronBlock
	encoded := make([]byte, 32)
	binary.BigEndian.PutUint64(encoded[:8], uint64(height))
	for index := 8; index < len(encoded); index++ {
		encoded[index] = 0xcd
	}
	block.ID = hex.EncodeToString(encoded)
	block.Header.RawData.Number = testInt64(height)
	block.Header.RawData.Timestamp = testInt64(testTronTimestamp)
	if height > 0 {
		block.Header.RawData.ParentHash = testTronBlockID(height - 1)
	}
	for _, hash := range hashes {
		block.Transactions = append(block.Transactions, TronBlockTransaction{TxID: hash})
	}
	return block
}

func testTronBlockID(height int64) string {
	encoded := make([]byte, 32)
	binary.BigEndian.PutUint64(encoded[:8], uint64(height))
	for index := 8; index < len(encoded); index++ {
		encoded[index] = 0xcd
	}
	return hex.EncodeToString(encoded)
}

func testTronLog() TronLog {
	return TronLog{Address: strings.Repeat("33", 20), Topics: []string{tronTransferTopic, strings.Repeat("0", 24) + strings.Repeat("11", 20), strings.Repeat("0", 24) + strings.Repeat("22", 20)}, Data: fmt.Sprintf("%064x", 20010002)}
}

func testTronReceipt() TronReceipt {
	receipt := TronReceipt{ID: testTronHash(), BlockNumber: testInt64(100), BlockTimeStamp: testInt64(testTronTimestamp), Logs: []TronLog{testTronLog()}}
	receipt.Receipt.Result = "SUCCESS"
	return receipt
}

type testTronSource struct {
	genesis string
	receipt TronReceipt
	head    TronBlock
	block   TronBlock
	fail    string
	calls   []string
}

func testTronEvidenceSource() *testTronSource {
	return &testTronSource{genesis: testTronGenesis(), receipt: testTronReceipt(), head: testTronBlock(110), block: testTronBlock(100, testTronHash())}
}

func (source *testTronSource) step(ctx context.Context, name string) error {
	source.calls = append(source.calls, name)
	if ctx.Err() != nil || source.fail == name {
		return ErrChainUnavailable
	}
	return nil
}

func (source *testTronSource) GenesisID(ctx context.Context) (string, error) {
	return source.genesis, source.step(ctx, "genesis")
}
func (source *testTronSource) SolidReceipt(ctx context.Context, hash string) (TronReceipt, error) {
	return source.receipt, source.step(ctx, "receipt")
}
func (source *testTronSource) SolidHead(ctx context.Context) (TronBlock, error) {
	return source.head, source.step(ctx, "head")
}
func (source *testTronSource) SolidBlock(ctx context.Context, height int64) (TronBlock, error) {
	return source.block, source.step(ctx, "block")
}

func testTronVerifier(t testing.TB, source TronEvidenceSource) *TronVerifier {
	t.Helper()
	verifier, err := NewTronVerifier(TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), Contract: testTronContract(), Decimals: 6}, source)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func testTronExpected() TronPaymentExpectation {
	timestamp := time.UnixMilli(testTronTimestamp).UTC()
	return TronPaymentExpectation{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), TxHash: testTronHash(), Contract: testTronContract(), Recipient: testTronRecipient, Units: 20010002, NotBefore: timestamp.Add(-time.Minute), ExpiresAt: timestamp.Add(time.Minute)}
}

func TestCanonicalTronAddressChecksChecksum(t *testing.T) {
	for _, address := range []string{testTronRecipient, testTronRecipientHex(), strings.ToUpper("41" + strings.Repeat("ab", 20))} {
		canonical, err := canonicalTronAddress(address)
		if err != nil || len(canonical) != 42 || canonical != strings.ToLower(canonical) {
			t.Fatal("valid address was not normalized")
		}
	}
	if canonical, err := canonicalTronAddress(testTronRecipient); err != nil || canonical != testTronRecipientHex() {
		t.Fatal("Base58Check vector mismatch")
	}
	for _, address := range []string{"", testTronRecipient[:33] + "F", "T" + strings.Repeat("0", 33), "0x" + strings.Repeat("22", 20), "42" + strings.Repeat("22", 20), "41" + strings.Repeat("g", 40), " " + testTronRecipient} {
		if _, err := canonicalTronAddress(address); err == nil {
			t.Fatal("invalid address accepted")
		}
	}
	if _, err := tronTopicAddress("01" + strings.Repeat("0", 62)); err == nil {
		t.Fatal("nonzero ABI address padding accepted")
	}
}

func TestSolidTronReceiptAndInternalTransferLogs(t *testing.T) {
	source := testTronEvidenceSource()
	source.receipt.Logs = append([]TronLog{{Address: strings.Repeat("44", 20)}, {Address: strings.Repeat("33", 20), Topics: []string{strings.Repeat("aa", 32)}}}, source.receipt.Logs...)
	evidence, err := testTronVerifier(t, source).ReadSolidTransaction(context.Background(), strings.ToUpper(testTronHash()))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.BlockNumber != 100 || evidence.TxHash != testTronHash() || evidence.GenesisID != testTronGenesis() || len(evidence.ReceiptHash) != 64 || len(evidence.Transfers) != 1 || evidence.Transfers[0].EventIndex != 2 {
		t.Fatal("incorrect solidified evidence or original log index lost")
	}
	transfer, err := MatchTronPayment(evidence, testTronExpected())
	if err != nil || transfer.Amount != "20010002" || transfer.To != testTronRecipientHex() {
		t.Fatal("valid Transfer log failed exact matching")
	}
	if strings.Join(source.calls, ",") != "genesis,receipt,head,block" {
		t.Fatal("receipt proof did not use independent chain and block checks")
	}
}

func TestTronVerifierRejectsMissingFailedAndInconsistentEvidence(t *testing.T) {
	for _, example := range []struct {
		name   string
		mutate func(*testTronSource)
		err    error
	}{
		{"wrong genesis", func(source *testTronSource) { source.genesis = strings.Repeat("01", 32) }, ErrWrongChain},
		{"invalid genesis hash", func(source *testTronSource) { source.genesis = "not-a-hash" }, ErrWrongChain},
		{"missing receipt", func(source *testTronSource) { source.receipt = TronReceipt{} }, ErrNotSolidified},
		{"wrong transaction", func(source *testTronSource) { source.receipt.ID = strings.Repeat("bb", 32) }, ErrInvalidChainEvidence},
		{"missing block number", func(source *testTronSource) { source.receipt.BlockNumber = nil }, ErrInvalidChainEvidence},
		{"negative block", func(source *testTronSource) { source.receipt.BlockNumber = testInt64(-1) }, ErrInvalidChainEvidence},
		{"missing timestamp", func(source *testTronSource) { source.receipt.BlockTimeStamp = nil }, ErrInvalidChainEvidence},
		{"missing execution result", func(source *testTronSource) { source.receipt.Receipt.Result = "" }, ErrInvalidChainEvidence},
		{"reverted", func(source *testTronSource) { source.receipt.Receipt.Result = "REVERT" }, ErrExecutionFailed},
		{"out of energy", func(source *testTronSource) { source.receipt.Receipt.Result = "OUT_OF_ENERGY" }, ErrExecutionFailed},
		{"failed top result", func(source *testTronSource) { source.receipt.Result = "FAILED" }, ErrExecutionFailed},
		{"unknown top result", func(source *testTronSource) { source.receipt.Result = "SUCCESS" }, ErrInvalidChainEvidence},
		{"not at solid head", func(source *testTronSource) { source.head = testTronBlock(99) }, ErrNotSolidified},
		{"wrong block height", func(source *testTronSource) { source.block = testTronBlock(101, testTronHash()) }, ErrInvalidChainEvidence},
		{"same height head fork", func(source *testTronSource) {
			source.head = testTronBlock(100)
			source.head.ID = source.head.ID[:63] + "f"
		}, ErrInvalidChainEvidence},
		{"adjacent head fork", func(source *testTronSource) {
			source.head = testTronBlock(101)
			source.head.Header.RawData.ParentHash = source.head.Header.RawData.ParentHash[:63] + "f"
		}, ErrInvalidChainEvidence},
		{"missing block header height", func(source *testTronSource) { source.block.Header.RawData.Number = nil }, ErrInvalidChainEvidence},
		{"block id height mismatch", func(source *testTronSource) { source.block.ID = testTronBlockID(99) }, ErrInvalidChainEvidence},
		{"parent height mismatch", func(source *testTronSource) { source.block.Header.RawData.ParentHash = testTronBlockID(90) }, ErrInvalidChainEvidence},
		{"receipt block time mismatch", func(source *testTronSource) { source.block.Header.RawData.Timestamp = testInt64(testTronTimestamp + 1) }, ErrInvalidChainEvidence},
		{"stale head time", func(source *testTronSource) { source.head.Header.RawData.Timestamp = testInt64(testTronTimestamp - 1) }, ErrInvalidChainEvidence},
		{"transaction missing from block", func(source *testTronSource) { source.block.Transactions = nil }, ErrInvalidChainEvidence},
		{"duplicate block transaction", func(source *testTronSource) { source.block = testTronBlock(100, testTronHash(), testTronHash()) }, ErrInvalidChainEvidence},
		{"invalid log address", func(source *testTronSource) { source.receipt.Logs[0].Address = "bad" }, ErrInvalidChainEvidence},
		{"missing indexed recipient", func(source *testTronSource) { source.receipt.Logs[0].Topics = source.receipt.Logs[0].Topics[:2] }, ErrInvalidChainEvidence},
		{"bad indexed padding", func(source *testTronSource) { source.receipt.Logs[0].Topics[2] = "01" + strings.Repeat("0", 62) }, ErrInvalidChainEvidence},
		{"invalid uint256", func(source *testTronSource) { source.receipt.Logs[0].Data = "01" }, ErrInvalidChainEvidence},
	} {
		t.Run(example.name, func(t *testing.T) {
			source := testTronEvidenceSource()
			example.mutate(source)
			if _, err := testTronVerifier(t, source).ReadSolidTransaction(context.Background(), testTronHash()); !errors.Is(err, example.err) {
				t.Fatalf("got %v, want %v", err, example.err)
			}
		})
	}
	for _, step := range []string{"genesis", "receipt", "head", "block"} {
		source := testTronEvidenceSource()
		source.fail = step
		if _, err := testTronVerifier(t, source).ReadSolidTransaction(context.Background(), testTronHash()); !errors.Is(err, ErrChainUnavailable) {
			t.Fatal("RPC failure was not propagated")
		}
	}
	source := testTronEvidenceSource()
	source.receipt.Result = "SUCESS"
	if _, err := testTronVerifier(t, source).ReadSolidTransaction(context.Background(), testTronHash()); err != nil {
		t.Fatal("explicit protobuf SUCESS rejected")
	}
}

func TestTronPaymentMatchingPreservesAmbiguousAndLateEvidence(t *testing.T) {
	for _, example := range []struct {
		name   string
		mutate func(*testTronSource)
		err    error
	}{
		{"wrong contract", func(source *testTronSource) { source.receipt.Logs[0].Address = strings.Repeat("44", 20) }, ErrTransferMismatch},
		{"wrong recipient", func(source *testTronSource) {
			source.receipt.Logs[0].Topics[2] = strings.Repeat("0", 24) + strings.Repeat("44", 20)
		}, ErrTransferMismatch},
		{"underpaid", func(source *testTronSource) { source.receipt.Logs[0].Data = fmt.Sprintf("%064x", 20010001) }, ErrTransferMismatch},
		{"overpaid", func(source *testTronSource) { source.receipt.Logs[0].Data = fmt.Sprintf("%064x", 20010003) }, ErrTransferMismatch},
		{"huge uint256", func(source *testTronSource) { source.receipt.Logs[0].Data = strings.Repeat("f", 64) }, ErrTransferMismatch},
		{"zero transfer", func(source *testTronSource) { source.receipt.Logs[0].Data = strings.Repeat("0", 64) }, ErrTransferMismatch},
		{"multiple incoming events", func(source *testTronSource) { source.receipt.Logs = append(source.receipt.Logs, testTronLog()) }, ErrAmbiguousTransfer},
		{"self transfer", func(source *testTronSource) { source.receipt.Logs[0].Topics[1] = source.receipt.Logs[0].Topics[2] }, ErrAmbiguousTransfer},
		{"incoming and outgoing", func(source *testTronSource) {
			outbound := testTronLog()
			outbound.Topics[1], outbound.Topics[2] = outbound.Topics[2], outbound.Topics[1]
			source.receipt.Logs = append(source.receipt.Logs, outbound)
		}, ErrAmbiguousTransfer},
	} {
		t.Run(example.name, func(t *testing.T) {
			source := testTronEvidenceSource()
			example.mutate(source)
			evidence, err := testTronVerifier(t, source).ReadSolidTransaction(context.Background(), testTronHash())
			if err != nil {
				t.Fatal("chain evidence lost instead of being available for review")
			}
			if _, err := MatchTronPayment(evidence, testTronExpected()); !errors.Is(err, example.err) {
				t.Fatalf("got %v, want %v", err, example.err)
			}
		})
	}
	evidence, err := testTronVerifier(t, testTronEvidenceSource()).ReadSolidTransaction(context.Background(), testTronHash())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*TronPaymentExpectation){
		func(expected *TronPaymentExpectation) { expected.NotBefore = evidence.BlockTime.Add(time.Second) },
		func(expected *TronPaymentExpectation) { expected.ExpiresAt = evidence.BlockTime.Add(-time.Second) },
	} {
		expected := testTronExpected()
		mutate(&expected)
		if transfer, err := MatchTronPayment(evidence, expected); !errors.Is(err, ErrPaymentWindow) || transfer.Amount != "20010002" {
			t.Fatal("out-of-window transfer discarded or accepted")
		}
	}
	expected := testTronExpected()
	expected.NotBefore = evidence.BlockTime
	if _, err := MatchTronPayment(evidence, expected); err != nil {
		t.Fatal("inclusive payment window start rejected")
	}
	expected = testTronExpected()
	expected.ExpiresAt = evidence.BlockTime
	if _, err := MatchTronPayment(evidence, expected); err != nil {
		t.Fatal("inclusive payment window end rejected")
	}
}

func TestTronVerifierAndExpectationConfiguration(t *testing.T) {
	valid := TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), Contract: testTronContract(), Decimals: 6}
	for _, mutate := range []func(*TronVerifierConfig){
		func(config *TronVerifierConfig) { config.Environment = "mainnet" },
		func(config *TronVerifierConfig) { config.GenesisID = "" },
		func(config *TronVerifierConfig) { config.GenesisID = strings.Repeat("0", 64) },
		func(config *TronVerifierConfig) { config.GenesisID = testTronHash() },
		func(config *TronVerifierConfig) { config.Contract = "not-verified" },
		func(config *TronVerifierConfig) { config.Contract = "41" + strings.Repeat("0", 40) },
		func(config *TronVerifierConfig) { config.Decimals = 18 },
	} {
		config := valid
		mutate(&config)
		if _, err := NewTronVerifier(config, testTronEvidenceSource()); err == nil {
			t.Fatal("unsafe verifier configuration accepted")
		}
	}
	if _, err := NewTronVerifier(valid, nil); err == nil {
		t.Fatal("missing independent source accepted")
	}
	if _, err := testTronVerifier(t, testTronEvidenceSource()).ReadSolidTransaction(context.Background(), "0x"+testTronHash()); err == nil {
		t.Fatal("invalid tx hash accepted")
	}
	evidence, err := testTronVerifier(t, testTronEvidenceSource()).ReadSolidTransaction(context.Background(), testTronHash())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*TronPaymentExpectation){
		func(expected *TronPaymentExpectation) { expected.Environment = "mainnet" },
		func(expected *TronPaymentExpectation) { expected.GenesisID = testTronHash() },
		func(expected *TronPaymentExpectation) { expected.TxHash = strings.Repeat("bb", 32) },
		func(expected *TronPaymentExpectation) { expected.Units = 0 },
		func(expected *TronPaymentExpectation) { expected.Units = maxPaymentUnits + 1 },
		func(expected *TronPaymentExpectation) { expected.NotBefore = time.Time{} },
		func(expected *TronPaymentExpectation) { expected.Recipient = "41" + strings.Repeat("0", 40) },
	} {
		expected := testTronExpected()
		mutate(&expected)
		if _, err := MatchTronPayment(evidence, expected); !errors.Is(err, ErrTransferMismatch) {
			t.Fatal("invalid expectation accepted")
		}
	}
}
