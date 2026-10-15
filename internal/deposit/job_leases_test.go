package deposit

import (
	"context"
	"errors"
	"testing"
	"time"

	"fcard/internal/domain"
)

func TestDepositJobRetryBounds(test *testing.T) {
	store := &JobLeaseStore{config: JobLeaseConfig{RetryBase: time.Second, RetryMax: 3 * time.Second}}
	for attempts, expected := range map[int32]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 3 * time.Second, 20: 3 * time.Second} {
		if actual := store.retryDelay(attempts); actual != expected {
			test.Fatalf("attempt %d: %v", attempts, actual)
		}
	}
	for _, kind := range []string{"CREATE", "", "verify_callback"} {
		if verificationJobKind(kind) {
			test.Fatal("unsafe job kind accepted")
		}
	}
	if _, err := NewJobLeaseStore(nil, JobLeaseConfig{}); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	if _, err := NewCallbackWorker(&JobLeaseStore{}); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	worker := &CallbackWorker{leases: &JobLeaseStore{config: JobLeaseConfig{Lease: 30 * time.Second}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx, CallbackRunConfig{PollInterval: time.Second, StepTimeout: 5 * time.Second, FailureBudget: 2}); !errors.Is(err, context.Canceled) {
		test.Fatal(err)
	}
	if err := worker.Run(ctx, CallbackRunConfig{}); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
}
