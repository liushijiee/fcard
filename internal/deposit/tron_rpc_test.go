package deposit

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
)

func testTronRPC(t testing.TB, baseURL string) *TronRPC {
	t.Helper()
	rpc, err := NewTronRPC(TronRPCConfig{Environment: TronEnvironmentNile, BaseURL: baseURL, AllowLocalTestServer: true})
	if err != nil {
		t.Fatal(err)
	}
	return rpc
}

func TestTronRPCUsesOnlySolidityEndpoints(t *testing.T) {
	calls := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Num     *int64 `json:"num"`
			Value   string `json:"value"`
			Visible *bool  `json:"visible"`
		}
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" || json.NewDecoder(request.Body).Decode(&body) != nil || body.Visible == nil || *body.Visible {
			t.Error("invalid Solidity request contract")
			writer.WriteHeader(400)
			return
		}
		calls <- request.URL.Path
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch request.URL.Path {
		case "/walletsolidity/getblockbynum":
			if body.Num == nil {
				t.Error("missing block number")
				writer.WriteHeader(400)
				return
			}
			if *body.Num == 0 {
				genesis := testTronBlock(0)
				genesis.Header.RawData.Number = nil
				genesis.Header.RawData.Timestamp = nil
				json.NewEncoder(writer).Encode(genesis)
			} else {
				json.NewEncoder(writer).Encode(testTronBlock(*body.Num, testTronHash()))
			}
		case "/walletsolidity/getnowblock":
			json.NewEncoder(writer).Encode(testTronBlock(110))
		case "/walletsolidity/gettransactioninfobyid":
			if body.Value != testTronHash() {
				t.Error("unexpected transaction hash")
			}
			json.NewEncoder(writer).Encode(testTronReceipt())
		default:
			t.Error("non-Solidity endpoint used")
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	evidence, err := testTronVerifier(t, testTronRPC(t, server.URL)).ReadSolidTransaction(context.Background(), testTronHash())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MatchTronPayment(evidence, testTronExpected()); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"/walletsolidity/getblockbynum", "/walletsolidity/gettransactioninfobyid", "/walletsolidity/getnowblock", "/walletsolidity/getblockbynum"} {
		if actual := <-calls; actual != expected {
			t.Fatalf("got %s, want %s", actual, expected)
		}
	}
	if len(calls) != 0 {
		t.Fatal("unexpected extra request or automatic retry")
	}
}

