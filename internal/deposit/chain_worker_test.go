package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fcard/internal/domain"
	"fcard/internal/storage/db"
	"github.com/google/uuid"
)

func TestChainJobPayloadBinding(test *testing.T) {
	order := db.DepositOrder{ID: uuid.New()}
	raw, _ := json.Marshal(chainJobPayload{TxHash: testTronHash()})
	job := db.DepositJob{OrderID: optionalDepositUUID(order.ID), DedupeKey: order.ID.String() + "/" + testTronHash(), Payload: raw}
	if hash, err := decodeChainJob(job, order); err != nil || hash != testTronHash() {
		test.Fatal(err)
	}
	for _, payload := range []string{`{"tx_hash":"bad"}`, `{"tx_hash":"` + testTronHash() + `","event_index":0}`, `{"tx_hash":"` + testTronHash() + `","inbox_id":"` + uuid.NewString() + `"}`, `{"tx_hash":"` + strings.ToUpper(testTronHash()) + `"}`, string(raw) + `{}`} {
		candidate := job
		candidate.Payload = []byte(payload)
		if _, err := decodeChainJob(candidate, order); err == nil {
			test.Fatal("unbound payload accepted")
		}
	}
	job.DedupeKey = "another-order"
	if _, err := decodeChainJob(job, order); err == nil {
		test.Fatal("dedupe binding ignored")
	}
	if _, err := NewChainWorker(nil); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	worker := &ChainWorker{leases: &JobLeaseStore{config: JobLeaseConfig{Lease: 30 * time.Second}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx, CallbackRunConfig{PollInterval: time.Second, StepTimeout: time.Second, FailureBudget: 2}); !errors.Is(err, context.Canceled) {
		test.Fatal(err)
	}
}
