package epusdt

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type notificationRecordFunc func(context.Context, Notification) error

func (record notificationRecordFunc) Record(ctx context.Context, notification Notification) error {
	return record(ctx, notification)
}

func callbackRequest(body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payments/epusdt/notify", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request
}

func callbackHandler(t testing.TB, recorder NotificationRecorder) http.Handler {
	t.Helper()
	handler, err := NewCallbackHandler(notificationVerifier(t), recorder)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestCallbackAcknowledgesOnlyAfterRecorderReturns(t *testing.T) {
	for _, example := range []struct {
		name   string
		err    error
		status int
	}{
		{"committed", nil, http.StatusOK},
		{"failed commit", errors.New("database failed with secret data"), http.StatusServiceUnavailable},
	} {
		t.Run(example.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			handler := callbackHandler(t, notificationRecordFunc(func(ctx context.Context, notification Notification) error {
				if _, hasDeadline := ctx.Deadline(); !hasDeadline || notification.OrderID != validInput().OrderID {
					t.Error("missing storage deadline or verified notification")
				}
				close(started)
				<-release
				return example.err
			}))
			response := httptest.NewRecorder()
			go func() {
				handler.ServeHTTP(response, callbackRequest(notificationPayload()))
				close(finished)
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("callback did not reach the recorder")
			}
			select {
			case <-finished:
				t.Error("callback returned before the storage result")
			default:
			}
			close(release)
			<-finished
			if response.Code != example.status || strings.Contains(response.Body.String(), "secret") {
				t.Fatal("incorrect or sensitive callback response")
			}
			if example.err == nil && (response.Body.String() != "ok" || response.Header().Get("Content-Type") != "text/plain; charset=utf-8") {
				t.Fatal("callback ACK is not HTTP200 and plain text ok")
			}
		})
	}
}

func TestCallbackRejectsBeforeRecording(t *testing.T) {
	for _, example := range []struct {
		name        string
		method      string
		contentType string
		encoding    string
		body        []byte
		status      int
	}{
		{"GET", http.MethodGet, "application/json", "", notificationPayload(), http.StatusMethodNotAllowed},
		{"form", http.MethodPost, "application/x-www-form-urlencoded", "", notificationPayload(), http.StatusUnsupportedMediaType},
		{"missing type", http.MethodPost, "", "", notificationPayload(), http.StatusUnsupportedMediaType},
		{"compressed", http.MethodPost, "application/json", "gzip", notificationPayload(), http.StatusUnsupportedMediaType},
		{"malformed", http.MethodPost, "application/json", "", []byte("{"), http.StatusBadRequest},
		{"bad signature", http.MethodPost, "application/json", "", []byte(strings.Replace(string(notificationPayload()), callbackTestSignature, strings.Repeat("0", 64), 1)), http.StatusUnauthorized},
		{"oversized", http.MethodPost, "application/json", "", []byte(strings.Repeat("x", maxNotificationBytes+1)), http.StatusRequestEntityTooLarge},
	} {
		t.Run(example.name, func(t *testing.T) {
			var calls atomic.Int32
			handler := callbackHandler(t, notificationRecordFunc(func(context.Context, Notification) error { calls.Add(1); return nil }))
			request := httptest.NewRequest(example.method, "/notify", bytes.NewReader(example.body))
			request.Header.Set("Content-Type", example.contentType)
			request.Header.Set("Content-Encoding", example.encoding)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != example.status || calls.Load() != 0 || response.Body.String() == "ok" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("invalid callback recorded or acknowledged")
			}
			if example.method == http.MethodGet && response.Header().Get("Allow") != http.MethodPost {
				t.Fatal("missing method allow header")
			}
		})
	}
}

func TestCallbackReplayUsesIdempotentRecorderContract(t *testing.T) {
	var mutex sync.Mutex
	events := map[string]Notification{}
	handler := callbackHandler(t, notificationRecordFunc(func(ctx context.Context, notification Notification) error {
		mutex.Lock()
		defer mutex.Unlock()
		events[notification.EventID] = notification
		return nil
	}))
	var workers sync.WaitGroup
	for attempt := 0; attempt < 8; attempt++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, callbackRequest(notificationPayload()))
			if response.Code != http.StatusOK || response.Body.String() != "ok" {
				t.Error("committed replay was not acknowledged")
			}
		}()
	}
	workers.Wait()
	if len(events) != 1 {
		t.Fatal("replayed callback generated multiple inbox identities")
	}
}

func TestCallbackCanceledStorageIsNotAcknowledged(t *testing.T) {
	handler := callbackHandler(t, notificationRecordFunc(func(ctx context.Context, notification Notification) error { return ctx.Err() }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := callbackRequest(notificationPayload()).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() == "ok" {
		t.Fatal("failed canceled write was acknowledged")
	}
}

func TestCallbackRecordsAuthenticatedMismatchBeforeACK(t *testing.T) {
	var recorded atomic.Int32
	handler := callbackHandler(t, notificationRecordFunc(func(ctx context.Context, notification Notification) error {
		if !errors.Is(MatchNotification(notification, expectedPayment()), ErrQuoteMismatch) {
			t.Error("mock mismatch was not quarantined for review")
		}
		recorded.Add(1)
		return nil
	}))
	payload := strings.Replace(string(notificationPayload()), `"token":"USDT"`, `"token":"TRX"`, 1)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, callbackRequest(resignNotification(t, []byte(payload), callbackTestSecret)))
	if response.Code != http.StatusOK || response.Body.String() != "ok" || recorded.Load() != 1 {
		t.Fatal("authenticated mismatch was dropped or ACKed without recording")
	}
}

func TestCallbackRequiresRecorderAndVerifier(t *testing.T) {
	if _, err := NewCallbackHandler(notificationVerifier(t), nil); err == nil {
		t.Fatal("callback can ACK without a recorder")
	}
	if _, err := NewCallbackHandler(nil, notificationRecordFunc(func(context.Context, Notification) error { return nil })); err == nil {
		t.Fatal("callback can ACK without verification")
	}
}
