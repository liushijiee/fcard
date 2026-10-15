package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"

	"github.com/google/uuid"
)

func TestCreateWorkerIntegration(test *testing.T) {
	for _, scenario := range []string{"success", "ambiguous", "response_lost", "canceled", "rejected", "unapproved", "bind_failure", "claim_failure", "claimed_restart", "payload", "authorization", "disabled", "config_changed"} {
		test.Run(scenario, func(test *testing.T) {
			pool := chainIntegrationDatabase(test)
			store := depositTestStore(test, pool)
			principal := depositTestPrincipal(test, pool)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			order, err := store.Prepare(ctx, principal, uuid.NewString(), "20.010001")
			if err != nil {
				test.Fatal(err)
			}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				posts.Add(1)
				if request.Method != http.MethodPost || request.ParseForm() != nil || request.Form.Get("order_id") != order.GatewayOrderID {
					test.Error("unexpected create request")
				}
				writer.Header().Set("Content-Type", "application/json")
				if scenario == "response_lost" {
					connection, _, err := writer.(http.Hijacker).Hijack()
					if err != nil {
						test.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				if scenario == "canceled" {
					cancel()
					<-request.Context().Done()
					return
				}
				if scenario == "ambiguous" {
					writer.WriteHeader(503)
					return
				}
				if scenario == "rejected" {
					writer.WriteHeader(400)
					_, _ = writer.Write([]byte(`{"status_code":10003}`))
					return
				}
				quote := epusdt.Transaction{OrderID: order.GatewayOrderID, TradeID: "trade-" + order.ID.String(), Amount: "20.010001", ActualAmount: "20.010002", ReceiveAddress: testTronRecipient, Currency: "USDT", Token: "USDT", Status: 1, ExpirationTime: time.Now().Add(15 * time.Minute).Unix(), PaymentURL: "https://gateway.example/pay/checkout-counter/" + order.GatewayOrderID}
				_ = json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "data": quote})
			}))
			defer server.Close()
			client, err := epusdt.New(epusdt.Config{Environment: "nile", PID: "1000", Secret: "synthetic-test-secret", BaseURL: server.URL, CheckoutOrigin: "https://gateway.example"})
			if err != nil {
				test.Fatal(err)
			}
			wallet := testTronRecipient
			if scenario == "unapproved" {
				wallet = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
			}
			worker, err := NewCreateWorker(store, client, []string{wallet})
			if err != nil {
				test.Fatal(err)
			}
			switch scenario {
			case "claimed_restart":
				if _, err := store.StartCreation(ctx, order.ID); err != nil {
					test.Fatal(err)
				}
			case "payload":
				jobTestExec(test, pool, "UPDATE deposit_jobs SET payload=payload || '{\"unexpected\":true}'::jsonb WHERE kind='CREATE'")
			case "authorization":
				jobTestExec(test, pool, "UPDATE user_credentials SET auth_version=2 WHERE user_id=$1", principal.User.ID)
			case "disabled":
				store.config.Enabled = false
			case "config_changed":
				store.config.NotifyURL = "https://other.example/notify"
			case "bind_failure":
				jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_quote CHECK(action <> 'QUOTE_BOUND')")
			case "claim_failure":
				jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_claim CHECK(action <> 'CREATE_STARTED')")
			}
			var wait sync.WaitGroup
			for index := 0; index < 6; index++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					_, _ = worker.Process(ctx, order.ID)
				}()
			}
			wait.Wait()
			_, _ = worker.Process(ctx, order.ID)
			current, err := store.Get(context.Background(), principal, order.ID)
			if scenario == "authorization" {
				if !errors.Is(err, domain.ErrUnauthorized) {
					test.Fatal(err)
				}
			} else if err != nil {
				test.Fatal(err)
			}
			expectedPosts, expectedState := int32(1), "WAITING"
			switch scenario {
			case "ambiguous", "response_lost", "canceled", "unapproved", "bind_failure":
				expectedState = "CREATE_UNKNOWN"
			case "rejected":
				expectedState = "FAILED"
			case "claim_failure", "claimed_restart", "disabled":
				expectedPosts, expectedState = 0, "CREATING"
			case "payload", "authorization", "config_changed":
				expectedPosts, expectedState = 0, "REVIEW_REQUIRED"
			}
			if posts.Load() != expectedPosts || scenario != "authorization" && current.Status != expectedState {
				test.Fatal("unexpected send count/state", posts.Load(), current.Status)
			}
			if scenario == "ambiguous" || scenario == "claimed_restart" {
				second, err := store.Prepare(ctx, principal, uuid.NewString(), "21")
				if err != nil {
					test.Fatal(err)
				}
				if _, err := worker.Process(ctx, second.ID); !errors.Is(err, ErrDepositAttributionPending) {
					test.Fatal("scope backpressure missing", err)
				}
				if posts.Load() != expectedPosts {
					test.Fatal("backpressure sent POST")
				}
			}
			if count := depositTestCount(test, pool, "SELECT count(*) FROM deposit_receipts"); count != 0 {
				test.Fatal("create credited funds")
			}
		})
	}
}

