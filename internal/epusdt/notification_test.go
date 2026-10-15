package epusdt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

const callbackTestSecret = "callback-test-secret"
const callbackTestSignature = "2ccb03b8ef2f9c3341ab8260761bc3557d94ddfb8a8cdcc9750ed485f2738226"

func notificationPayload() []byte {
	return []byte(fmt.Sprintf(`{"pid":"1000","trade_id":"nile-test-trade","order_id":"0123456789abcdef0123456789abcdef","amount":20.010001,"actual_amount":20.010002,"receive_address":"TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf","token":"USDT","block_transaction_id":"%s","signature":"%s","status":2}`, strings.Repeat("ab", 32), callbackTestSignature))
}

func notificationVerifier(t testing.TB, secrets ...string) *CallbackVerifier {
	t.Helper()
	if len(secrets) == 0 {
		secrets = []string{callbackTestSecret}
	}
	keys := make([]CallbackKey, len(secrets))
	for index, secret := range secrets {
		keys[index] = CallbackKey{Version: fmt.Sprintf("test-key-%d", index), Secret: secret}
	}
	verifier, err := NewCallbackVerifier(CallbackConfig{Environment: EnvironmentNile, PID: "1000", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func resignNotification(t testing.TB, payload []byte, secret string) []byte {
	t.Helper()
	fields, err := decodeNotificationFields(payload)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for key, raw := range fields {
		if key == "amount" || key == "actual_amount" || key == "status" {
			value, err := strconv.ParseFloat(string(raw), 64)
			if err != nil {
				t.Fatal("invalid numeric fixture")
			}
			params[key] = strconv.FormatFloat(value, 'f', -1, 64)
		} else {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				t.Fatal("invalid string fixture")
			}
			params[key] = value
		}
	}
	fields["signature"], err = json.Marshal(Sign(params, secret))
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func expectedPayment() ExpectedPayment {
	return ExpectedPayment{Environment: EnvironmentNile, PID: "1000", Network: "tron", Transaction: Transaction{
		TradeID: "nile-test-trade", OrderID: validInput().OrderID, Amount: json.Number("20.010001"), ActualAmount: json.Number("20.010002"),
		Currency: "USDT", Token: "USDT", Status: 1, ReceiveAddress: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", ExpirationTime: time.Now().Add(time.Minute).Unix(),
	}}
}

func TestNotificationSignatureVectorAndQuote(t *testing.T) {
	body := notificationPayload()
	notification, err := notificationVerifier(t).Verify(body)
	if err != nil {
		t.Fatal(err)
	}
	if notification.Environment != EnvironmentNile || notification.KeyVersion != "test-key-0" || notification.AmountUnits != 20010001 || notification.ActualAmountUnits != 20010002 || notification.Status != 2 || len(notification.EventID) != 64 || string(notification.RawPayload) != string(body) {
		t.Fatal("incorrect notification values")
	}
	if err := MatchNotification(notification, expectedPayment()); err != nil {
		t.Fatal(err)
	}
	body[0] = '['
	if notification.RawPayload[0] != '{' {
		t.Fatal("stored payload aliases the caller buffer")
	}
}

func TestNotificationNumericCanonicalization(t *testing.T) {
	verifier := notificationVerifier(t)
	var eventID string
	for _, amounts := range [][2]string{{"100", "100"}, {"100.00", "1e2"}, {"1E+2", "100.000000"}} {
		payload := strings.ReplaceAll(string(notificationPayload()), "20.010001", amounts[0])
		payload = strings.ReplaceAll(payload, "20.010002", amounts[1])
		payload = strings.ReplaceAll(payload, callbackTestSignature, "51019fd06550be72dd7d589719a3b14a46b4558e3d4ca440f963d0901be550a0")
		notification, err := verifier.Verify([]byte(payload))
		if err != nil || notification.AmountUnits != 100000000 || notification.ActualAmountUnits != 100000000 {
			t.Fatalf("numeric canonicalization failed: %v", err)
		}
		if eventID != "" && notification.EventID != eventID {
			t.Fatal("equivalent amounts changed the inbox event identity")
		}
		eventID = notification.EventID
	}
}

func TestNotificationRejectsMalformedAndTamperedPayloads(t *testing.T) {
	base := string(notificationPayload())
	for _, example := range []struct {
		name string
		body string
		err  error
	}{
		{"empty", "", ErrInvalidNotification},
		{"array", "[]", ErrInvalidNotification},
		{"duplicate key", strings.Replace(base, `"pid":"1000"`, `"pid":"other","pid":"1000"`, 1), ErrInvalidNotification},
		{"escaped duplicate", strings.Replace(base, `"pid":"1000"`, `"p\u0069d":"other","pid":"1000"`, 1), ErrInvalidNotification},
		{"unknown field", strings.Replace(base, `"status":2`, `"status":2,"network":"tron"`, 1), ErrInvalidNotification},
		{"missing field", strings.Replace(base, `"status":2`, `"ignored":2`, 1), ErrInvalidNotification},
		{"trailing JSON", base + "{}", ErrInvalidNotification},
		{"invalid JSON", base[:len(base)-1], ErrInvalidNotification},
		{"null amount", strings.Replace(base, "20.010001", "null", 1), ErrInvalidNotification},
		{"string amount", strings.Replace(base, "20.010001", `"20.010001"`, 1), ErrInvalidNotification},
		{"nested amount", strings.Replace(base, "20.010001", "{}", 1), ErrInvalidNotification},
		{"too precise", strings.Replace(base, "20.010001", "20.010000999999999", 1), ErrInvalidNotification},
		{"float alias", strings.Replace(base, "20.010001", "20.01000100000000001", 1), ErrInvalidNotification},
		{"overflow", strings.Replace(base, "20.010001", "1e999", 1), ErrInvalidNotification},
		{"underflow", strings.Replace(base, "20.010001", "1e-999", 1), ErrInvalidNotification},
		{"amount boundary", strings.Replace(base, "20.010001", "0.01", 1), ErrInvalidNotification},
		{"oversized amount", strings.Replace(base, "20.010001", "1000001", 1), ErrInvalidNotification},
		{"invalid status", strings.Replace(base, `"status":2`, `"status":1`, 1), ErrInvalidNotification},
		{"fractional status", strings.Replace(base, `"status":2`, `"status":2.0`, 1), ErrInvalidNotification},
		{"malformed token", strings.Replace(base, `"token":"USDT"`, `"token":"USDT/TRX"`, 1), ErrInvalidNotification},
		{"bad address", strings.Replace(base, "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", "not-an-address", 1), ErrInvalidNotification},
		{"bad hash", strings.Replace(base, strings.Repeat("ab", 32), "0xabc", 1), ErrInvalidNotification},
		{"delimiter ambiguity", strings.Replace(base, "nile-test-trade", "nile-test-trade&status=2", 1), ErrInvalidNotification},
		{"wrong PID", strings.Replace(base, `"pid":"1000"`, `"pid":"1001"`, 1), ErrNotificationSignature},
		{"legacy signature", strings.Replace(base, callbackTestSignature, strings.Repeat("a", 32), 1), ErrNotificationSignature},
		{"uppercase signature", strings.Replace(base, callbackTestSignature, strings.ToUpper(callbackTestSignature), 1), ErrNotificationSignature},
		{"tampered quote", strings.ReplaceAll(strings.ReplaceAll(base, "20.010001", "21.010001"), "20.010002", "21.010002"), ErrNotificationSignature},
		{"oversized body", strings.Repeat(" ", maxNotificationBytes) + base, ErrInvalidNotification},
	} {
		t.Run(example.name, func(t *testing.T) {
			if _, err := notificationVerifier(t).Verify([]byte(example.body)); !errors.Is(err, example.err) {
				t.Fatalf("got %v, want %v", err, example.err)
			}
		})
	}
}

func TestNotificationKeyRotationPreservesEventID(t *testing.T) {
	rotated := resignNotification(t, notificationPayload(), "rotated-test-secret")
	verifier := notificationVerifier(t, "rotated-test-secret", callbackTestSecret)
	previous, oldErr := verifier.Verify(notificationPayload())
	current, newErr := verifier.Verify(rotated)
	if oldErr != nil || newErr != nil || previous.EventID != current.EventID {
		t.Fatal("rotation dropped a pending callback or changed event identity")
	}
	if previous.KeyVersion != "test-key-1" || current.KeyVersion != "test-key-0" {
		t.Fatal("verified callback key version was not recorded")
	}
	if _, err := notificationVerifier(t, "rotated-test-secret").Verify(notificationPayload()); !errors.Is(err, ErrNotificationSignature) {
		t.Fatal("retired callback key was accepted")
	}
}

func TestAuthenticatedQuoteMismatchRemainsEvidence(t *testing.T) {
	base := string(notificationPayload())
	for _, payload := range []string{
		strings.Replace(base, `"token":"USDT"`, `"token":"TRX"`, 1),
		strings.Replace(base, "20.010002", "20.01", 1),
		strings.Replace(base, "20.010002", "20.010101", 1),
		strings.Replace(base, "nile-test-trade", "another-trade", 1),
	} {
		notification, err := notificationVerifier(t).Verify(resignNotification(t, []byte(payload), callbackTestSecret))
		if err != nil || notification.EventID == "" || len(notification.RawPayload) == 0 {
			t.Fatal("authenticated mismatch discarded instead of being available to the inbox")
		}
		if !errors.Is(MatchNotification(notification, expectedPayment()), ErrQuoteMismatch) {
			t.Fatal("authenticated mismatch accepted as the saved quote")
		}
	}
}

func TestNotificationVerifierConfiguration(t *testing.T) {
	for _, config := range []CallbackConfig{
		{Environment: "mainnet", PID: "1000", Keys: []CallbackKey{{Version: "test", Secret: callbackTestSecret}}},
		{Environment: EnvironmentNile, Keys: []CallbackKey{{Version: "test", Secret: callbackTestSecret}}},
		{Environment: EnvironmentNile, PID: "1000&status=2", Keys: []CallbackKey{{Version: "test", Secret: callbackTestSecret}}},
		{Environment: EnvironmentNile, PID: "1000"},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Version: "test", Secret: ""}}},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Secret: callbackTestSecret}}},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Version: "invalid/version", Secret: callbackTestSecret}}},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Version: "a", Secret: "a"}, {Version: "b", Secret: "b"}, {Version: "c", Secret: "c"}}},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Version: "a", Secret: "a"}, {Version: "a", Secret: "b"}}},
		{Environment: EnvironmentNile, PID: "1000", Keys: []CallbackKey{{Version: "a", Secret: "a"}, {Version: "b", Secret: "a"}}},
	} {
		if _, err := NewCallbackVerifier(config); err == nil {
			t.Fatal("unsafe callback configuration accepted")
		}
	}
	keys := []CallbackKey{{Version: "test", Secret: callbackTestSecret}}
	verifier, err := NewCallbackVerifier(CallbackConfig{Environment: EnvironmentNile, PID: "1000", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	keys[0].Secret = "changed-by-caller"
	if _, err := verifier.Verify(notificationPayload()); err != nil {
		t.Fatal("callback key aliases the caller configuration")
	}
}

func TestMatchNotificationRejectsWrongSnapshot(t *testing.T) {
	notification, err := notificationVerifier(t).Verify(notificationPayload())
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range []struct {
		name   string
		mutate func(*ExpectedPayment)
	}{
		{"environment", func(expected *ExpectedPayment) { expected.Environment = "mainnet" }},
		{"PID", func(expected *ExpectedPayment) { expected.PID = "1001" }},
		{"network", func(expected *ExpectedPayment) { expected.Network = "ethereum" }},
		{"order", func(expected *ExpectedPayment) { expected.Transaction.OrderID = "different-order" }},
		{"trade", func(expected *ExpectedPayment) { expected.Transaction.TradeID = "different-trade" }},
		{"address", func(expected *ExpectedPayment) { expected.Transaction.ReceiveAddress = "another-address" }},
		{"amount", func(expected *ExpectedPayment) { expected.Transaction.Amount = json.Number("21") }},
		{"tail", func(expected *ExpectedPayment) { expected.Transaction.ActualAmount = json.Number("20.010003") }},
		{"currency", func(expected *ExpectedPayment) { expected.Transaction.Currency = "CNY" }},
		{"token", func(expected *ExpectedPayment) { expected.Transaction.Token = "TRX" }},
		{"placeholder", func(expected *ExpectedPayment) { expected.Transaction.Status = 4 }},
	} {
		t.Run(example.name, func(t *testing.T) {
			expected := expectedPayment()
			example.mutate(&expected)
			if !errors.Is(MatchNotification(notification, expected), ErrQuoteMismatch) {
				t.Fatal("mismatched snapshot accepted")
			}
		})
	}
	late := expectedPayment()
	late.Transaction.ExpirationTime = time.Now().Add(-time.Hour).Unix()
	if err := MatchNotification(notification, late); err != nil {
		t.Fatal("late callback evidence discarded instead of being preserved for review")
	}
}

func FuzzNotificationVerification(fuzzer *testing.F) {
	fuzzer.Add(notificationPayload())
	fuzzer.Add([]byte(`{"pid":"1000","pid":"1001"}`))
	fuzzer.Add([]byte("null"))
	verifier := notificationVerifier(fuzzer)
	fuzzer.Fuzz(func(t *testing.T, body []byte) {
		notification, err := verifier.Verify(body)
		if err == nil && (notification.Environment != EnvironmentNile || notification.PID != "1000" || notification.Status != 2 || notification.AmountUnits <= 10000 || notification.ActualAmountUnits <= 10000 || len(notification.EventID) != 64 || notification.KeyVersion == "") {
			t.Fatal("invalid verified notification")
		}
	})
}
