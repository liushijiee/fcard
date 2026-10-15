package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"
	"fcard/internal/userauth"
	"fcard/migrations"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func depositTestStore(test testing.TB, pool *pgxpool.Pool) *OrderStore {
	test.Helper()
	store, err := NewOrderStore(pool, OrderStoreConfig{Enabled: true, Chain: testScanConfig(), PID: "1000", NotifyURL: "https://fcard.example/api/deposits/notify", GatewayOrigin: "https://gateway.example", MaxQuoteLifetime: 30 * time.Minute, Quarantine: 24 * time.Hour})
	if err != nil {
		test.Fatal(err)
	}
	return store
}

func depositTestPrincipal(test testing.TB, pool *pgxpool.Pool) userauth.Principal {
	test.Helper()
	principal := userauth.Principal{User: userauth.Profile{ID: uuid.New()}, SessionID: uuid.New(), AuthVersion: 1}
	ctx := context.Background()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO users(id,email,display_name) VALUES($1,$2,'deposit test')", []any{principal.User.ID, principal.User.ID.String() + "@example.com"}},
		{"INSERT INTO user_credentials(user_id,password_hash,must_change_password) VALUES($1,'test-only-hash',false)", []any{principal.User.ID}},
		{"INSERT INTO user_sessions(id,user_id,token_hash,auth_version,expires_at) VALUES($1,$2,$3,1,now()+interval '1 hour')", []any{principal.SessionID, principal.User.ID, []byte(principal.SessionID.String())}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			test.Fatal(err)
		}
	}
	return principal
}

func TestSmallOrderLimitPreservesOriginalIdempotencyIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	store := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	key := uuid.NewString()
	original, err := store.Prepare(context.Background(), principal, key, "2")
	if err != nil {
		test.Fatal(err)
	}
	store.config.MaxOrderUnits = 1000000
	replayed, err := store.Prepare(context.Background(), principal, key, "2")
	if err != nil || replayed.ID != original.ID {
		test.Fatal("new limit broke original idempotency", err)
	}
	if _, err := store.Prepare(context.Background(), principal, uuid.NewString(), "1.000001"); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal("test limit bypassed", err)
	}
	if _, err := store.Prepare(context.Background(), principal, uuid.NewString(), "1"); err != nil {
		test.Fatal("valid small test order refused", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM deposit_orders WHERE user_id=$1", principal.User.ID).Scan(&count); err != nil || count != 2 {
		test.Fatal("rejected request persisted an order", err)
	}
}

func depositTestQuote(order db.DepositOrder, actual string) epusdt.Transaction {
	return epusdt.Transaction{OrderID: order.GatewayOrderID, TradeID: "trade-" + order.ID.String(), Amount: json.Number(depositAmount(order.RequestedUnits)), ActualAmount: json.Number(actual), ReceiveAddress: testTronRecipient, Token: "USDT", Currency: "USDT", Status: 1, ExpirationTime: order.CreateStartedAt.Time.Add(15 * time.Minute).Unix(), PaymentURL: "https://gateway.example/pay/checkout-counter/" + order.GatewayOrderID}
}

func depositTestWaiting(test testing.TB, store *OrderStore, principal userauth.Principal) db.DepositOrder {
	test.Helper()
	order, err := store.Prepare(context.Background(), principal, uuid.NewString(), "20.010001")
	if err != nil {
		test.Fatal(err)
	}
	order, err = store.StartCreation(context.Background(), order.ID)
	if err != nil {
		test.Fatal(err)
	}
	order, err = store.BindQuote(context.Background(), order.ID, uuid.UUID(order.CreateToken.Bytes), depositTestQuote(order, "20.010002"))
	if err != nil {
		test.Fatal(err)
	}
	return order
}

func depositTestCount(test testing.TB, pool *pgxpool.Pool, query string, args ...any) int {
	test.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		test.Fatal(err)
	}
	return count
}

func TestDepositOrderIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	store := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "ALTER TABLE deposit_events ADD CONSTRAINT reject_preparation_audit CHECK(action <> 'ORDER_PREPARED')"); err != nil {
		test.Fatal(err)
	}
	if _, err := store.Prepare(ctx, principal, uuid.NewString(), "20"); err == nil {
		test.Fatal("preparation audit failure accepted")
	}
	for _, table := range []string{"deposit_orders", "deposit_jobs", "deposit_events"} {
		if count := depositTestCount(test, pool, "SELECT count(*) FROM "+table); count != 0 {
			test.Fatal("preparation partially committed", table, count)
		}
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE deposit_events DROP CONSTRAINT reject_preparation_audit"); err != nil {
		test.Fatal(err)
	}
	key := uuid.NewString()
	var wait sync.WaitGroup
	ids := make(chan uuid.UUID, 8)
	failures := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			order, err := store.Prepare(ctx, principal, key, "20.010001")
			ids <- order.ID
			failures <- err
		}()
	}
	wait.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		if err != nil {
			test.Fatal(err)
		}
	}
	var id uuid.UUID
	for candidate := range ids {
		if id != uuid.Nil && candidate != id {
			test.Fatal("concurrent idempotency diverged")
		}
		id = candidate
	}
	order, err := store.Prepare(ctx, principal, key, "20.010001")
	if err != nil || order.ID != id {
		test.Fatal(err)
	}
	if _, err := store.Prepare(ctx, principal, key, "21"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		test.Fatal(err)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_jobs WHERE kind='CREATE'"); count != 1 {
		test.Fatal(count)
	}
	stranger := depositTestPrincipal(test, pool)
	if _, err := store.Get(ctx, stranger, id); !errors.Is(err, domain.ErrNotFound) {
		test.Fatal("other user read order", err)
	}
	disabled := *store
	disabled.config.Enabled = false
	if _, err := disabled.Prepare(ctx, principal, uuid.NewString(), "20"); !errors.Is(err, ErrDepositsDisabled) {
		test.Fatal(err)
	}
	order, err = store.StartCreation(ctx, id)
	if err != nil {
		test.Fatal(err)
	}
	token := uuid.UUID(order.CreateToken.Bytes)
	if _, err := store.StartCreation(ctx, id); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("create claimed twice", err)
	}
	order, err = store.CreateFailed(ctx, id, token, true)
	if err != nil || order.Status != "CREATE_UNKNOWN" {
		test.Fatal(order.Status, err)
	}
	if _, err := store.StartCreation(ctx, id); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("unknown retried", err)
	}
	quote := depositTestQuote(order, "20.010002")
	order, err = store.BindQuote(ctx, id, token, quote)
	if err != nil || order.Status != "WAITING" {
		test.Fatal(order.Status, err)
	}
	for _, statement := range []string{
		"UPDATE deposit_orders SET user_id=$2 WHERE id=$1",
		"UPDATE deposit_orders SET create_token=NULL,create_started_at=NULL WHERE id=$1 AND $2::uuid IS NOT NULL",
		"UPDATE deposit_orders SET expires_at=expires_at+interval '1 second',reserved_until=reserved_until+interval '1 second' WHERE id=$1 AND $2::uuid IS NOT NULL",
	} {
		if _, err := pool.Exec(ctx, statement, id, stranger.User.ID); err == nil {
			test.Fatal("immutable deposit snapshot changed")
		}
	}
	if _, err := store.BindQuote(ctx, id, token, quote); err != nil {
		test.Fatal("quote replay", err)
	}
	quote.ActualAmount = "20.010003"
	if _, err := store.BindQuote(ctx, id, token, quote); !errors.Is(err, domain.ErrConflict) {
		test.Fatal("changed quote accepted", err)
	}
	overlap := depositTestWaiting(test, store, principal)
	if overlap.Status != "REVIEW_REQUIRED" || overlap.ReviewReason != "RESERVATION_OVERLAP" {
		test.Fatal(overlap.Status, overlap.ReviewReason)
	}
	down, err := migrations.Files.ReadFile("000008_deposits.down.sql")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		test.Fatal("down destroyed order evidence")
	}
	for _, mutation := range []string{"UPDATE user_sessions SET revoked=true WHERE id=$1", "UPDATE user_sessions SET auth_version=2 WHERE id=$1", "UPDATE user_sessions SET expires_at=now()-interval '1 second' WHERE id=$1"} {
		if _, err := pool.Exec(ctx, mutation, principal.SessionID); err != nil {
			test.Fatal(err)
		}
		if _, err := store.Get(ctx, principal, id); !errors.Is(err, domain.ErrUnauthorized) {
			test.Fatal("invalid session accepted", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE user_sessions SET revoked=false,auth_version=1,expires_at=now()+interval '1 hour' WHERE id=$1", principal.SessionID); err != nil {
			test.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE user_credentials SET must_change_password=true WHERE user_id=$1", principal.User.ID); err != nil {
		test.Fatal(err)
	}
	if _, err := store.Prepare(ctx, principal, uuid.NewString(), "20"); !errors.Is(err, domain.ErrPasswordChangeRequired) {
		test.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE user_credentials SET must_change_password=false,auth_version=2 WHERE user_id=$1", principal.User.ID); err != nil {
		test.Fatal(err)
	}
	if _, err := store.Get(ctx, principal, id); !errors.Is(err, domain.ErrUnauthorized) {
		test.Fatal("stale authorization accepted", err)
	}
	fresh, err := store.Prepare(ctx, stranger, uuid.NewString(), "25")
	if err != nil {
		test.Fatal(err)
	}
	fresh, err = store.StartCreation(ctx, fresh.ID)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := store.CreateFailed(ctx, fresh.ID, uuid.New(), false); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("foreign claim token accepted", err)
	}
	fresh, err = store.CreateFailed(ctx, fresh.ID, uuid.UUID(fresh.CreateToken.Bytes), false)
	if err != nil || fresh.Status != "FAILED" {
		test.Fatal(fresh.Status, err)
	}
	if _, err := store.StartCreation(ctx, fresh.ID); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("failed order recreated", err)
	}
}

