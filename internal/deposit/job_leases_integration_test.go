package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"
	"fcard/migrations"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func jobTestLeases(test testing.TB, pool *pgxpool.Pool) *JobLeaseStore {
	test.Helper()
	store, err := NewJobLeaseStore(pool, JobLeaseConfig{Lease: 30 * time.Second, MaxAttempts: 3, RetryBase: time.Second, RetryMax: 4 * time.Second})
	if err != nil {
		test.Fatal(err)
	}
	return store
}

func jobTestVerifier(test testing.TB) *epusdt.CallbackVerifier {
	test.Helper()
	verifier, err := epusdt.NewCallbackVerifier(epusdt.CallbackConfig{Environment: epusdt.EnvironmentNile, PID: "1000", Keys: []epusdt.CallbackKey{{Version: "job-test", Secret: "synthetic-job-secret"}}})
	if err != nil {
		test.Fatal(err)
	}
	return verifier
}

func jobTestRef(job db.DepositJob) LeaseRef {
	return LeaseRef{JobID: job.ID, Token: uuid.UUID(job.LeaseToken.Bytes)}
}

func jobTestExec(test testing.TB, pool *pgxpool.Pool, query string, args ...any) {
	test.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		test.Fatal(err)
	}
}

func jobTestDue(test testing.TB, pool *pgxpool.Pool, id uuid.UUID) {
	test.Helper()
	jobTestExec(test, pool, "UPDATE deposit_jobs SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", id)
}

func TestDepositJobLeasesIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	leases := jobTestLeases(test, pool)
	ctx := context.Background()
	verifier := jobTestVerifier(test)
	recorder, err := NewInboxRecorder(pool, verifier)
	if err != nil {
		test.Fatal(err)
	}
	notification := depositTestNotification(test, verifier, "synthetic-job-secret")
	if err := recorder.Record(ctx, notification); err != nil {
		test.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan struct {
		job db.DepositJob
		err error
	}, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			job, err := leases.Claim(ctx, "VERIFY_CALLBACK")
			results <- struct {
				job db.DepositJob
				err error
			}{job, err}
		}()
	}
	wait.Wait()
	close(results)
	var claimed db.DepositJob
	count := 0
	for result := range results {
		if result.err == nil {
			claimed = result.job
			count++
		} else if !errors.Is(result.err, ErrNoDepositJob) {
			test.Fatal(result.err)
		}
	}
	if count != 1 || claimed.Attempts != 1 || !claimed.LeaseToken.Valid {
		test.Fatal("claim was not exclusive", count, claimed.Attempts)
	}
	original := jobTestRef(claimed)
	renewed, err := leases.Renew(ctx, original)
	if err != nil || !renewed.LeaseUntil.Time.After(claimed.LeaseUntil.Time) || renewed.Attempts != 1 {
		test.Fatal("renew failed", err)
	}
	if _, err := leases.Retry(ctx, original, "http://credential-secret"); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal("unsafe error persisted", err)
	}
	jobTestExec(test, pool, "UPDATE deposit_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", claimed.ID)
	if _, err := leases.Finish(ctx, original); !errors.Is(err, ErrDepositLeaseLost) {
		test.Fatal("expired owner finished", err)
	}
	if _, err := leases.Renew(ctx, original); !errors.Is(err, ErrDepositLeaseLost) {
		test.Fatal("expired owner renewed", err)
	}
	blocking, err := pool.Begin(ctx)
	if err != nil {
		test.Fatal(err)
	}
	defer rollbackDeposit(blocking)
	if _, err := db.New(pool).WithTx(blocking).LockDepositJob(ctx, claimed.ID); err != nil {
		test.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, time.Second)
	recovered, recoverErr := leases.Recover(short, "VERIFY_CALLBACK")
	cancel()
	if recoverErr != nil || recovered != 0 {
		test.Fatal("recovery waited on a locked job", recovered, recoverErr)
	}
	if err := blocking.Rollback(ctx); err != nil {
		test.Fatal(err)
	}
	if count, err := leases.Recover(ctx, "VERIFY_CHAIN"); err != nil || count != 0 {
		test.Fatal("recovery crossed kinds", count, err)
	}
	restarted := jobTestLeases(test, pool)
	if count, err := restarted.Recover(ctx, "VERIFY_CALLBACK"); err != nil || count != 1 {
		test.Fatal(count, err)
	}
	if _, err := restarted.Claim(ctx, "VERIFY_CALLBACK"); !errors.Is(err, ErrNoDepositJob) {
		test.Fatal("recovery backoff ignored", err)
	}
	jobTestDue(test, pool, claimed.ID)
	claimed, err = restarted.Claim(ctx, "VERIFY_CALLBACK")
	if err != nil || claimed.Attempts != 2 || jobTestRef(claimed).Token == original.Token {
		test.Fatal("reclaim failed", err)
	}
	if _, err := leases.Retry(ctx, original, "STALE_OWNER"); !errors.Is(err, ErrDepositLeaseLost) {
		test.Fatal("stale owner changed job", err)
	}
	pending, err := restarted.Retry(ctx, jobTestRef(claimed), "QUOTE_PENDING")
	if err != nil || pending.Status != "PENDING" || pending.LeaseToken.Valid || pending.LastError != "QUOTE_PENDING" {
		test.Fatal(pending.Status, err)
	}
	jobTestDue(test, pool, claimed.ID)
	claimed, err = restarted.Claim(ctx, "VERIFY_CALLBACK")
	if err != nil || claimed.Attempts != 3 {
		test.Fatal(err)
	}
	exhausted, err := restarted.Retry(ctx, jobTestRef(claimed), "QUOTE_PENDING")
	if err != nil || exhausted.Status != "MANUAL_REVIEW" {
		test.Fatal(exhausted.Status, err)
	}
	if _, err := leases.Claim(ctx, "VERIFY_CALLBACK"); !errors.Is(err, ErrNoDepositJob) {
		test.Fatal("exhausted job reclaimed", err)
	}
	secondQuote := epusdt.Transaction{OrderID: strings.Repeat("b", 32), TradeID: "recovery-budget", Amount: "20.010001", ActualAmount: "20.010002", ReceiveAddress: testTronRecipient}
	if err := recorder.Record(ctx, jobTestNotification(test, verifier, secondQuote, "USDT", false)); err != nil {
		test.Fatal(err)
	}
	limited := *leases
	limited.config.MaxAttempts = 1
	lastAttempt, err := limited.Claim(ctx, "VERIFY_CALLBACK")
	if err != nil {
		test.Fatal(err)
	}
	jobTestExec(test, pool, "UPDATE deposit_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", lastAttempt.ID)
	if count, err := limited.Recover(ctx, "VERIFY_CALLBACK"); err != nil || count != 1 {
		test.Fatal(count, err)
	}
	terminal, err := db.New(pool).GetDepositJob(ctx, lastAttempt.ID)
	if err != nil || terminal.Status != "MANUAL_REVIEW" || terminal.LastError != "LEASE_EXPIRED" {
		test.Fatal(terminal.Status, err)
	}
	orders := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	order, err := orders.Prepare(ctx, principal, uuid.NewString(), "25")
	if err != nil {
		test.Fatal(err)
	}
	order, err = orders.StartCreation(ctx, order.ID)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := leases.Claim(ctx, "CREATE"); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal("CREATE entered automatic leases", err)
	}
	if _, err := leases.Recover(ctx, "CREATE"); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal("CREATE recovered automatically", err)
	}
	if _, err := orders.StartCreation(ctx, order.ID); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("CREATE claim retried", err)
	}
	down, err := migrations.Files.ReadFile("000009_deposit_job_leases.down.sql")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		test.Fatal("used lease state discarded")
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
		test.Fatal("leases changed money")
	}
}

func TestDepositLeaseMigrationGuardIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	ctx := context.Background()
	down, err := migrations.Files.ReadFile("000009_deposit_job_leases.down.sql")
	if err != nil {
		test.Fatal(err)
	}
	jobTestExec(test, pool, string(down))
	orders := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	order, err := orders.Prepare(ctx, principal, uuid.NewString(), "20")
	if err != nil {
		test.Fatal(err)
	}
	jobID := uuid.New()
	jobTestExec(test, pool, "INSERT INTO deposit_jobs(id,kind,dedupe_key,order_id,payload,status) VALUES($1,'VERIFY_CHAIN','legacy',$2,'{}','IN_FLIGHT')", jobID, order.ID)
	up, err := migrations.Files.ReadFile("000009_deposit_job_leases.up.sql")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err == nil {
		test.Fatal("unleased legacy in-flight job upgraded automatically")
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM information_schema.columns WHERE table_name='deposit_jobs' AND column_name='lease_token'"); count != 0 {
		test.Fatal("rejected migration partially changed schema")
	}
	jobTestExec(test, pool, "UPDATE deposit_jobs SET status='MANUAL_REVIEW' WHERE id=$1", jobID)
	jobTestExec(test, pool, string(up))
	leases := jobTestLeases(test, pool)
	worker, err := NewCallbackWorker(leases)
	if err != nil {
		test.Fatal(err)
	}
	pool.Close()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := worker.Run(bounded, CallbackRunConfig{PollInterval: time.Second, StepTimeout: time.Second, FailureBudget: 2}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		test.Fatal("database failure budget did not stop worker", err)
	}
}

