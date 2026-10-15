package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fcard/internal/auth"
	"fcard/internal/deposit"
	"fcard/internal/epusdt"
	"fcard/internal/userauth"

	"github.com/google/uuid"
)

func TestDepositRoutesIntegration(test *testing.T) {
	fixture := newUserFixture(test)
	ctx := context.Background()
	profile := fixture.create(test, "deposit-routes@example.com")
	fixture.reset(test, profile.ID, temporaryPassword)
	if _, err := fixture.pool.Exec(ctx, "UPDATE user_credentials SET must_change_password=false WHERE user_id=$1", profile.ID); err != nil {
		test.Fatal(err)
	}
	login := fixture.login(test, profile.Email, temporaryPassword)
	principal, err := fixture.service.Authenticate(ctx, login.AccessToken)
	if err != nil {
		test.Fatal(err)
	}
	store, err := deposit.NewOrderStore(fixture.pool, deposit.OrderStoreConfig{Enabled: true, Chain: deposit.TronVerifierConfig{Environment: "nile", GenesisID: deposit.NileGenesisID, Contract: "41" + strings.Repeat("33", 20), Decimals: 6}, PID: "1000", NotifyURL: "https://fcard.example/api/v1/deposits/notify", GatewayOrigin: "https://gateway.example", MaxQuoteLifetime: 30 * time.Minute, Quarantine: 24 * time.Hour})
	if err != nil {
		test.Fatal(err)
	}
	client, err := epusdt.New(epusdt.Config{Environment: "nile", BaseURL: "https://gateway.example", PID: "1000", Secret: "synthetic-route-secret"})
	if err != nil {
		test.Fatal(err)
	}
	creator, err := deposit.NewCreateWorker(store, client, []string{"TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E"})
	if err != nil {
		test.Fatal(err)
	}
	service, err := deposit.NewService(store, creator)
	if err != nil {
		test.Fatal(err)
	}
	verifier, err := epusdt.NewCallbackVerifier(epusdt.CallbackConfig{Environment: "nile", PID: "1000", Keys: []epusdt.CallbackKey{{Version: "test", Secret: "synthetic-route-secret"}}})
	if err != nil {
		test.Fatal(err)
	}
	recorder, err := deposit.NewInboxRecorder(fixture.pool, verifier)
	if err != nil {
		test.Fatal(err)
	}
	callback, err := epusdt.NewCallbackHandler(verifier, recorder)
	if err != nil {
		test.Fatal(err)
	}
	adminService, err := auth.New(fixture.pool, time.Hour)
	if err != nil {
		test.Fatal(err)
	}
	router := NewWithDeposits(adminService, fixture.users, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), fixture.service, service, callback)
	expectStatus(test, send(router, "GET", "/api/v1/user/deposits", fixture.adminToken, ""), 401)
	expectStatus(test, send(router, "GET", "/api/v1/admin/deposits", login.AccessToken, ""), 401)
	expectStatus(test, send(router, "POST", "/api/v1/user/deposits", login.AccessToken, `{"amount":20}`), 400)
	expectStatus(test, send(router, "POST", "/api/v1/user/deposits", login.AccessToken, `{"amount":"20","contract":"unsafe"}`), 400)
	key := uuid.NewString()
	prepare := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/user/deposits", strings.NewReader(`{"amount":"20.010001"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+login.AccessToken)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := dataAs[deposit.Order](test, prepare(), 202)
	if replay := dataAs[deposit.Order](test, prepare(), 202); replay.ID != created.ID {
		test.Fatal("HTTP idempotency diverged")
	}
	response := send(router, "GET", "/api/v1/user/deposits/"+created.ID.String(), login.AccessToken, "")
	expectStatus(test, response, 200)
	if strings.Contains(response.Body.String(), "claim_token") || strings.Contains(response.Body.String(), "synthetic-route-secret") {
		test.Fatal("internal data leaked")
	}
	other := fixture.create(test, "other-deposit@example.com")
	fixture.reset(test, other.ID, temporaryPassword)
	if _, err := fixture.pool.Exec(ctx, "UPDATE user_credentials SET must_change_password=false WHERE user_id=$1", other.ID); err != nil {
		test.Fatal(err)
	}
	otherLogin := fixture.login(test, other.Email, temporaryPassword)
	expectStatus(test, send(router, "GET", "/api/v1/user/deposits/"+created.ID.String(), otherLogin.AccessToken, ""), 404)
	order, err := store.StartCreation(ctx, created.ID)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := store.CreateFailed(ctx, order.ID, uuid.UUID(order.CreateToken.Bytes), true); err != nil {
		test.Fatal(err)
	}
	quote := epusdt.Transaction{TradeID: "route-test-trade", OrderID: order.GatewayOrderID, Amount: "20.010001", ActualAmount: "20.010002", Currency: "USDT", Token: "USDT", ReceiveAddress: "TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E", Status: 1, ExpirationTime: time.Now().Add(15 * time.Minute).Unix(), PaymentURL: "https://gateway.example/pay/checkout-counter/route-test-trade"}
	input := deposit.RecoveryInput{ClaimToken: uuid.UUID(order.CreateToken.Bytes), Reason: "reviewed read-only original trade", EvidenceReference: "test-export/route-trade", Quote: quote}
	body, _ := json.Marshal(input)
	expectStatus(test, send(router, "POST", "/api/v1/admin/deposits/"+order.ID.String()+"/recover-quote", login.AccessToken, string(body)), 401)
	expectStatus(test, send(router, "POST", "/api/v1/admin/deposits/"+order.ID.String()+"/recover-quote", fixture.adminToken, string(body)), 200)
	params := map[string]string{"pid": "1000", "trade_id": quote.TradeID, "order_id": order.GatewayOrderID, "amount": "20.010001", "actual_amount": "20.010002", "receive_address": quote.ReceiveAddress, "token": "USDT", "status": "2", "block_transaction_id": strings.Repeat("ab", 32)}
	fields := map[string]any{}
	for name, value := range params {
		fields[name] = value
	}
	fields["amount"], fields["actual_amount"], fields["status"] = json.Number("20.010001"), json.Number("20.010002"), 2
	fields["signature"] = epusdt.Sign(params, "synthetic-route-secret")
	notification, _ := json.Marshal(fields)
	ack := send(router, "POST", "/api/v1/deposits/notify", "", string(notification))
	if ack.Code != 200 || ack.Body.String() != "ok" {
		test.Fatal("callback ACK", ack.Code, ack.Body.String())
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM deposit_callback_inbox").Scan(&count); err != nil || count != 1 {
		test.Fatal("ACK before durable inbox", count, err)
	}
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM deposit_receipts").Scan(&count); err != nil || count != 0 {
		test.Fatal("callback credited funds", count, err)
	}
	if _, err := fixture.pool.Exec(ctx, "ALTER TABLE deposit_events ADD CONSTRAINT reject_route_inbox CHECK(action <> 'CALLBACK_RECEIVED') NOT VALID"); err != nil {
		test.Fatal(err)
	}
	params["block_transaction_id"] = strings.Repeat("cd", 32)
	fields["block_transaction_id"], fields["signature"] = params["block_transaction_id"], epusdt.Sign(params, "synthetic-route-secret")
	notification, _ = json.Marshal(fields)
	expectStatus(test, send(router, "POST", "/api/v1/deposits/notify", "", string(notification)), 503)
	if err := fixture.pool.QueryRow(ctx, "SELECT count(*) FROM deposit_callback_inbox").Scan(&count); err != nil || count != 1 {
		test.Fatal("failed callback partially committed", err)
	}
	if _, err := service.Get(ctx, principal, order.ID); err != nil {
		test.Fatal(err)
	}
	if err := fixture.service.Logout(ctx, userauth.Principal{User: principal.User, SessionID: principal.SessionID, AuthVersion: principal.AuthVersion}, uuid.NewString()); err != nil {
		test.Fatal(err)
	}
	expectStatus(test, send(router, "GET", "/api/v1/user/deposits", login.AccessToken, ""), 401)
}