func depositTestNotification(test testing.TB, verifier *epusdt.CallbackVerifier, secret string) epusdt.Notification {
	test.Helper()
	params := map[string]string{"pid": "1000", "trade_id": "early-trade", "order_id": strings.Repeat("a", 32), "amount": "20.010001", "actual_amount": "20.010002", "receive_address": testTronRecipient, "token": "USDT", "block_transaction_id": testTronHash(), "status": "2"}
	fields := map[string]any{}
	for key, value := range params {
		fields[key] = value
	}
	fields["amount"], fields["actual_amount"], fields["status"] = json.Number(params["amount"]), json.Number(params["actual_amount"]), 2
	fields["signature"] = epusdt.Sign(params, secret)
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

func TestDepositInboxIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	ctx := context.Background()
	verifier, err := epusdt.NewCallbackVerifier(epusdt.CallbackConfig{Environment: epusdt.EnvironmentNile, PID: "1000", Keys: []epusdt.CallbackKey{{Version: "new", Secret: "synthetic-new-secret"}, {Version: "old", Secret: "synthetic-old-secret"}}})
	if err != nil {
		test.Fatal(err)
	}
	recorder, err := NewInboxRecorder(pool, verifier)
	if err != nil {
		test.Fatal(err)
	}
	notification := depositTestNotification(test, verifier, "synthetic-old-secret")
	if _, err := pool.Exec(ctx, "ALTER TABLE deposit_jobs ADD CONSTRAINT reject_test_callback CHECK(kind <> 'VERIFY_CALLBACK')"); err != nil {
		test.Fatal(err)
	}
	handler, err := epusdt.NewCallbackHandler(verifier, recorder)
	if err != nil {
		test.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(notification.RawPayload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		test.Fatal(response.Code)
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_callback_inbox"); count != 0 {
		test.Fatal("inbox committed without job")
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE deposit_jobs DROP CONSTRAINT reject_test_callback"); err != nil {
		test.Fatal(err)
	}
	if err := recorder.Record(ctx, notification); err != nil {
		test.Fatal(err)
	}
	rotated := depositTestNotification(test, verifier, "synthetic-new-secret")
	if rotated.EventID != notification.EventID {
		test.Fatal("key rotation changed event")
	}
	var wait sync.WaitGroup
	failures := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); failures <- recorder.Record(ctx, rotated) }()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			test.Fatal(err)
		}
	}
	var raw []byte
	var deliveries int
	var version string
	if err := pool.QueryRow(ctx, "SELECT raw_payload,deliveries,key_version FROM deposit_callback_inbox").Scan(&raw, &deliveries, &version); err != nil {
		test.Fatal(err)
	}
	if !bytes.Equal(raw, notification.RawPayload) || deliveries != 9 || version != "old" {
		test.Fatal("first evidence not retained")
	}
	for _, table := range []string{"deposit_callback_inbox", "deposit_jobs", "deposit_events"} {
		if count := depositTestCount(test, pool, "SELECT count(*) FROM "+table); count != 1 {
			test.Fatal(table, count)
		}
	}
	forged := notification
	forged.ActualAmountUnits++
	if err := recorder.Record(ctx, forged); !errors.Is(err, epusdt.ErrInvalidNotification) {
		test.Fatal("forged fields accepted", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := recorder.Record(canceled, notification); err == nil {
		test.Fatal("canceled callback acknowledged")
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(rotated.RawPayload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		test.Fatal(response.Code, response.Body.String())
	}
	if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
		test.Fatal("callback confirmed money")
	}
}

func depositTestProof(test testing.TB, pool *pgxpool.Pool, order db.DepositOrder, change func(*TronReceipt)) {
	test.Helper()
	receipt := testTronReceipt()
	receipt.BlockTimeStamp = testInt64(order.CreateStartedAt.Time.Add(time.Second).UnixMilli())
	if change != nil {
		change(&receipt)
	}
	source := &testScanSource{genesis: testTronGenesis(), head: 100, blocks: map[int64]TronBlock{}, receipts: map[string]TronReceipt{receipt.ID: receipt}}
	block := testTronBlock(100, receipt.ID)
	block.Header.RawData.Timestamp = testInt64(*receipt.BlockTimeStamp)
	block.Transactions[0].Ret = []TronExecutionResult{{ContractRet: "SUCCESS"}}
	block.Transactions[0].RawData = &TronTransactionRawData{Contracts: []TronContractType{{Type: "TriggerSmartContract"}}}
	source.blocks[99], source.blocks[100] = testTronBlock(99), block
	batch, err := testBlockReader(test, source).ReadNext(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)})
	if err != nil {
		test.Fatal(err)
	}
	scanner, err := NewTronScanStore(pool, testScanConfig(), "deposit-test")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := scanner.Initialize(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); err != nil {
		test.Fatal(err)
	}
	if _, err := scanner.CommitSolidBlock(context.Background(), batch); err != nil {
		test.Fatal(err)
	}
}