func TestCreateStaleClaimIntegration(test *testing.T) {
	pool := chainIntegrationDatabase(test)
	store := depositTestStore(test, pool)
	principal := depositTestPrincipal(test, pool)
	ctx := context.Background()
	client, err := epusdt.New(epusdt.Config{Environment: "nile", PID: "1000", Secret: "synthetic-secret", BaseURL: "https://gateway.example"})
	if err != nil {
		test.Fatal(err)
	}
	worker, err := NewCreateWorker(store, client, []string{testTronRecipient})
	if err != nil {
		test.Fatal(err)
	}
	order, err := store.Prepare(ctx, principal, uuid.NewString(), "20.010001")
	if err != nil {
		test.Fatal(err)
	}
	order, err = store.StartCreation(ctx, order.ID)
	if err != nil {
		test.Fatal(err)
	}
	if count, err := worker.RecoverStaleClaims(ctx, time.Minute); err != nil || count != 0 {
		test.Fatal("fresh claim recovered", count, err)
	}
	if _, err := worker.RecoverStaleClaims(ctx, time.Second); !errors.Is(err, domain.ErrInvalid) {
		test.Fatal(err)
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_orders DISABLE TRIGGER deposit_snapshot_immutable")
	jobTestExec(test, pool, "UPDATE deposit_orders SET create_started_at=now()-interval '2 minutes' WHERE id=$1", order.ID)
	jobTestExec(test, pool, "ALTER TABLE deposit_orders ENABLE TRIGGER deposit_snapshot_immutable")
	jobTestExec(test, pool, "ALTER TABLE deposit_events ADD CONSTRAINT reject_unknown CHECK(action <> 'CREATE_UNKNOWN')")
	if _, err := worker.RecoverStaleClaims(ctx, time.Minute); err == nil {
		test.Fatal("recovery audit failure accepted")
	}
	jobTestExec(test, pool, "ALTER TABLE deposit_events DROP CONSTRAINT reject_unknown")
	if count, err := worker.RecoverStaleClaims(ctx, time.Minute); err != nil || count != 1 {
		test.Fatal(count, err)
	}
	current, err := store.Get(ctx, principal, order.ID)
	if err != nil || current.Status != "CREATE_UNKNOWN" || current.CreateToken != order.CreateToken {
		test.Fatal("claim was reset", current.Status, err)
	}
	if count, err := worker.RecoverStaleClaims(ctx, time.Minute); err != nil || count != 0 {
		test.Fatal("recovery not idempotent", count, err)
	}
	if _, err := worker.Process(ctx, order.ID); !errors.Is(err, domain.ErrInvalidState) {
		test.Fatal("recovered claim authorized send", err)
	}
}
