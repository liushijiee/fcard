package deposit

import (
	"context"
	"errors"
	"time"
)

type PreflightResult struct {
	Environment      string    `json:"environment"`
	GenesisID        string    `json:"genesis_id"`
	Contract         string    `json:"contract"`
	CheckpointHeight int64     `json:"checkpoint_height"`
	CheckpointID     string    `json:"checkpoint_id"`
	HeadHeight       int64     `json:"head_height"`
	HeadID           string    `json:"head_id"`
	HeadTime         time.Time `json:"head_time"`
}

func preflightSource(ctx context.Context, settings Settings, source TronEvidenceSource) (PreflightResult, error) {
	if !settings.RuntimeEnabled || settings.CreateEnabled || settings.Order.Chain.Environment != "nile" || settings.Order.Chain.GenesisID != NileGenesisID {
		return PreflightResult{}, errors.New("preflight requires reviewed Nile configuration with creation disabled")
	}
	genesis, err := source.GenesisID(ctx)
	if err != nil {
		return PreflightResult{}, err
	}
	if genesis != NileGenesisID {
		return PreflightResult{}, ErrWrongChain
	}
	head, err := source.SolidHead(ctx)
	if err != nil {
		return PreflightResult{}, err
	}
	height, err := tronBlockHeight(head)
	if err != nil || head.Header.RawData.Timestamp == nil || settings.Checkpoint.Height > height {
		return PreflightResult{}, ErrInvalidChainEvidence
	}
	headTime := time.UnixMilli(*head.Header.RawData.Timestamp)
	if time.Since(headTime) < 0 || time.Since(headTime) > 2*time.Minute {
		return PreflightResult{}, ErrNotSolidified
	}
	anchor, err := source.SolidBlock(ctx, settings.Checkpoint.Height)
	if err != nil {
		return PreflightResult{}, err
	}
	anchorHeight, err := tronBlockHeight(anchor)
	if err != nil || anchorHeight != settings.Checkpoint.Height || anchor.ID != settings.Checkpoint.BlockID {
		return PreflightResult{}, ErrScanConflict
	}
	return PreflightResult{Environment: "nile", GenesisID: genesis, Contract: settings.Order.Chain.Contract, CheckpointHeight: anchorHeight, CheckpointID: anchor.ID, HeadHeight: height, HeadID: head.ID, HeadTime: headTime.UTC()}, nil
}

func ProbeNile(ctx context.Context, settings Settings) (PreflightResult, error) {
	rpc, err := NewTronRPC(TronRPCConfig{Environment: "nile", BaseURL: "https://nile.trongrid.io"})
	if err != nil {
		return PreflightResult{}, err
	}
	return preflightSource(ctx, settings, rpc)
}