func TestDepositConfirmationIntegration(test *testing.T) {
	for _, scenario := range []string{"confirm", "ambiguous", "late", "wrong amount", "disabled", "overlap", "rollback", "corrupt proof", "corrupt transfer", "unresolved create"} {
		test.Run(scenario, func(test *testing.T) {
			pool := chainIntegrationDatabase(test)
			store := depositTestStore(test, pool)
			principal := depositTestPrincipal(test, pool)
			ctx := context.Background()
			order := depositTestWaiting(test, store, principal)
			if _, err := pool.Exec(ctx, "UPDATE deposit_orders SET status='CONFIRMED' WHERE id=$1", order.ID); err == nil {
				test.Fatal("database accepted confirmation without receipt")
			}
			ref := TransferRef{TxHash: testTronHash(), EventIndex: 0}
			if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, ErrDepositProofPending) {
				test.Fatal("confirmed without proof", err)
			}
			depositTestProof(test, pool, order, func(receipt *TronReceipt) {
				switch scenario {
				case "ambiguous":
					receipt.Logs = append(receipt.Logs, testTronLog())
				case "late":
					receipt.BlockTimeStamp = testInt64(order.ExpiresAt.Time.Add(time.Second).UnixMilli())
				case "wrong amount":
					receipt.Logs[0].Data = fmt.Sprintf("%064x", 20010003)
				}
			})
			if scenario == "unresolved create" {
				pending, err := store.Prepare(ctx, principal, uuid.NewString(), "20.010001")
				if err != nil {
					test.Fatal(err)
				}
				pending, err = store.StartCreation(ctx, pending.ID)
				if err != nil {
					test.Fatal(err)
				}
				if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, ErrDepositAttributionPending) {
					test.Fatal("in-flight quote ignored", err)
				}
				pending, err = store.CreateFailed(ctx, pending.ID, uuid.UUID(pending.CreateToken.Bytes), true)
				if err != nil {
					test.Fatal(err)
				}
				if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, ErrDepositAttributionPending) {
					test.Fatal("unknown reservation ignored", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("unresolved attribution confirmed")
				}
				if _, err := store.BindQuote(ctx, pending.ID, uuid.UUID(pending.CreateToken.Bytes), depositTestQuote(pending, "20.010003")); err != nil {
					test.Fatal(err)
				}
			}
			if scenario == "corrupt proof" || scenario == "corrupt transfer" {
				statement := "UPDATE chain_scan_blocks SET evidence_hash=repeat('f',64)"
				if scenario == "corrupt transfer" {
					statement = "UPDATE chain_transfers SET raw_units='1'"
				}
				if _, err := pool.Exec(ctx, statement); err != nil {
					test.Fatal(err)
				}
				if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, ErrInvalidChainEvidence) {
					test.Fatal("corrupt persisted evidence accepted", err)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("corrupt evidence created receipt")
				}
				return
			}
			if scenario == "disabled" {
				if _, err := pool.Exec(ctx, "UPDATE users SET status='DISABLED' WHERE id=$1", principal.User.ID); err != nil {
					test.Fatal(err)
				}
			}
			if scenario == "overlap" {
				depositTestWaiting(test, store, principal)
			}
			if scenario == "rollback" {
				if _, err := pool.Exec(ctx, "ALTER TABLE deposit_events ADD CONSTRAINT fail_confirmation_audit CHECK(action <> 'PAYMENT_CONFIRMED')"); err != nil {
					test.Fatal(err)
				}
				if _, err := store.Confirm(ctx, order.ID, ref); err == nil {
					test.Fatal("audit failure accepted")
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("receipt leaked from rollback")
				}
				saved, err := store.Get(ctx, principal, order.ID)
				if err != nil || saved.Status != "WAITING" {
					test.Fatal(saved.Status, err)
				}
				return
			}
			if scenario != "confirm" && scenario != "unresolved create" {
				if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, ErrDepositReview) {
					test.Fatal(err)
				}
				var status, reason string
				if err := pool.QueryRow(ctx, "SELECT status,review_reason FROM deposit_orders WHERE id=$1", order.ID).Scan(&status, &reason); err != nil {
					test.Fatal(err)
				}
				expected := map[string]string{"ambiguous": "AMBIGUOUS_TRANSFER", "late": "PAYMENT_WINDOW", "wrong amount": "TRANSFER_MISMATCH", "disabled": "USER_DISABLED", "overlap": "RESERVATION_OVERLAP"}[scenario]
				if status != "REVIEW_REQUIRED" || reason != expected {
					test.Fatal(status, reason)
				}
				if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
					test.Fatal("review created receipt")
				}
				return
			}
			var wait sync.WaitGroup
			failures := make(chan error, 6)
			ids := make(chan uuid.UUID, 6)
			for index := 0; index < 6; index++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					receipt, err := store.Confirm(ctx, order.ID, ref)
					failures <- err
					ids <- receipt.ID
				}()
			}
			wait.Wait()
			close(failures)
			close(ids)
			for err := range failures {
				if err != nil {
					test.Fatal(err)
				}
			}
			var receiptID uuid.UUID
			for id := range ids {
				if receiptID != uuid.Nil && receiptID != id {
					test.Fatal("different confirmation receipts")
				}
				receiptID = id
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_events WHERE action='PAYMENT_CONFIRMED'"); count != 1 {
				test.Fatal(count)
			}
			if _, err := pool.Exec(ctx, "UPDATE deposit_receipts SET confirmed_units=confirmed_units+1"); err == nil {
				test.Fatal("receipt updated")
			}
			if _, err := pool.Exec(ctx, "DELETE FROM deposit_receipts"); err == nil {
				test.Fatal("receipt deleted")
			}
			if _, err := pool.Exec(ctx, "DELETE FROM deposit_events"); err == nil {
				test.Fatal("audit deleted")
			}
			if _, err := pool.Exec(ctx, "UPDATE deposit_orders SET status='WAITING' WHERE id=$1", order.ID); err == nil {
				test.Fatal("confirmed receipt detached")
			}
			ref.EventIndex = 1
			if _, err := store.Confirm(ctx, order.ID, ref); !errors.Is(err, domain.ErrConflict) {
				test.Fatal("confirmation ref changed", err)
			}
		})
	}
}
