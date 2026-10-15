package epusdt

import (
	"strings"
	"testing"
)

func TestOfficialSignatureVector(t *testing.T) {
	params := map[string]string{"pid": "1000", "order_id": "ORD202605230001", "currency": "cny", "token": "usdt", "network": "tron", "amount": "100", "notify_url": "https://merchant.example/notify", "redirect_url": "https://merchant.example/return", "name": "VIP", "empty": "", "signature": "ignored"}
	expected := "6f874b1919d95081835e2809b620e354a5866f5a6dbb2e432d1627f1eb10059d"
	if actual := Sign(params, "epusdt_secret_key"); actual != expected {
		t.Fatalf("signature mismatch: %s", actual)
	}
	if !Verify(params, "epusdt_secret_key", expected) {
		t.Fatal("valid signature rejected")
	}
	for _, signature := range []string{strings.ToUpper(expected), expected[:63], strings.Repeat("x", 64)} {
		if Verify(params, "epusdt_secret_key", signature) {
			t.Fatal("invalid signature accepted")
		}
	}
	params["amount"] = "101"
	if Verify(params, "epusdt_secret_key", expected) {
		t.Fatal("tampered amount accepted")
	}
	if Verify(params, "", expected) {
		t.Fatal("empty key accepted")
	}
}

func TestSignatureIncludesPIDAndPaymentType(t *testing.T) {
	params := map[string]string{"pid": "1000", "amount": "20.010001"}
	initial := Sign(params, "secret")
	params["pid"] = "1001"
	if Sign(params, "secret") == initial {
		t.Fatal("PID omitted")
	}
	params["payment_type"] = "Gmpay"
	withPaymentType := Sign(params, "secret")
	delete(params, "payment_type")
	if Sign(params, "secret") == withPaymentType {
		t.Fatal("payment type omitted")
	}
}
