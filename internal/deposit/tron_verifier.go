package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const TronEnvironmentNile = "nile"
const tronTransferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
const maxPaymentUnits = uint64(1000000000000)

var ErrInvalidChainEvidence = errors.New("invalid TRON chain evidence")
var ErrChainUnavailable = errors.New("TRON evidence source unavailable")
var ErrNotSolidified = errors.New("TRON solidified evidence not available yet")
var ErrWrongChain = errors.New("TRON chain environment does not match")
var ErrExecutionFailed = errors.New("TRON contract execution failed")
var ErrTransferMismatch = errors.New("TRON transfer does not match the saved payment")
var ErrAmbiguousTransfer = errors.New("TRON transfers require manual review")
var ErrPaymentWindow = errors.New("TRON transfer is outside the saved payment window")

type TronLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type TronReceipt struct {
	ID             string `json:"id"`
	BlockNumber    *int64 `json:"blockNumber"`
	BlockTimeStamp *int64 `json:"blockTimeStamp"`
	Result         string `json:"result"`
	Receipt        struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Logs []TronLog `json:"log"`
}

type TronBlock struct {
	ID     string `json:"blockID"`
	Header struct {
		RawData struct {
			Number     *int64 `json:"number"`
			Timestamp  *int64 `json:"timestamp"`
			ParentHash string `json:"parentHash"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []TronBlockTransaction `json:"transactions"`
}

type TronBlockTransaction struct {
	TxID    string                  `json:"txID"`
	Ret     []TronExecutionResult   `json:"ret,omitempty"`
	RawData *TronTransactionRawData `json:"raw_data,omitempty"`
}

type TronExecutionResult struct {
	ContractRet string `json:"contractRet"`
	Ret         string `json:"ret,omitempty"`
}

type TronTransactionRawData struct {
	Contracts []TronContractType `json:"contract"`
}

type TronContractType struct {
	Type string `json:"type"`
}

type TronEvidenceSource interface {
	GenesisID(context.Context) (string, error)
	SolidHead(context.Context) (TronBlock, error)
	SolidBlock(context.Context, int64) (TronBlock, error)
	SolidReceipt(context.Context, string) (TronReceipt, error)
}

type TronVerifierConfig struct {
	Environment string
	GenesisID   string
	Contract    string
	Decimals    int
}

type TronVerifier struct {
	config TronVerifierConfig
	source TronEvidenceSource
}

type TronTransfer struct {
	EventIndex int
	Contract   string
	From       string
	To         string
	Amount     string
}

type TronTransactionEvidence struct {
	Disposition string `json:",omitempty"`
	Environment string
	GenesisID   string
	TxHash      string
	BlockNumber int64
	BlockID     string
	BlockTime   time.Time
	ReceiptHash string
	ReceiptJSON []byte
	Transfers   []TronTransfer
}

type TronPaymentExpectation struct {
	Environment string
	GenesisID   string
	TxHash      string
	Contract    string
	Recipient   string
	Units       uint64
	NotBefore   time.Time
	ExpiresAt   time.Time
}

func canonicalTronHash(raw string) (string, error) {
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return "", ErrInvalidChainEvidence
	}
	return hex.EncodeToString(decoded), nil
}

func tronBlockHeight(block TronBlock) (int64, error) {
	id, err := hex.DecodeString(block.ID)
	if err != nil || len(id) != 32 {
		return 0, ErrInvalidChainEvidence
	}
	encodedHeight := binary.BigEndian.Uint64(id[:8])
	if encodedHeight > uint64(^uint64(0)>>1) {
		return 0, ErrInvalidChainEvidence
	}
	height := int64(encodedHeight)
	if block.Header.RawData.Number == nil {
		if height != 0 {
			return 0, ErrInvalidChainEvidence
		}
	} else if *block.Header.RawData.Number != height {
		return 0, ErrInvalidChainEvidence
	}
	if height > 0 {
		parent, parentErr := hex.DecodeString(block.Header.RawData.ParentHash)
		if parentErr != nil || len(parent) != 32 || binary.BigEndian.Uint64(parent[:8]) != uint64(height-1) || block.Header.RawData.Timestamp == nil || *block.Header.RawData.Timestamp <= 0 {
			return 0, ErrInvalidChainEvidence
		}
	}
	seen := map[string]struct{}{}
	for _, transaction := range block.Transactions {
		hash, err := canonicalTronHash(transaction.TxID)
		if err != nil {
			return 0, err
		}
		if _, exists := seen[hash]; exists {
			return 0, ErrInvalidChainEvidence
		}
		seen[hash] = struct{}{}
	}
	return height, nil
}

func NewTronVerifier(config TronVerifierConfig, source TronEvidenceSource) (*TronVerifier, error) {
	genesis, err := canonicalTronHash(config.GenesisID)
	if err != nil || !strings.HasPrefix(genesis, "0000000000000000") || genesis == strings.Repeat("0", 64) || config.Environment != TronEnvironmentNile || config.Decimals != 6 || source == nil {
		return nil, errors.New("verified Nile genesis and six-decimal token configuration are required")
	}
	contract, err := canonicalTronAddress(config.Contract)
	if err != nil || contract == "41"+strings.Repeat("0", 40) {
		return nil, errors.New("verified Nile token contract is required")
	}
	config.GenesisID = genesis
	config.Contract = contract
	return &TronVerifier{config: config, source: source}, nil
}

func (verifier *TronVerifier) ReadSolidTransaction(ctx context.Context, rawHash string) (TronTransactionEvidence, error) {
	hash, err := canonicalTronHash(rawHash)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	genesis, err := verifier.source.GenesisID(ctx)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	genesis, err = canonicalTronHash(genesis)
	if err != nil || genesis != verifier.config.GenesisID {
		return TronTransactionEvidence{}, ErrWrongChain
	}
	receipt, err := verifier.source.SolidReceipt(ctx, hash)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	if receipt.ID == "" {
		return TronTransactionEvidence{}, ErrNotSolidified
	}
	receiptID, err := canonicalTronHash(receipt.ID)
	if err != nil || receiptID != hash || receipt.BlockNumber == nil || *receipt.BlockNumber <= 0 || receipt.BlockTimeStamp == nil || *receipt.BlockTimeStamp <= 0 {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	if receipt.Result == "FAILED" || receipt.Receipt.Result != "" && receipt.Receipt.Result != "SUCCESS" {
		return TronTransactionEvidence{}, ErrExecutionFailed
	}
	if receipt.Receipt.Result != "SUCCESS" || receipt.Result != "" && receipt.Result != "SUCESS" {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	head, err := verifier.source.SolidHead(ctx)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	headHeight, err := tronBlockHeight(head)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	if *receipt.BlockNumber > headHeight {
		return TronTransactionEvidence{}, ErrNotSolidified
	}
	if head.Header.RawData.Timestamp == nil || *head.Header.RawData.Timestamp < *receipt.BlockTimeStamp {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	block, err := verifier.source.SolidBlock(ctx, *receipt.BlockNumber)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	height, err := tronBlockHeight(block)
	if err != nil || height != *receipt.BlockNumber || block.Header.RawData.Timestamp == nil || *block.Header.RawData.Timestamp != *receipt.BlockTimeStamp {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	if height == headHeight && !strings.EqualFold(block.ID, head.ID) || height == headHeight-1 && !strings.EqualFold(block.ID, head.Header.RawData.ParentHash) {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	found := false
	for _, transaction := range block.Transactions {
		if strings.EqualFold(transaction.TxID, hash) {
			found = true
		}
	}
	if !found {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	transfers, err := extractTronTransfers(receipt.Logs, verifier.config.Contract)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	digest := sha256.Sum256(encoded)
	return TronTransactionEvidence{
		Environment: verifier.config.Environment, GenesisID: genesis, TxHash: hash, BlockNumber: height,
		BlockID: strings.ToLower(block.ID), BlockTime: time.UnixMilli(*receipt.BlockTimeStamp).UTC(),
		ReceiptHash: hex.EncodeToString(digest[:]), ReceiptJSON: encoded, Transfers: transfers,
	}, nil
}

func extractTronTransfers(logs []TronLog, contract string) ([]TronTransfer, error) {
	var transfers []TronTransfer
	for index, entry := range logs {
		address, err := hex.DecodeString(entry.Address)
		if err != nil || len(address) != 20 {
			return nil, ErrInvalidChainEvidence
		}
		if "41"+hex.EncodeToString(address) != contract || len(entry.Topics) == 0 || !strings.EqualFold(entry.Topics[0], tronTransferTopic) {
			continue
		}
		if len(entry.Topics) != 3 {
			return nil, ErrInvalidChainEvidence
		}
		from, fromErr := tronTopicAddress(entry.Topics[1])
		to, toErr := tronTopicAddress(entry.Topics[2])
		amount, amountErr := hex.DecodeString(entry.Data)
		if fromErr != nil || toErr != nil || amountErr != nil || len(amount) != 32 {
			return nil, ErrInvalidChainEvidence
		}
		transfers = append(transfers, TronTransfer{EventIndex: index, Contract: contract, From: from, To: to, Amount: new(big.Int).SetBytes(amount).String()})
	}
	return transfers, nil
}

func MatchTronPayment(evidence TronTransactionEvidence, expected TronPaymentExpectation) (TronTransfer, error) {
	if evidence.Disposition != "" && evidence.Disposition != tronContractSuccess {
		return TronTransfer{}, ErrTransferMismatch
	}
	hash, hashErr := canonicalTronHash(expected.TxHash)
	genesis, genesisErr := canonicalTronHash(expected.GenesisID)
	contract, contractErr := canonicalTronAddress(expected.Contract)
	recipient, recipientErr := canonicalTronAddress(expected.Recipient)
	if hashErr != nil || genesisErr != nil || contractErr != nil || recipientErr != nil || expected.Environment != TronEnvironmentNile || evidence.Environment != expected.Environment || evidence.GenesisID != genesis || evidence.TxHash != hash || expected.Units == 0 || expected.Units > maxPaymentUnits || expected.NotBefore.IsZero() || !expected.ExpiresAt.After(expected.NotBefore) || recipient == "41"+strings.Repeat("0", 40) {
		return TronTransfer{}, ErrTransferMismatch
	}
	var incoming []TronTransfer
	for _, transfer := range evidence.Transfers {
		if transfer.Contract != contract {
			continue
		}
		if transfer.From == recipient && transfer.Amount != "0" {
			return TronTransfer{}, ErrAmbiguousTransfer
		}
		if transfer.To == recipient && transfer.Amount != "0" {
			incoming = append(incoming, transfer)
		}
	}
	if len(incoming) > 1 {
		return TronTransfer{}, ErrAmbiguousTransfer
	}
	if len(incoming) != 1 || incoming[0].Amount != strconv.FormatUint(expected.Units, 10) {
		return TronTransfer{}, ErrTransferMismatch
	}
	if evidence.BlockTime.Before(expected.NotBefore) || evidence.BlockTime.After(expected.ExpiresAt) {
		return incoming[0], ErrPaymentWindow
	}
	return incoming[0], nil
}
