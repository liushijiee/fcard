package epusdt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommittedCreateWithLostResponseRemainsUnknown(t *testing.T) {
	var committedOrders atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ParseForm() != nil || request.PostForm.Get("order_id") != validInput().OrderID {
			t.Error("unexpected create request")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		committedOrders.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("could not simulate a lost response")
			return
		}
		connection.Close()
	}))
	defer provider.Close()
	client, err := New(Config{Environment: EnvironmentNile, BaseURL: provider.URL, PID: "1000", Secret: callbackTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Create(context.Background(), validInput())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Ambiguous || committedOrders.Load() != 1 {
		t.Fatal("lost response was treated as a rejected order or create was replayed")
	}
}

func TestCallbackBeforeCreateResponsePreservesEvidence(t *testing.T) {
	recorded := make(chan Notification, 1)
	handler := callbackHandler(t, notificationRecordFunc(func(ctx context.Context, notification Notification) error {
		recorded <- notification
		return nil
	}))
	merchant := httptest.NewServer(handler)
	defer merchant.Close()
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		callback, err := http.NewRequestWithContext(request.Context(), http.MethodPost, merchant.URL, bytes.NewReader(notificationPayload()))
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		callback.Header.Set("Content-Type", "application/json")
		response, err := (&http.Client{Timeout: time.Second}).Do(callback)
		if err != nil {
			t.Error("mock callback delivery failed")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		ack, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || string(ack) != "ok" {
			t.Error("callback was not preserved before create returned")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		quote := expectedPayment().Transaction
		quote.PaymentURL = provider.URL + "/pay/checkout-counter/" + quote.TradeID
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "data": quote})
	}))
	defer provider.Close()
	client, err := New(Config{Environment: EnvironmentNile, BaseURL: provider.URL, PID: "1000", Secret: callbackTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := client.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-recorded:
		if err := MatchNotification(notification, ExpectedPayment{Environment: EnvironmentNile, PID: "1000", Network: "tron", Transaction: quote}); err != nil {
			t.Fatal("preserved callback cannot be matched after the quote arrives")
		}
	default:
		t.Fatal("callback evidence was discarded while the create result was unknown")
	}
}