func TestTronRPCRejectsUnsafeURLs(t *testing.T) {
	for _, config := range []TronRPCConfig{
		{Environment: "mainnet", BaseURL: "https://nile.trongrid.io"},
		{Environment: TronEnvironmentNile, BaseURL: "https://api.trongrid.io"},
		{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io.evil.example"},
		{Environment: TronEnvironmentNile, BaseURL: "http://nile.trongrid.io"},
		{Environment: TronEnvironmentNile, BaseURL: "https://user:secret@nile.trongrid.io"},
		{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io:443"},
		{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io/api"},
		{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io/?key=secret"},
		{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io/#fragment"},
		{Environment: TronEnvironmentNile, BaseURL: "http://127.0.0.1:8000"},
		{Environment: TronEnvironmentNile, BaseURL: "http://10.0.0.1", AllowLocalTestServer: true},
	} {
		if _, err := NewTronRPC(config); !errors.Is(err, ErrWrongChain) {
			t.Fatal("unsafe chain RPC URL accepted")
		}
	}
	if _, err := NewTronRPC(TronRPCConfig{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io"}); err != nil {
		t.Fatal("official Nile endpoint rejected")
	}
}

func TestTronRPCFailuresAndMalformedJSONDoNotProduceEvidence(t *testing.T) {
	for _, example := range []struct {
		name        string
		status      int
		contentType string
		body        string
		err         error
	}{
		{"throttled", 429, "application/json", `{"Error":"secret"}`, ErrChainUnavailable},
		{"server failure", 503, "application/json", "secret", ErrChainUnavailable},
		{"node error", 200, "application/json", `{"Error":"secret"}`, ErrChainUnavailable},
		{"lowercase node error", 200, "application/json", `{"error":"secret"}`, ErrChainUnavailable},
		{"html", 200, "text/html", "secret", ErrInvalidChainEvidence},
		{"malformed", 200, "application/json", "{", ErrInvalidChainEvidence},
		{"null", 200, "application/json", "null", ErrInvalidChainEvidence},
		{"array", 200, "application/json", "[]", ErrInvalidChainEvidence},
		{"empty receipt", 200, "application/json", "{}", ErrNotSolidified},
		{"duplicate id", 200, "application/json", `{"id":"first","id":"second"}`, ErrInvalidChainEvidence},
		{"duplicate execution result", 200, "application/json", `{"receipt":{"result":"FAILED","result":"SUCCESS"}}`, ErrInvalidChainEvidence},
		{"escaped duplicate", 200, "application/json", `{"id":"first","\u0069d":"second"}`, ErrInvalidChainEvidence},
		{"trailing JSON", 200, "application/json", "{}{}", ErrInvalidChainEvidence},
		{"wrong tx id", 200, "application/json", `{"id":"` + strings.Repeat("bb", 32) + `"}`, ErrInvalidChainEvidence},
		{"invalid number", 200, "application/json", `{"id":"` + testTronHash() + `","blockNumber":1e999}`, ErrInvalidChainEvidence},
		{"deep JSON", 200, "application/json", `{"nested":` + strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40) + "}", ErrInvalidChainEvidence},
		{"oversized", 200, "application/json", strings.Repeat("x", maxTronResponseBytes+1), ErrInvalidChainEvidence},
		{"invalid UTF8", 200, "application/json", "{\"id\":\"\xff\"}", ErrInvalidChainEvidence},
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
			_, err := testTronRPC(t, server.URL).SolidReceipt(context.Background(), testTronHash())
			if !errors.Is(err, example.err) || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("unsafe or retried response: %v", err)
			}
		})
	}
}

func TestTronRPCNeverFollowsRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if _, err := testTronRPC(t, source.URL).SolidReceipt(context.Background(), testTronHash()); !errors.Is(err, ErrChainUnavailable) || targetCalls.Load() != 0 {
		t.Fatal("RPC redirect followed")
	}
}

func TestTronRPCValidatesRequestedBlockAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(testTronBlock(101))
	}))
	defer server.Close()
	rpc := testTronRPC(t, server.URL)
	if _, err := rpc.SolidBlock(context.Background(), 100); !errors.Is(err, ErrInvalidChainEvidence) {
		t.Fatal("wrong requested height accepted")
	}
	if _, err := rpc.SolidBlock(context.Background(), -1); !errors.Is(err, ErrInvalidChainEvidence) {
		t.Fatal("negative height accepted")
	}
	if _, err := rpc.SolidReceipt(context.Background(), "bad"); !errors.Is(err, ErrInvalidChainEvidence) {
		t.Fatal("invalid transaction requested")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rpc.SolidHead(ctx); !errors.Is(err, ErrChainUnavailable) {
		t.Fatal("canceled RPC produced evidence")
	}
}

func FuzzTronRPCJSON(fuzzer *testing.F) {
	fuzzer.Add([]byte(`{"receipt":{"result":"SUCCESS"}}`))
	fuzzer.Add([]byte(`{"id":1,"id":2}`))
	fuzzer.Add([]byte(`{"array":[{"id":1},{"id":2}]}`))
	fuzzer.Fuzz(func(t *testing.T, body []byte) {
		if rejectDuplicateRPCKeys(body) == nil {
			var object map[string]json.RawMessage
			if json.Unmarshal(body, &object) != nil || object == nil {
				t.Fatal("nonobject or malformed RPC JSON accepted")
			}
		}
	})
}