func jobTestNotification(test testing.TB, verifier *epusdt.CallbackVerifier, quote epusdt.Transaction, token string, mismatch bool) epusdt.Notification {
	test.Helper()
	return jobTestNotificationWithHash(test, verifier, quote, token, mismatch, testTronHash())
}

func jobTestNotificationWithHash(test testing.TB, verifier *epusdt.CallbackVerifier, quote epusdt.Transaction, token string, mismatch bool, txHash string) epusdt.Notification {
	test.Helper()
	params := map[string]string{"pid": "1000", "trade_id": quote.TradeID, "order_id": quote.OrderID, "amount": quote.Amount.String(), "actual_amount": quote.ActualAmount.String(), "receive_address": quote.ReceiveAddress, "token": token, "block_transaction_id": txHash, "status": "2"}
	if mismatch {
		params["actual_amount"] = "20.010003"
	}
	fields := map[string]any{}
	for key, value := range params {
		fields[key] = value
	}
	fields["amount"], fields["actual_amount"], fields["status"] = json.Number(params["amount"]), json.Number(params["actual_amount"]), 2
	fields["signature"] = epusdt.Sign(params, "synthetic-job-secret")
	raw, err := json.Marshal(fields)
	if err != nil {
		test.Fatal(err)
	}
	notification, err := verifier.Verify(raw)
	if err != nil {
		test.Fatal(err)
	}
	return notification
}

func TestDepositCallbackWorkerIntegration(test *testing.T) {
	for _, scenario := range []string{"match", "early", "mismatch", "expired", "disabled", "failed", "unknown", "rollback", "expired lease", "corrupt"} {
		test.Run(scenario, func(test *testing.T) {
			pool := chainIntegrationDatabase(test)
			leases := jobTestLeases(test, pool)
			orders := depositTestStore(test, pool)
			principal := depositTestPrincipal(test, pool)
			ctx := context.Background()
			order, err := orders.Prepare(ctx, principal, uuid.NewString(), "20.010001")
			if err != nil {
				test.Fatal(err)
			}
			order, err = orders.StartCreation(ctx, order.ID)
			if err != nil {
				test.Fatal(err)
			}
			quote := depositTestQuote(order, "20.010002")
			if scenario == "failed" {
				if _, err := orders.CreateFailed(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), false); err != nil {
					test.Fatal(err)
				}
			} else if scenario != "early" {
				order, err = orders.BindQuote(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), quote)
				if err != nil {
					test.Fatal(err)
				}
			}
			if scenario == "unknown" {
				quote.OrderID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			verifier := jobTestVerifier(test)
			notification := jobTestNotification(test, verifier, quote, "USDT", scenario == "mismatch")
			recorder, err := NewInboxRecorder(pool, verifier)
			if err != nil {
				test.Fatal(err)
			}
			if err := recorder.Record(ctx, notification); err != nil {
				test.Fatal(err)
			}
			if scenario == "expired" {
				jobTestExec(test, pool, "UPDATE deposit_orders SET status='EXPIRED' WHERE id=$1", order.ID)
			}
			if scenario == "disabled" {
				jobTestExec(test, pool, "UPDATE users SET status='DISABLED' WHERE id=$1", principal.User.ID)
				orders.config.Enabled = false
			}
			if scenario == "corrupt" {
				jobTestExec(test, pool, "UPDATE deposit_callback_inbox SET raw_payload=raw_payload || decode('20','hex')")
			}
			worker, err := NewCallbackWorker(leases)
			if err != nil {
				test.Fatal(err)
			}
			var jobID uuid.UUID
			if err := pool.QueryRow(ctx, "SELECT id FROM deposit_jobs WHERE kind='VERIFY_CALLBACK'").Scan(&jobID); err != nil {
				test.Fatal(err)
			}
			if scenario == "rollback" {
				jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_callback_match CHECK(action <> 'CALLBACK_MATCHED')")
				if processed, err := worker.ScanOnce(ctx); !processed || err == nil {
					test.Fatal("callback audit failure ignored", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN'"); count != 0 {
					test.Fatal("chain task leaked")
				}
				jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_callback_match")
				jobTestDue(test, pool, jobID)
			}
			if scenario == "expired lease" {
				job, err := leases.Claim(ctx, "VERIFY_CALLBACK")
				if err != nil {
					test.Fatal(err)
				}
				jobTestExec(test, pool, "UPDATE deposit_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID)
				if _, err := worker.Process(ctx, jobTestRef(job)); !errors.Is(err, ErrDepositLeaseLost) {
					test.Fatal("expired worker committed", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN'"); count != 0 {
					test.Fatal("expired worker queued chain verification")
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_events WHERE action='CALLBACK_MATCHED'"); count != 0 {
					test.Fatal("expired audit committed")
				}
				return
			}
			if processed, err := worker.ScanOnce(ctx); !processed || err != nil {
				test.Fatal(processed, err)
			}
			if scenario == "early" {
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN'"); count != 0 {
					test.Fatal("unbound quote queued confirmation")
				}
				if _, err := orders.BindQuote(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), quote); err != nil {
					test.Fatal(err)
				}
				jobTestDue(test, pool, jobID)
				if processed, err := worker.ScanOnce(ctx); !processed || err != nil {
					test.Fatal(processed, err)
				}
			}
			if scenario == "unknown" {
				for attempt := 1; attempt < 3; attempt++ {
					jobTestDue(test, pool, jobID)
					if _, err := worker.ScanOnce(ctx); err != nil {
						test.Fatal(err)
					}
				}
			}
			saved, err := db.New(pool).GetDepositJob(ctx, jobID)
			if err != nil {
				test.Fatal(err)
			}
			expected, chainCount := "DONE", 1
			if scenario == "mismatch" || scenario == "unknown" || scenario == "corrupt" || scenario == "failed" {
				expected, chainCount = "MANUAL_REVIEW", 0
			}
			if saved.Status != expected || saved.LeaseToken.Valid {
				test.Fatal(saved.Status, saved.LastError)
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN'"); count != chainCount {
				test.Fatal("wrong chain queue count", count)
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
				test.Fatal("callback confirmed funds")
			}
			if scenario == "match" {
				duplicate := jobTestNotification(test, verifier, quote, "usdt", false)
				if err := recorder.Record(ctx, duplicate); err != nil {
					test.Fatal(err)
				}
				uppercase := jobTestNotificationWithHash(test, verifier, quote, "USDT", false, strings.ToUpper(testTronHash()))
				if err := recorder.Record(ctx, uppercase); err != nil {
					test.Fatal("uppercase hash callback rejected", err)
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
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN'"); count != 1 {
					test.Fatal("chain hint not deduplicated")
				}
			}
		})
	}
}

func TestDepositChainJobConfirmationFencingIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	ctx := context.Background()
	orders := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	order := depositTestWaiting(test, orders, principal)
	verifier := jobTestVerifier(test)
	recorder, err := NewInboxRecorder(pool, verifier)
	if err != nil {
		test.Fatal(err)
	}
	if err := recorder.Record(ctx, jobTestNotification(test, verifier, depositTestQuote(order, "20.010002"), "USDT", false)); err != nil {
		test.Fatal(err)
	}
	leases := jobTestLeases(test, pool)
	worker, err := NewCallbackWorker(leases)
	if err != nil {
		test.Fatal(err)
	}
	if processed, err := worker.ScanOnce(ctx); err != nil || !processed {
		test.Fatal(err)
	}
	job, err := leases.Claim(ctx, "VERIFY_CHAIN")
	if err != nil {
		test.Fatal(err)
	}
	otherHash := testTronBlockID(101)
	payload, _ := json.Marshal(map[string]string{"tx_hash": otherHash})
	if err := db.New(pool).CreateDepositJob(ctx, db.CreateDepositJobParams{ID: uuid.New(), Kind: "VERIFY_CHAIN", DedupeKey: order.ID.String() + "/" + otherHash, OrderID: optionalDepositUUID(order.ID), Payload: payload}); err != nil {
		test.Fatal(err)
	}
	depositTestProof(test, pool, order, nil)
	if _, err := orders.Confirm(ctx, order.ID, TransferRef{TxHash: testTronHash(), EventIndex: 0}); err != nil {
		test.Fatal(err)
	}
	finished, err := db.New(pool).GetDepositJob(ctx, job.ID)
	if err != nil || finished.Status != "DONE" || finished.LeaseToken.Valid || finished.LeaseUntil.Valid {
		test.Fatal(finished.Status, err)
	}
	if _, err := leases.Retry(ctx, jobTestRef(job), "STALE_CHAIN_WORKER"); !errors.Is(err, ErrDepositLeaseLost) {
		test.Fatal("stale chain worker changed confirmed job", err)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='VERIFY_CHAIN' AND status='PENDING'"); count != 1 {
		test.Fatal("unrelated transaction hint lost", count)
	}
}
