package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/storage/db"
	"fcard/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func chainTestQueue(test testing.TB, pool *pgxpool.Pool, order db.DepositOrder, hash string) uuid.UUID {
	test.Helper()
	id := uuid.New()
	payload, _ := json.Marshal(chainJobPayload{TxHash: hash})
	if err := db.New(pool).CreateDepositJob(context.Background(), db.CreateDepositJobParams{ID: id, Kind: "VERIFY_CHAIN", DedupeKey: order.ID.String() + "/" + hash, OrderID: optionalDepositUUID(order.ID), Payload: payload}); err != nil {
		test.Fatal(err)
	}
	return id
}

func chainTestProof(test testing.TB, pool *pgxpool.Pool, order db.DepositOrder, scenario, hash string, height int64) {
	test.Helper()
	receipt := testTronReceipt()
	receipt.ID = hash
	receipt.BlockNumber = testInt64(height)
	receipt.BlockTimeStamp = testInt64(order.CreateStartedAt.Time.Add(time.Second).UnixMilli())
	kind, result := "TriggerSmartContract", "SUCCESS"
	switch scenario {
	case "ambiguous":
		receipt.Logs = append(receipt.Logs, testTronLog())
	case "late":
		receipt.BlockTimeStamp = testInt64(order.ExpiresAt.Time.Add(time.Second).UnixMilli())
	case "early":
		receipt.BlockTimeStamp = testInt64(order.CreateStartedAt.Time.Add(-time.Second).UnixMilli())
	case "wrong amount":
		receipt.Logs[0].Data = fmt.Sprintf("%064x", 20010003)
	case "self":
		receipt.Logs[0].Topics[1] = receipt.Logs[0].Topics[2]
	case "wrong contract":
		receipt.Logs[0].Address = strings.Repeat("44", 20)
	case "native":
		kind = "TransferContract"
		receipt.Logs = nil
		receipt.Receipt.Result = ""
	case "failed":
		result = "REVERT"
		receipt.Logs = nil
		receipt.Receipt.Result = "REVERT"
		receipt.Result = "FAILED"
	}
	block := testTronBlock(height, hash)
	block.Header.RawData.Timestamp = testInt64(*receipt.BlockTimeStamp)
	block.Transactions[0].Ret = []TronExecutionResult{{ContractRet: result}}
	block.Transactions[0].RawData = &TronTransactionRawData{Contracts: []TronContractType{{Type: kind}}}
	source := &testScanSource{genesis: testTronGenesis(), head: height, blocks: map[int64]TronBlock{height - 1: testTronBlock(height - 1), height: block}, receipts: map[string]TronReceipt{hash: receipt}}
	checkpoint := ScanCheckpoint{Height: height - 1, BlockID: testTronBlockID(height - 1)}
	batch, err := testBlockReader(test, source).ReadNext(context.Background(), checkpoint)
	if err != nil {
		test.Fatal(err)
	}
	store, err := NewTronScanStore(pool, testScanConfig(), fmt.Sprintf("chain-worker-%d", height))
	if err != nil {
		test.Fatal(err)
	}
	if _, err := store.Initialize(context.Background(), checkpoint); err != nil {
		test.Fatal(err)
	}
	if _, err := store.CommitSolidBlock(context.Background(), batch); err != nil {
		test.Fatal(err)
	}
}

