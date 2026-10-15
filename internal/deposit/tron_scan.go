package deposit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

const tronContractSuccess = "contract_success"
const tronNativeSuccess = "native_success"
const tronContractFailed = "contract_failed"

var ErrScanCaughtUp = errors.New("chain scan is at the solidified head")
var ErrUnsupportedTronTransaction = errors.New("TRON transaction requires a reviewed evidence policy")

type TronScanSource interface {
	TronEvidenceSource
	SourceID() string
}

type TronScanObservation struct {
	Source string
	At     time.Time
	Head   TronBlock
}

type TronBlockReader struct {
	config TronVerifierConfig
	source TronScanSource
}

func NewTronBlockReader(config TronVerifierConfig, source TronScanSource) (*TronBlockReader, error) {
	verifier, err := NewTronVerifier(config, source)
	if err != nil {
		return nil, err
	}
	if source.SourceID() == "" || len(source.SourceID()) > 512 || strings.ContainsAny(source.SourceID(), "\r\n") {
		return nil, ErrInvalidChainEvidence
	}
	return &TronBlockReader{config: verifier.config, source: source}, nil
}

func normalizeScanBlock(block TronBlock) (TronBlock, int64, error) {
	height, err := tronBlockHeight(block)
	if err != nil {
		return TronBlock{}, 0, err
	}
	block.ID = strings.ToLower(block.ID)
	block.Header.RawData.ParentHash = strings.ToLower(block.Header.RawData.ParentHash)
	if block.Header.RawData.Number != nil {
		number := *block.Header.RawData.Number
		block.Header.RawData.Number = &number
	}
	if block.Header.RawData.Timestamp != nil {
		timestamp := *block.Header.RawData.Timestamp
		block.Header.RawData.Timestamp = &timestamp
	}
	block.Transactions = append([]TronBlockTransaction(nil), block.Transactions...)
	for index := range block.Transactions {
		block.Transactions[index].TxID = strings.ToLower(block.Transactions[index].TxID)
		block.Transactions[index].Ret = append([]TronExecutionResult(nil), block.Transactions[index].Ret...)
		for resultIndex := range block.Transactions[index].Ret {
			if block.Transactions[index].Ret[resultIndex].Ret == "SUCESS" {
				block.Transactions[index].Ret[resultIndex].Ret = ""
			}
		}
		if block.Transactions[index].RawData != nil {
			data := *block.Transactions[index].RawData
			data.Contracts = append([]TronContractType(nil), data.Contracts...)
			block.Transactions[index].RawData = &data
		}
	}
	return block, height, nil
}

func classifyScanReceipt(transaction TronBlockTransaction, receipt TronReceipt, contract string) (string, []TronTransfer, error) {
	if transaction.RawData == nil || len(transaction.RawData.Contracts) != 1 || len(transaction.Ret) != 1 {
		return "", nil, ErrInvalidChainEvidence
	}
	result := transaction.Ret[0]
	switch transaction.RawData.Contracts[0].Type {
	// These reviewed native account, staking and governance operations do not
	// execute TVM code. Require explicit block SUCCESS and no execution failure
	// or logs in TransactionInfo; never derive TRC20 payment evidence from them.
	// UpdateBrokerageContract only updates witness brokerage; reviewed Nile
	// block 71809560 confirms the native success receipt without TVM logs.
	case "TransferContract", "TransferAssetContract", "DelegateResourceContract", "UnDelegateResourceContract", "FreezeBalanceV2Contract", "UnfreezeBalanceV2Contract", "VoteWitnessContract", "AccountCreateContract", "WithdrawBalanceContract", "WithdrawExpireUnfreezeContract", "AccountPermissionUpdateContract", "UpdateBrokerageContract":
		if result.ContractRet != "SUCCESS" || result.Ret != "" && result.Ret != "SUCESS" || receipt.Result != "" && receipt.Result != "SUCESS" || receipt.Receipt.Result != "" && receipt.Receipt.Result != "DEFAULT" && receipt.Receipt.Result != "SUCCESS" || len(receipt.Logs) != 0 {
			return "", nil, ErrInvalidChainEvidence
		}
		return tronNativeSuccess, nil, nil
	case "TriggerSmartContract", "CreateSmartContract":
		if result.ContractRet == "SUCCESS" {
			if result.Ret != "" && result.Ret != "SUCESS" || receipt.Result != "" && receipt.Result != "SUCESS" || receipt.Receipt.Result != "SUCCESS" {
				return "", nil, ErrInvalidChainEvidence
			}
			transfers, err := extractTronTransfers(receipt.Logs, contract)
			return tronContractSuccess, transfers, err
		}
		switch result.ContractRet {
		case "REVERT", "BAD_JUMP_DESTINATION", "OUT_OF_MEMORY", "PRECOMPILED_CONTRACT", "STACK_TOO_SMALL", "STACK_TOO_LARGE", "ILLEGAL_OPERATION", "STACK_OVERFLOW", "OUT_OF_ENERGY", "OUT_OF_TIME", "JVM_STACK_OVER_FLOW", "TRANSFER_FAILED", "INVALID_CODE":
			if result.Ret != "" && result.Ret != "SUCESS" && result.Ret != "FAILED" || receipt.Result != "FAILED" || receipt.Receipt.Result != result.ContractRet || len(receipt.Logs) != 0 {
				return "", nil, ErrInvalidChainEvidence
			}
			return tronContractFailed, nil, nil
		default:
			return "", nil, ErrInvalidChainEvidence
		}
	default:
		return "", nil, ErrUnsupportedTronTransaction
	}
}

