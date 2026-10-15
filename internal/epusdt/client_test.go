package epusdt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validInput() CreateRequest {
	return CreateRequest{OrderID: "0123456789abcdef0123456789abcdef", Amount: "20.010001", NotifyURL: "https://merchant.example/notify", Name: "Fcard Nile test"}
}

func TestCreateUsesFixedAssetAndFormSignature(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" || request.URL.Path != "/payments/gmpay/v1/order/create-transaction" {
			t.Error("incorrect route")
		}
		request.ParseForm()
		params := map[string]string{}
		for key := range request.PostForm {
			params[key] = request.PostForm.Get(key)
		}
		if !Verify(params, "secret", params["signature"]) || params["currency"] != "USDT" || params["token"] != "usdt" || params["network"] != "tron" || params["amount"] != "20.010001" || params["payment_type"] != "" {
			t.Error("uncontrolled parameters or invalid signature")
		}
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "data": Transaction{TradeID: "test-trade", OrderID: params["order_id"], Amount: json.Number("20.010001"), Currency: "USDT", ActualAmount: json.Number("20.010002"), ReceiveAddress: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", Token: "USDT", Status: 1, ExpirationTime: time.Now().Add(time.Minute).Unix(), PaymentURL: server.URL + "/pay/checkout-counter/test-trade"}})
	}))
	defer server.Close()
	client, err := New(Config{Environment: EnvironmentNile, BaseURL: server.URL, PID: "1000", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Create(context.Background(), validInput())
	if err != nil || result.ActualAmount.String() != "20.010002" {
		t.Fatalf("create failed: %v", err)
	}
}

func TestFailuresAreNotAutomaticallyRetriedOrLeaked(t *testing.T) {
	for _, example := range []struct {
		name        string
		status      int
		contentType string
		body        string
		ambiguous   bool
	}{
		{"duplicate", 400, "application/json", `{"status_code":10002,"message":"secret"}`, true},
		{"no wallet", 400, "application/json", `{"status_code":10003}`, false},
		{"bad signature", 401, "application/json", `{"status_code":401}`, false},
		{"unknown error", 400, "application/json", `{"status_code":99999}`, true},
		{"generic system error", 400, "application/json", `{"status_code":400}`, true},
		{"server failure", 503, "application/json", `{"message":"secret"}`, true},
		{"malformed", 200, "application/json", `{`, true},
		{"missing data", 200, "application/json", `{"status_code":200}`, true},
		{"placeholder", 200, "application/json", `{"status_code":200,"data":{"status":4}}`, true},
		{"html", 200, "text/html", "secret", true},
		{"oversized", 200, "application/json", strings.Repeat("x", maxResponseBytes+1), true},
	} {
		t.Run(example.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", example.contentType)
				writer.WriteHeader(example.status)
				fmt.Fprint(writer, example.body)
			}))
			defer server.Close()
			client, err := New(Config{Environment: EnvironmentNile, BaseURL: server.URL, PID: "1000", Secret: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Create(context.Background(), validInput())
			var failure *Failure
			if !errors.As(err, &failure) || failure.Ambiguous != example.ambiguous || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("incorrect failure: %v", err)
			}
		})
	}
}

func TestCreateDoesNotFollowRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, _ := New(Config{Environment: EnvironmentNile, BaseURL: source.URL, PID: "1000", Secret: "secret"})
	_, err := client.Create(context.Background(), validInput())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Ambiguous || targetCalls.Load() != 0 {
		t.Fatal("redirect followed or unknown result lost")
	}
}

func TestCreateRejectsMismatchedQuotes(t *testing.T) {
	for _, example := range []struct {
		name   string
		mutate func(*Transaction)
	}{
		{"wrong order", func(result *Transaction) { result.OrderID = "another-order" }},
		{"wrong token", func(result *Transaction) { result.Token = "TRX" }},
		{"wrong currency", func(result *Transaction) { result.Currency = "CNY" }},
		{"invalid address", func(result *Transaction) { result.ReceiveAddress = "not-a-tron-address" }},
		{"underpayment", func(result *Transaction) { result.ActualAmount = json.Number("20.01") }},
		{"excessive increment", func(result *Transaction) { result.ActualAmount = json.Number("20.010101") }},
		{"expired", func(result *Transaction) { result.ExpirationTime = time.Now().Add(-time.Minute).Unix() }},
		{"foreign checkout", func(result *Transaction) { result.PaymentURL = "https://other.example/pay/checkout-counter/test-trade" }},
		{"wrong checkout path", func(result *Transaction) { result.PaymentURL = "http://127.0.0.1/admin" }},
	} {
		t.Run(example.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				result := Transaction{TradeID: "test-trade", OrderID: validInput().OrderID, Amount: json.Number("20.010001"), Currency: "USDT", ActualAmount: json.Number("20.010001"), ReceiveAddress: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", Token: "USDT", Status: 1, ExpirationTime: time.Now().Add(time.Minute).Unix(), PaymentURL: server.URL + "/pay/checkout-counter/test-trade"}
				example.mutate(&result)
				writer.Header().Set("Content-Type", "application/json")
				json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "data": result})
			}))
			defer server.Close()
			client, err := New(Config{Environment: EnvironmentNile, BaseURL: server.URL, PID: "1000", Secret: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Create(context.Background(), validInput())
			var failure *Failure
			if !errors.As(err, &failure) || !failure.Ambiguous {
				t.Fatal("mismatched quote accepted or unknown result lost")
			}
		})
	}
}

func TestCanceledCreateRemainsUnknown(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := New(Config{Environment: EnvironmentNile, BaseURL: server.URL, PID: "1000", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Create(ctx, validInput())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Ambiguous || calls.Load() != 0 {
		t.Fatal("canceled request sent or unknown result lost")
	}
}

func TestValidationAndAmountNormalization(t *testing.T) {
	for _, raw := range []string{"1e2", "NaN", "-1", "0.1234567", "1000000.000001", "18446744073709551615", " 20"} {
		if _, _, err := normalizeAmount(raw); err == nil {
			t.Errorf("accepted amount %q", raw)
		}
	}
	if amount, units, err := normalizeAmount("00020.010000"); err != nil || amount != "20.01" || units != 20010000 {
		t.Fatal("incorrect integer normalization")
	}
	for _, config := range []Config{
		{Environment: "mainnet", BaseURL: "https://pay.example", PID: "1000", Secret: "secret"},
		{Environment: EnvironmentNile, BaseURL: "http://pay.example", PID: "1000", Secret: "secret"},
		{Environment: EnvironmentNile, BaseURL: "https://user:secret@pay.example", PID: "1000", Secret: "secret"},
		{Environment: EnvironmentNile, BaseURL: "https://pay.example/api", PID: "1000", Secret: "secret"},
	} {
		if _, err := New(config); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	client, _ := New(Config{Environment: EnvironmentNile, BaseURL: "http://127.0.0.1:1", PID: "1000", Secret: "secret"})
	input := validInput()
	input.OrderID = "01234567-89ab-cdef-0123-456789abcdef"
	if _, err := client.Create(context.Background(), input); err == nil {
		t.Fatal("oversized order ID accepted")
	}
	input = validInput()
	input.Amount = "0.01"
	if _, err := client.Create(context.Background(), input); err == nil {
		t.Fatal("HTTP lower boundary accepted")
	}
}
