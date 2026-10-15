package deposit

import (
	"context"
	"errors"
	"testing"
	"time"

	"fcard/internal/domain"
)

func TestCreateWorkerConfiguration(test *testing.T) {
	if _, err := NewCreateWorker(nil, nil, nil); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	worker := &CreateWorker{}
	for _, age := range []time.Duration{0, time.Second, time.Minute + time.Nanosecond, 25 * time.Hour} {
		if _, err := worker.RecoverStaleClaims(context.Background(), age); !errors.Is(err, domain.ErrInvalid) {
			test.Fatal(err)
		}
	}
}