func scanTransactionEvidence(config TronVerifierConfig, block TronBlock, transaction TronBlockTransaction, receipt TronReceipt) (TronTransactionEvidence, error) {
	height, err := tronBlockHeight(block)
	hash, hashErr := canonicalTronHash(receipt.ID)
	if err != nil || hashErr != nil || height <= 0 || hash != transaction.TxID || receipt.BlockNumber == nil || *receipt.BlockNumber != height || receipt.BlockTimeStamp == nil || *receipt.BlockTimeStamp != *block.Header.RawData.Timestamp {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	receipt.ID = hash
	disposition, transfers, err := classifyScanReceipt(transaction, receipt, config.Contract)
	if err != nil {
		return TronTransactionEvidence{}, err
	}
	if receipt.Result == "SUCESS" {
		receipt.Result = ""
	}
	if receipt.Receipt.Result == "DEFAULT" {
		receipt.Receipt.Result = ""
	}
	if len(receipt.Logs) == 0 {
		receipt.Logs = nil
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > maxTronResponseBytes {
		return TronTransactionEvidence{}, ErrInvalidChainEvidence
	}
	digest := sha256.Sum256(encoded)
	return TronTransactionEvidence{Disposition: disposition, Environment: config.Environment, GenesisID: config.GenesisID, TxHash: hash, BlockNumber: height, BlockID: block.ID, BlockTime: time.UnixMilli(*receipt.BlockTimeStamp).UTC(), ReceiptHash: hex.EncodeToString(digest[:]), ReceiptJSON: encoded, Transfers: transfers}, nil
}

func (reader *TronBlockReader) ReadNext(ctx context.Context, checkpoint ScanCheckpoint) (SolidBlockBatch, error) {
	checkpointHash, err := canonicalTronHash(checkpoint.BlockID)
	if err != nil || checkpoint.Height < 0 || checkpointHash != checkpoint.BlockID || checkpoint.Height == 0 && checkpointHash != reader.config.GenesisID {
		return SolidBlockBatch{}, ErrScanConflict
	}
	decoded, _ := hex.DecodeString(checkpointHash)
	if binary.BigEndian.Uint64(decoded[:8]) != uint64(checkpoint.Height) {
		return SolidBlockBatch{}, ErrScanConflict
	}
	genesis, err := reader.source.GenesisID(ctx)
	if err != nil {
		return SolidBlockBatch{}, err
	}
	genesis, err = canonicalTronHash(genesis)
	if err != nil || genesis != reader.config.GenesisID {
		return SolidBlockBatch{}, ErrWrongChain
	}
	head, err := reader.source.SolidHead(ctx)
	if err != nil {
		return SolidBlockBatch{}, err
	}
	head, headHeight, err := normalizeScanBlock(head)
	if err != nil || checkpoint.Height < 0 {
		return SolidBlockBatch{}, ErrInvalidChainEvidence
	}
	if headHeight < checkpoint.Height {
		return SolidBlockBatch{}, ErrNotSolidified
	}
	if headHeight == checkpoint.Height {
		if head.ID != checkpoint.BlockID {
			return SolidBlockBatch{}, ErrScanConflict
		}
		return SolidBlockBatch{}, ErrScanCaughtUp
	}
	anchor, err := reader.source.SolidBlock(ctx, checkpoint.Height)
	if err != nil {
		return SolidBlockBatch{}, err
	}
	anchor, anchorHeight, err := normalizeScanBlock(anchor)
	if err != nil || anchorHeight != checkpoint.Height || anchor.ID != checkpoint.BlockID {
		return SolidBlockBatch{}, ErrScanConflict
	}
	block, err := reader.source.SolidBlock(ctx, checkpoint.Height+1)
	if err != nil {
		return SolidBlockBatch{}, err
	}
	block, height, err := normalizeScanBlock(block)
	if err != nil || height != checkpoint.Height+1 || block.Header.RawData.ParentHash != anchor.ID || headHeight == height && head.ID != block.ID || headHeight == height+1 && head.Header.RawData.ParentHash != block.ID || head.Header.RawData.Timestamp == nil || *head.Header.RawData.Timestamp < *block.Header.RawData.Timestamp || anchor.Header.RawData.Timestamp != nil && *anchor.Header.RawData.Timestamp > *block.Header.RawData.Timestamp {
		return SolidBlockBatch{}, ErrInvalidChainEvidence
	}
	head.Transactions = nil
	batch := SolidBlockBatch{Version: 2, Block: block, Observation: &TronScanObservation{Source: reader.source.SourceID(), Head: head}}
	var receipts map[string]TronReceipt
	if source, ok := reader.source.(interface {
		SolidReceipts(context.Context, int64) ([]TronReceipt, error)
	}); ok && len(block.Transactions) > 0 {
		all, err := source.SolidReceipts(ctx, height)
		if err != nil {
			return SolidBlockBatch{}, err
		}
		if len(all) != len(block.Transactions) {
			return SolidBlockBatch{}, ErrInvalidChainEvidence
		}
		receipts = make(map[string]TronReceipt, len(all))
		for _, receipt := range all {
			hash, err := canonicalTronHash(receipt.ID)
			if err != nil {
				return SolidBlockBatch{}, err
			}
			if _, exists := receipts[hash]; exists {
				return SolidBlockBatch{}, ErrInvalidChainEvidence
			}
			receipts[hash] = receipt
		}
	}
	for _, transaction := range block.Transactions {
		var receipt TronReceipt
		if receipts != nil {
			var exists bool
			receipt, exists = receipts[transaction.TxID]
			if !exists {
				return SolidBlockBatch{}, ErrInvalidChainEvidence
			}
		} else {
			var err error
			receipt, err = reader.source.SolidReceipt(ctx, transaction.TxID)
			if err != nil {
				return SolidBlockBatch{}, err
			}
		}
		evidence, err := scanTransactionEvidence(reader.config, block, transaction, receipt)
		if err != nil {
			return SolidBlockBatch{}, err
		}
		batch.Transactions = append(batch.Transactions, evidence)
	}
	batch.Observation.At = time.Now().UTC()
	if _, _, _, err := prepareTronScanBatch(reader.config, batch); err != nil {
		return SolidBlockBatch{}, err
	}
	return batch, nil
}

func prepareTronScanBatch(config TronVerifierConfig, batch SolidBlockBatch) ([]byte, string, int64, error) {
	block, height, err := normalizeScanBlock(batch.Block)
	if err != nil || height <= 0 || batch.Version != 2 || !reflect.DeepEqual(block, batch.Block) || len(batch.Transactions) != len(block.Transactions) || batch.Observation == nil || batch.Observation.Source == "" || len(batch.Observation.Source) > 512 || strings.ContainsAny(batch.Observation.Source, "\r\n") || batch.Observation.At.IsZero() {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	head, headHeight, err := normalizeScanBlock(batch.Observation.Head)
	if err != nil || headHeight < height || len(head.Transactions) != 0 || head.Header.RawData.Timestamp == nil || *head.Header.RawData.Timestamp < *block.Header.RawData.Timestamp || headHeight == height && head.ID != block.ID || headHeight == height+1 && head.Header.RawData.ParentHash != block.ID {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	for index, evidence := range batch.Transactions {
		if len(evidence.ReceiptJSON) > maxTronResponseBytes {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		var receipt TronReceipt
		if json.Unmarshal(evidence.ReceiptJSON, &receipt) != nil {
			return nil, "", 0, ErrInvalidChainEvidence
		}
		expected, err := scanTransactionEvidence(config, block, block.Transactions[index], receipt)
		if err != nil {
			return nil, "", 0, err
		}
		if !reflect.DeepEqual(expected, evidence) {
			return nil, "", 0, ErrInvalidChainEvidence
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > 16<<20 {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	core := batch
	core.Observation = nil
	canonical, err := json.Marshal(core)
	if err != nil {
		return nil, "", 0, ErrInvalidChainEvidence
	}
	digest := sha256.Sum256(canonical)
	return encoded, hex.EncodeToString(digest[:]), height, nil
}

func sameScanCore(first, second SolidBlockBatch) bool {
	first.Observation, second.Observation = nil, nil
	firstJSON, firstErr := json.Marshal(first)
	secondJSON, secondErr := json.Marshal(second)
	return firstErr == nil && secondErr == nil && bytes.Equal(firstJSON, secondJSON)
}
