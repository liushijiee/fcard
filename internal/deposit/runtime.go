package deposit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Runtime struct {
	Service      *Service
	Callback     http.Handler
	creator      *CreateWorker
	scanner      *TronScanner
	callbacks    *CallbackWorker
	chain        *ChainWorker
	candidates   *ChainCandidatePublisher
	lastCaughtUp atomic.Int64
	running      atomic.Bool
}

func NewRuntime(ctx context.Context, pool *pgxpool.Pool, settings Settings) (*Runtime, error) {
	if !settings.RuntimeEnabled {
		return nil, nil
	}
	store, err := NewOrderStore(pool, settings.Order)
	if err != nil {
		return nil, err
	}
	client, err := epusdt.New(settings.Gateway)
	if err != nil {
		return nil, err
	}
	creator, err := NewCreateWorker(store, client, settings.Wallets)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store, creator)
	if err != nil {
		return nil, err
	}
	verifier, err := epusdt.NewCallbackVerifier(epusdt.CallbackConfig{Environment: "nile", PID: settings.Gateway.PID, Keys: []epusdt.CallbackKey{{Version: "current", Secret: settings.Gateway.Secret}}})
	if err != nil {
		return nil, err
	}
	recorder, err := NewInboxRecorder(pool, verifier)
	if err != nil {
		return nil, err
	}
	callback, err := epusdt.NewCallbackHandler(verifier, recorder)
	if err != nil {
		return nil, err
	}
	chainStore, err := NewTronScanStore(pool, settings.Order.Chain, settings.Scanner)
	if err != nil {
		return nil, err
	}
	if _, err := chainStore.Initialize(ctx, settings.Checkpoint); err != nil {
		return nil, err
	}
	rpc, err := NewTronRPC(TronRPCConfig{Environment: "nile", BaseURL: "https://nile.trongrid.io"})
	if err != nil {
		return nil, err
	}
	reader, err := NewTronBlockReader(settings.Order.Chain, rpc)
	if err != nil {
		return nil, err
	}
	scanner, err := NewTronScanner(reader, chainStore, TronScannerOptions{CatchUpBatchSize: 16, PollInterval: 3 * time.Second, RequestTimeout: 30 * time.Second, MaxBackoff: time.Minute, MaxConsecutiveFailures: 5})
	if err != nil {
		return nil, err
	}
	leases, err := NewJobLeaseStore(pool, JobLeaseConfig{Lease: 30 * time.Second, MaxAttempts: 10, RetryBase: 3 * time.Second, RetryMax: time.Minute})
	if err != nil {
		return nil, err
	}
	callbacks, err := NewCallbackWorker(leases)
	if err != nil {
		return nil, err
	}
	chain, err := NewChainWorker(leases)
	if err != nil {
		return nil, err
	}
	candidates, err := NewChainCandidatePublisher(pool, settings.Order.Chain)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{Service: service, Callback: callback, creator: creator, scanner: scanner, callbacks: callbacks, chain: chain, candidates: candidates}
	service.admission, creator.admission = runtime.AdmissionReady, runtime.AdmissionReady
	return runtime, nil
}

func (runtime *Runtime) AdmissionReady() bool {
	last := runtime.lastCaughtUp.Load()
	return runtime.running.Load() && last > 0 && time.Since(time.Unix(0, last)) < 30*time.Second
}

func (runtime *Runtime) Run(ctx context.Context, logger *slog.Logger) error {
	if !runtime.running.CompareAndSwap(false, true) {
		return errors.New("deposit runtime already running")
	}
	defer runtime.running.Store(false)
	defer runtime.lastCaughtUp.Store(0)
	if logger == nil {
		logger = slog.Default()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	failures := make(chan error, 4)
	run := func(work func(context.Context) error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := work(workerCtx)
			if err != nil && workerCtx.Err() == nil {
				failures <- err
			}
		}()
	}
	run(func(ctx context.Context) error { return runtime.scanLoop(ctx) })
	config := CallbackRunConfig{PollInterval: time.Second, StepTimeout: 10 * time.Second, FailureBudget: 5}
	run(func(ctx context.Context) error { return runtime.callbacks.Run(ctx, config) })
	run(func(ctx context.Context) error { return runtime.chain.Run(ctx, config) })
	run(func(ctx context.Context) error { return runtime.maintenanceLoop(ctx, logger) })
	var result error
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case result = <-failures:
		logger.Error("deposit runtime stopped; admission disabled; manual review required")
	}
	runtime.running.Store(false)
	cancel()
	workers.Wait()
	return result
}

func (runtime *Runtime) scanLoop(ctx context.Context) error {
	failures := 0
	for {
		step, cancel := context.WithTimeout(ctx, 30*time.Second)
		committed, err := runtime.scanner.ScanOnce(step)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if cursor, loadErr := runtime.scanner.repository.Load(ctx); loadErr == nil {
				// Reasons are fixed evidence errors; do not log callback payloads.
				logger := slog.Default()
				logger.Warn("Nile scan step failed", "next_height", cursor.BlockHeight+1, "reason", err.Error())
			}
			runtime.lastCaughtUp.Store(0)
			failures++
			if errors.Is(err, ErrWrongChain) || errors.Is(err, ErrInvalidChainEvidence) || errors.Is(err, ErrUnsupportedTronTransaction) || errors.Is(err, ErrScanConflict) || failures >= 5 {
				return err
			}
		} else {
			failures = 0
			if committed {
				continue
			}
			cursor, loadErr := runtime.scanner.repository.Load(ctx)
			if loadErr != nil {
				return loadErr
			}
			lookup, lookupCancel := context.WithTimeout(ctx, 5*time.Second)
			block, blockErr := db.New(runtime.Service.store.pool).GetChainScanBlock(lookup, db.GetChainScanBlockParams{ChainEnvironment: cursor.ChainEnvironment, GenesisID: cursor.GenesisID, Network: cursor.Network, Scanner: cursor.Scanner, BlockHeight: cursor.BlockHeight})
			lookupCancel()
			if blockErr != nil && !errors.Is(blockErr, pgx.ErrNoRows) {
				return blockErr
			}
			if blockErr == nil && time.Since(block.BlockTime) >= 0 && time.Since(block.BlockTime) < 2*time.Minute {
				runtime.lastCaughtUp.Store(time.Now().UnixNano())
			} else {
				runtime.lastCaughtUp.Store(0)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (runtime *Runtime) maintenanceLoop(ctx context.Context, logger *slog.Logger) error {
	failures := 0
	for {
		step, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := runtime.creator.RecoverStaleClaims(step, time.Minute)
		if err == nil {
			_, err = runtime.Service.Expire(step)
		}
		if err == nil {
			_, err = runtime.candidates.ScanOnce(step)
		}
		if err == nil && runtime.Service.Enabled() {
			_, err = runtime.creator.ScanOnce(step)
		}
		if errors.Is(err, ErrDepositsDisabled) {
			err = nil
		}
		if err == nil {
			counts, countErr := db.New(runtime.Service.store.pool).GetDepositOperationalCounts(step)
			err = countErr
			if err == nil && (counts.UnresolvedCreates > 0 || counts.ReviewJobs > 0) {
				logger.Warn("deposit review required", "unresolved_creates", counts.UnresolvedCreates, "review_jobs", counts.ReviewJobs)
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			failures++
			logger.Warn("deposit maintenance failed; no create POST replay", "failures", failures)
			if failures >= 5 {
				return err
			}
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
