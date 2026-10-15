package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type logNotification chan struct{}

func (notification logNotification) Write(data []byte) (int, error) {
	notification <- struct{}{}
	return len(data), nil
}

func TestDepositStopKeepsHTTPAvailable(t *testing.T) {
	for _, outcome := range []string{"failure", "unexpected_return", "closed_channel"} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deposits := make(chan error, 1)
			switch outcome {
			case "failure":
				deposits <- errors.New("unsupported transaction")
			case "unexpected_return":
				deposits <- nil
			case "closed_channel":
				close(deposits)
			}
			logged := make(logNotification, 1)
			done := make(chan error, 1)
			go func() {
				done <- waitForHTTP(ctx, server.Config, make(chan error), deposits, slog.New(slog.NewTextHandler(logged, nil)))
			}()
			select {
			case <-logged:
			case <-time.After(3 * time.Second):
				t.Fatal("deposit failure was not reported")
			}
			response, err := server.Client().Get(server.URL + "/api/v1/user/auth/login")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("HTTP unavailable after deposit failure: %d", response.StatusCode)
			}
			select {
			case err := <-done:
				t.Fatalf("HTTP lifecycle ended after deposit failure: %v", err)
			default:
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP did not shut down on cancellation")
			}
		})
	}
}

func TestHTTPListenerFailure(t *testing.T) {
	failures := make(chan error, 1)
	failures <- errors.New("address already in use")
	err := waitForHTTP(context.Background(), &http.Server{}, failures, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("HTTP listener failure was ignored")
	}
}
