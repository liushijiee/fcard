package airwallex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientAuthenticationCachingAndControls(t *testing.T) {
	var authentications atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/v1/authentication/login" {
			authentications.Add(1)
			if request.Header.Get("x-client-id") != "client" || request.Header.Get("x-api-key") != "secret" {
				t.Error("missing server credentials")
			}
			fmt.Fprintf(writer, `{"token":"cached-token","expires_at":%q}`, time.Now().Add(time.Hour).Format("2006-01-02T15:04:05-0700"))
			return
		}
		if request.Header.Get("Authorization") != "Bearer cached-token" || request.Header.Get("x-api-version") != "2026-08-21" {
			t.Error("missing provider headers")
		}
		if request.URL.Path == "/api/v1/issuing/cards/create" {
			var payload CreateRequest
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Error("invalid payload")
			}
			if payload.Controls.Limits.Limits[0].Amount.String() != "0.01" || payload.Controls.Limits.Limits[0].Interval != "ALL_TIME" || payload.Controls.Currencies[0] != "USD" || payload.Metadata["fcard_card_id"] != "card" || payload.Metadata["fcard_user_id"] != "" || payload.Personalized || payload.Program.Purpose != "COMMERCIAL" {
				t.Error("uncontrolled card creation")
			}
		}
		fmt.Fprint(writer, `{"card_id":"card","cardholder_id":"holder"}`)
	}))
	defer server.Close()
	client := New(Config{ClientID: "client", APIKey: "secret", APIVersion: "2026-08-21"})
	client.baseURL = server.URL
	payload, err := json.Marshal(BuildRequest("holder", "John Smith", "COMMERCIAL", "request", "nickname", "card", "operation", 1))
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for index := 0; index < 6; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := client.CreateCard(context.Background(), payload); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if authentications.Load() != 1 {
		t.Fatal("token not cached across concurrent requests")
	}
}

func TestProviderErrorsDoNotLeakOrReplay(t *testing.T) {
	for _, status := range []int{400, 401, 403, 409, 429, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Location", "https://example.invalid/secret")
				writer.WriteHeader(status)
				fmt.Fprint(writer, `{"message":"secret PAN 4111111111111111"}`)
			}))
			defer server.Close()
			client := New(Config{APIVersion: "2026-08-21"})
			client.baseURL, client.token, client.expires = server.URL, "token", time.Now().Add(time.Hour)
			_, err := client.CreateCard(context.Background(), json.RawMessage(`{}`))
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "4111") {
				t.Fatal("provider error leaked")
			}
			if calls.Load() != 1 {
				t.Fatal("unsafe automatic replay")
			}
			if status == 401 && client.token != "" {
				t.Fatal("expired token retained")
			}
		})
	}
}

func TestMalformedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, "not-json-with-secret") }))
	defer server.Close()
	client := New(Config{})
	client.baseURL, client.token, client.expires = server.URL, "token", time.Now().Add(time.Hour)
	if _, err := client.GetCard(context.Background(), "card"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("malformed body leaked")
	}
}
