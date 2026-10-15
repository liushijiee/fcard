package epusdt

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNileGatewayContract(t *testing.T) {
	baseURL := os.Getenv("EPUSDT_TEST_BASE_URL")
	credentialFile := os.Getenv("EPUSDT_TEST_CREDENTIALS_FILE")
	if baseURL == "" || credentialFile == "" {
		t.Skip("run scripts/test-epusdt-testnet.ps1 for the isolated gateway contract test")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		t.Fatal("contract probe requires its loopback-only test gateway")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		t.Fatal("refusing a non-loopback gateway")
	}
	encoded, err := os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal("cannot read local test credentials")
	}
	var credentials struct {
		Environment string
		Username    string
		Password    string
		PID         string
		Secret      string
	}
	if json.Unmarshal(encoded, &credentials) != nil || credentials.Environment != "nile" || credentials.Password == "" || credentials.PID == "" || credentials.Secret == "" {
		t.Fatal("invalid Nile test credentials")
	}
	var policy struct {
		Environment string `json:"environment"`
		Genesis     string `json:"genesis"`
		Contract    string `json:"contract"`
		Decimals    int    `json:"decimals"`
	}
	receiving := false
	policyJSON, policyErr := os.ReadFile(filepath.Join(filepath.Dir(credentialFile), "nile-receiving.json"))
	if policyErr == nil {
		if json.Unmarshal(policyJSON, &policy) != nil || policy.Environment != "nile" || policy.Genesis != "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc" || policy.Decimals != 6 || policy.Contract == "" {
			t.Fatal("invalid reviewed receiving policy")
		}
		receiving = true
	} else if !os.IsNotExist(policyErr) {
		t.Fatal("cannot read receiving policy")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path, contentType, body, token string, expected int) json.RawMessage {
		t.Helper()
		request, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("invalid probe request")
		}
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("test gateway request failed")
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		if err != nil || len(payload) > maxResponseBytes || response.StatusCode != expected {
			t.Fatalf("unexpected HTTP status: got %d, want %d", response.StatusCode, expected)
		}
		return payload
	}
	dataOf := func(payload json.RawMessage, expected int) json.RawMessage {
		t.Helper()
		var envelope struct {
			Code int             `json:"status_code"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &envelope) != nil || envelope.Code != expected {
			t.Fatalf("gateway business code mismatch: got %d, want %d", envelope.Code, expected)
		}
		return envelope.Data
	}
	t.Run("public config matches reviewed receiving policy", func(t *testing.T) {
		var config struct {
			Assets []struct {
				Network string
				Tokens  []string
			} `json:"supported_assets"`
			Version string
			OkPay   struct{ Enabled bool }
		}
		payload := dataOf(call("GET", "/payments/gmpay/v1/config", "", "", "", 200), 200)
		if json.Unmarshal(payload, &config) != nil || config.OkPay.Enabled || config.Version != "v2.0.0" {
			t.Fatal("unexpected public config or enabled payment assets")
		}
		if receiving {
			if len(config.Assets) != 1 || config.Assets[0].Network != "tron" || len(config.Assets[0].Tokens) != 1 || config.Assets[0].Tokens[0] != "USDT" {
				t.Fatal("unreviewed public receiving asset")
			}
		} else if len(config.Assets) != 0 {
			t.Fatal("receiving enabled without a policy")
		}
	})
	t.Run("unsafe channels are blocked", func(t *testing.T) {
		for _, path := range []string{"/pay/switch-network", "/payments/epay/v1/order/create-transaction/submit.php", "/payments/okpay/v1/notify", "/api/install"} {
			call("POST", path, "application/json", "{}", "", 403)
		}
	})
	params := map[string]string{"pid": credentials.PID, "order_id": "fcard_nile_signature_probe", "currency": "USDT", "token": "usdt", "network": "tron", "amount": "20.010001", "name": "Nile no-funds probe", "notify_url": "http://127.0.0.1:8081/api/v1/payments/epusdt/notify"}
	post := func(signature string) json.RawMessage {
		values := url.Values{}
		for key, value := range params {
			values.Set(key, value)
		}
		values.Set("signature", signature)
		expected := 400
		if signature == strings.Repeat("0", 64) || len(signature) == 32 {
			expected = 401
		}
		return call("POST", "/payments/gmpay/v1/order/create-transaction", "application/x-www-form-urlencoded", values.Encode(), "", expected)
	}
	t.Run("invalid HMAC and legacy signature rejected", func(t *testing.T) {
		dataOf(post(strings.Repeat("0", 64)), 401)
		dataOf(post(strings.Repeat("a", 32)), 401)
	})
	t.Run("valid HMAC reaches callback URL safety validation", func(t *testing.T) {
		dataOf(post(Sign(params, credentials.Secret)), 10041)
	})
	loginBody, _ := json.Marshal(map[string]string{"username": credentials.Username, "password": credentials.Password})
	var login struct{ Token string }
	if json.Unmarshal(dataOf(call("POST", "/admin/api/v1/auth/login", "application/json", string(loginBody), "", 200), 200), &login) != nil || login.Token == "" {
		t.Fatal("local admin authentication failed")
	}
	t.Run("only Nile RPC can be selected", func(t *testing.T) {
		var nodes []struct {
			Network string
			URL     string `json:"url"`
			Type    string
			Enabled bool
		}
		payload := dataOf(call("GET", "/admin/api/v1/rpc-nodes", "", "", login.Token, 200), 200)
		if json.Unmarshal(payload, &nodes) != nil {
			t.Fatal("invalid RPC response")
		}
		enabled := 0
		for _, node := range nodes {
			if node.Enabled {
				enabled++
				if node.Network != "tron" || node.URL != "https://nile.trongrid.io" || node.Type != "http" {
					t.Fatal("non-Nile RPC enabled")
				}
			}
		}
		if enabled != 1 {
			t.Fatal("expected exactly one Nile RPC")
		}
	})
	t.Run("enabled chain token matches reviewed policy", func(t *testing.T) {
		var tokens []struct {
			Enabled  bool
			Network  string
			Symbol   string
			Contract string `json:"contract_address"`
			Decimals int
		}
		payload := dataOf(call("GET", "/admin/api/v1/chain-tokens", "", "", login.Token, 200), 200)
		if json.Unmarshal(payload, &tokens) != nil {
			t.Fatal("invalid token response")
		}
		enabled := 0
		for _, token := range tokens {
			if token.Enabled {
				enabled++
				if !receiving || token.Network != "tron" || token.Symbol != "USDT" || token.Contract != policy.Contract || token.Decimals != 6 {
					t.Fatal("unreviewed token is enabled")
				}
			}
		}
		if receiving && enabled != 1 {
			t.Fatal("expected exactly one reviewed test token")
		}
	})
	t.Run("probes did not create a payable order", func(t *testing.T) {
		var orders struct{ Total int64 }
		payload := dataOf(call("GET", "/admin/api/v1/orders", "", "", login.Token, 200), 200)
		if json.Unmarshal(payload, &orders) != nil || orders.Total != 0 {
			t.Fatal("no-funds probes unexpectedly created an order")
		}
	})
}