func TestChainWorkerIntegration(test *testing.T) {
	for _, scenario := range []string{"confirm", "missing proof", "ambiguous", "late", "early", "wrong amount", "wrong contract", "self", "disabled", "native", "failed", "corrupt", "missing transfer", "expired lease", "lease expires during write", "audit rollback", "unknown"} {
		test.Run(scenario, func(test *testing.T) {
			ctx := context.Background()
			pool := chainIntegrationDatabase(test)
			orders := depositTestStore(test, pool)
			principal := depositTestPrincipal(test, pool)
			order := depositTestWaiting(test, orders, principal)
			id := chainTestQueue(test, pool, order, testTronHash())
			leases := jobTestLeases(test, pool)
			worker, err := NewChainWorker(leases)
			if err != nil {
				test.Fatal(err)
			}
			if scenario != "missing proof" {
				chainTestProof(test, pool, order, scenario, testTronHash(), 100)
			}
			if scenario == "disabled" {
				jobTestExec(test, pool, "UPDATE users SET status='DISABLED' WHERE id=$1", principal.User.ID)
			}
			if scenario == "corrupt" {
				jobTestExec(test, pool, "UPDATE chain_scan_blocks SET evidence_hash=repeat('f',64)")
			}
			if scenario == "missing transfer" {
				jobTestExec(test, pool, "DELETE FROM chain_transfers")
			}
			var pending db.DepositOrder
			if scenario == "unknown" {
				pending, err = orders.Prepare(ctx, principal, uuid.NewString(), "20.010001")
				if err != nil {
					test.Fatal(err)
				}
				pending, err = orders.StartCreation(ctx, pending.ID)
				if err != nil {
					test.Fatal(err)
				}
				pending, err = orders.CreateFailed(ctx, pending.ID, uuid.UUID(pending.CreateToken.Bytes), true)
				if err != nil {
					test.Fatal(err)
				}
			}
			if scenario == "expired lease" || scenario == "lease expires during write" {
				job, err := leases.Claim(ctx, "VERIFY_CHAIN")
				if err != nil {
					test.Fatal(err)
				}
				if scenario == "expired lease" {
					jobTestExec(test, pool, "UPDATE deposit_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", id)
				} else {
					jobTestExec(test, pool, "UPDATE deposit_jobs SET lease_until=clock_timestamp()+interval '0.2 second' WHERE id=$1", id)
					jobTestExec(test, pool, "CREATE FUNCTION test_slow_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.5); RETURN NEW; END $$; CREATE TRIGGER test_receipt_wait AFTER INSERT ON deposit_receipts FOR EACH ROW EXECUTE FUNCTION test_slow_receipt()")
				}
				if _, err := worker.Process(ctx, jobTestRef(job)); !errors.Is(err, ErrDepositLeaseLost) {
					test.Fatal("expired owner committed finance", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("expired receipt leaked")
				}
				saved, err := orders.Get(ctx, principal, order.ID)
				if err != nil || saved.Status != "WAITING" {
					test.Fatal(saved.Status, err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_events WHERE action IN ('PAYMENT_CONFIRMED','CHAIN_VERIFIED')"); count != 0 {
					test.Fatal("expired audit leaked")
				}
				return
			}
			if scenario == "audit rollback" {
				jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_chain_completion CHECK(action <> 'CHAIN_VERIFIED')")
				if processed, err := worker.ScanOnce(ctx); !processed || err == nil {
					test.Fatal("outer audit failure ignored", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("financial receipt committed without job audit")
				}
				saved, err := orders.Get(ctx, principal, order.ID)
				if err != nil || saved.Status != "WAITING" {
					test.Fatal(saved.Status, err)
				}
				jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_chain_completion")
				jobTestDue(test, pool, id)
			}
			if processed, err := worker.ScanOnce(ctx); !processed || err != nil {
				test.Fatal(processed, err)
			}
			if scenario == "missing proof" {
				for attempt := 1; attempt < 3; attempt++ {
					jobTestDue(test, pool, id)
					if _, err := worker.ScanOnce(ctx); err != nil {
						test.Fatal(err)
					}
				}
			}
			if scenario == "unknown" {
				job, err := db.New(pool).GetDepositJob(ctx, id)
				if err != nil || job.Status != "PENDING" || job.LastError != "ATTRIBUTION_PENDING" {
					test.Fatal(job.Status, job.LastError, err)
				}
				if _, err := orders.BindQuote(ctx, pending.ID, uuid.UUID(pending.CreateToken.Bytes), depositTestQuote(pending, "20.010003")); err != nil {
					test.Fatal(err)
				}
				jobTestDue(test, pool, id)
				if _, err := worker.ScanOnce(ctx); err != nil {
					test.Fatal(err)
				}
			}
			job, err := db.New(pool).GetDepositJob(ctx, id)
			if err != nil {
				test.Fatal(err)
			}
			success := scenario == "confirm" || scenario == "audit rollback" || scenario == "unknown"
			expected, receipts := "MANUAL_REVIEW", 0
			if success {
				expected, receipts = "DONE", 1
			}
			if job.Status != expected || job.LeaseToken.Valid {
				test.Fatal(job.Status, job.LastError)
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != receipts {
				test.Fatal(count)
			}
			if scenario == "native" || scenario == "failed" || scenario == "wrong contract" {
				if count := depositTestCount(test, pool, "SELECT count(*) FROM chain_scan_transactions"); count != 1 {
					test.Fatal("no-transfer transaction not indexed")
				}
			}
		})
	}
}

func TestChainCompensationWithoutCallbackIntegration(test *testing.T) {
	for _, scenario := range []string{"confirm", "late", "overlap"} {
		test.Run(scenario, func(test *testing.T) {
			ctx := context.Background()
			pool := chainIntegrationDatabase(test)
			orders := depositTestStore(test, pool)
			principal := depositTestPrincipal(test, pool)
			order := depositTestWaiting(test, orders, principal)
			if scenario == "overlap" {
				depositTestWaiting(test, orders, principal)
			}
			chainTestProof(test, pool, order, scenario, testTronHash(), 100)
			publisher, err := NewChainCandidatePublisher(pool, testScanConfig())
			if err != nil {
				test.Fatal(err)
			}
			var wait sync.WaitGroup
			results := make(chan int, 6)
			failures := make(chan error, 6)
			for index := 0; index < 6; index++ {
				wait.Add(1)
				go func() { defer wait.Done(); count, err := publisher.ScanOnce(ctx); results <- count; failures <- err }()
			}
			wait.Wait()
			close(results)
			close(failures)
			for err := range failures {
				if err != nil {
					test.Fatal(err)
				}
			}
			total := 0
			for count := range results {
				total += count
			}
			expected := 1
			if scenario == "overlap" {
				expected = 2
			}
			if total != expected {
				test.Fatal("compensation dedupe failed", total)
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_callback_inbox"); count != 0 {
				test.Fatal("compensation required callback")
			}
			worker, err := NewChainWorker(jobTestLeases(test, pool))
			if err != nil {
				test.Fatal(err)
			}
			for index := 0; index < expected; index++ {
				if _, err := worker.ScanOnce(ctx); err != nil {
					test.Fatal(err)
				}
			}
			receipts := 0
			if scenario == "confirm" {
				receipts = 1
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != receipts {
				test.Fatal("late/ambiguous compensation credited", count)
			}
			if count, err := publisher.ScanOnce(ctx); err != nil || count != 0 {
				test.Fatal("manual/done jobs resurrected", count, err)
			}
		})
	}
}

func TestChainTransactionIndexMigrationIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	ctx := context.Background()
	down, err := migrations.Files.ReadFile("000010_chain_transaction_index.down.sql")
	if err != nil {
		test.Fatal(err)
	}
	up, err := migrations.Files.ReadFile("000010_chain_transaction_index.up.sql")
	if err != nil {
		test.Fatal(err)
	}
	store := testChainStore(test, pool, "legacy-index")
	batch := testStoreBatch(test, testTronReceipt())
	if _, err := store.CommitSolidBlock(ctx, batch); err != nil {
		test.Fatal(err)
	}
	empty := SolidBlockBatch{Block: testTronBlock(101)}
	if _, err := store.CommitSolidBlock(ctx, empty); err != nil {
		test.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		test.Fatal("persisted index removed")
	}
	jobTestExec(test, pool, "DELETE FROM chain_scan_transactions")
	if _, err := store.CommitSolidBlock(ctx, batch); !errors.Is(err, ErrScanConflict) {
		test.Fatal("history replay ignored missing transaction index", err)
	}
	jobTestExec(test, pool, string(down))
	jobTestExec(test, pool, string(up))
	if count := depositTestCount(test, pool, "SELECT count(*) FROM chain_scan_transactions"); count != 1 {
		test.Fatal("legacy block not backfilled")
	}
	proof, err := db.New(pool).GetDepositTransactionProof(ctx, db.GetDepositTransactionProofParams{ChainEnvironment: TronEnvironmentNile, GenesisID: testTronGenesis(), Network: "tron", TxHash: testTronHash(), Contract: testTronContract()})
	if err != nil || proof.Variants != 1 {
		test.Fatal(err)
	}
}

func TestChainWorkerCompetingTransactionsIntegration(test *testing.T) {
	ctx := context.Background()
	pool := chainIntegrationDatabase(test)
	orders := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	order := depositTestWaiting(test, orders, principal)
	otherHash := strings.Repeat("cd", 32)
	chainTestProof(test, pool, order, "confirm", testTronHash(), 100)
	chainTestProof(test, pool, order, "confirm", otherHash, 101)
	chainTestQueue(test, pool, order, testTronHash())
	chainTestQueue(test, pool, order, otherHash)
	worker, err := NewChainWorker(jobTestLeases(test, pool))
	if err != nil {
		test.Fatal(err)
	}
	var wait sync.WaitGroup
	failures := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			processed, err := worker.ScanOnce(ctx)
			if err == nil && !processed {
				err = ErrNoDepositJob
			}
			failures <- err
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			test.Fatal(err)
		}
	}
	for query, expected := range map[string]int{"SELECT count(*) FROM deposit_receipts": 1, "SELECT count(*) FROM deposit_events WHERE action='PAYMENT_CONFIRMED'": 1, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN' AND status='DONE'": 1, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN' AND status='MANUAL_REVIEW' AND last_error='ORDER_ALREADY_CONFIRMED'": 1} {
		if count := depositTestCount(test, pool, query); count != expected {
			test.Fatal(query, count)
		}
	}
}
