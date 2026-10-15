package deposit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"fcard/internal/storage/db"
)

type TronScanRepository interface {
	Load(context.Context) (db.ChainScanCursor, error)
	CommitSolidBlock(context.Context, SolidBlockBatch) (bool, error)
}

type TronScannerOptions struct {
	CatchUpBatchSize       int
	PollInterval           time.Duration
	RequestTimeout         time.Duration
	MaxBackoff             time.Duration
	MaxConsecutiveFailures int
}

type TronScanner struct {
	reader     *TronBlockReader
	repository TronScanRepository
	options    TronScannerOptions
	mutex      sync.Mutex
	pending    *SolidBlockBatch
	queue      []SolidBlockBatch
}

func NewTronScanner(reader *TronBlockReader, repository TronScanRepository, options TronScannerOptions) (*TronScanner, error) {
	if reader == nil || repository == nil || options.CatchUpBatchSize < 0 || options.CatchUpBatchSize > 32 || options.PollInterval < 10*time.Millisecond || options.PollInterval > time.Minute || options.RequestTimeout < time.Second || options.RequestTimeout > 5*time.Minute || options.MaxBackoff < options.PollInterval || options.MaxBackoff > 5*time.Minute || options.MaxConsecutiveFailures < 1 || options.MaxConsecutiveFailures > 100 {
		return nil, errors.New("bounded chain scanner polling, deadline and retry options are required")
	}
	return &TronScanner{reader: reader, repository: repository, options: options}, nil
}

func (scanner *TronScanner) ScanOnce(ctx context.Context) (bool, error) {
	scanner.mutex.Lock()
	defer scanner.mutex.Unlock()
	if scanner.options.CatchUpBatchSize > 1 {
		return scanner.scanRange(ctx)
	}
	if scanner.pending == nil {
		cursor, err := scanner.repository.Load(ctx)
		if err != nil {
			return false, err
		}
		config := scanner.reader.config
		if cursor.ChainEnvironment != config.Environment || cursor.GenesisID != config.GenesisID || cursor.Network != "tron" || cursor.Contract != config.Contract || cursor.EvidenceVersion != 2 {
			return false, ErrScanConflict
		}
		batch, err := scanner.reader.ReadNext(ctx, ScanCheckpoint{Height: cursor.BlockHeight, BlockID: cursor.BlockID})
		if errors.Is(err, ErrScanCaughtUp) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		scanner.pending = &batch
	}
	committed, err := scanner.repository.CommitSolidBlock(ctx, *scanner.pending)
	if err != nil {
		return false, err
	}
	scanner.pending = nil
	return committed, nil
}

func (scanner *TronScanner) scanRange(ctx context.Context) (bool, error) {
	if len(scanner.queue) == 0 {
		cursor, err := scanner.repository.Load(ctx)
		if err != nil {
			return false, err
		}
		config := scanner.reader.config
		if cursor.ChainEnvironment != config.Environment || cursor.GenesisID != config.GenesisID || cursor.Network != "tron" || cursor.Contract != config.Contract || cursor.EvidenceVersion != 2 {
			return false, ErrScanConflict
		}
		batches, err := scanner.reader.ReadRange(ctx, ScanCheckpoint{Height: cursor.BlockHeight, BlockID: cursor.BlockID}, scanner.options.CatchUpBatchSize)
		if errors.Is(err, ErrScanCaughtUp) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		scanner.queue = batches
	}
	committed := false
	for len(scanner.queue) > 0 {
		changed, err := scanner.repository.CommitSolidBlock(ctx, scanner.queue[0])
		if err != nil {
			return committed, err
		}
		committed = committed || changed
		scanner.queue = scanner.queue[1:]
	}
	return committed, nil
}

func (scanner *TronScanner) Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	failures := 0
	backoff := scanner.options.PollInterval
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, scanner.options.RequestTimeout)
		committed, err := scanner.ScanOnce(request)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := scanner.options.PollInterval
		if err != nil {
			if errors.Is(err, ErrWrongChain) || errors.Is(err, ErrInvalidChainEvidence) || errors.Is(err, ErrScanConflict) || errors.Is(err, ErrUnsupportedTronTransaction) {
				logger.Error("chain scanner paused; evidence or configuration requires review")
				return err
			}
			failures++
			if failures >= scanner.options.MaxConsecutiveFailures {
				logger.Error("chain scanner retry budget exhausted", "failures", failures)
				return err
			}
			delay = backoff
			backoff = min(backoff*2, scanner.options.MaxBackoff)
			logger.Warn("chain scanner retry scheduled without advancing cursor", "failures", failures)
		} else {
			failures, backoff = 0, scanner.options.PollInterval
			if committed {
				continue
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
