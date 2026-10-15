package deposit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/storage/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDepositAmountExactUnits(test *testing.T) {
	for _, sample := range []struct {
		amount string
		units  int64
	}{{"0.010001", 10001}, {"20.010001", 20010001}, {"20.010000", 20010000}, {"1000000", 1000000000000}, {"1", 1000000}} {
		units, err := depositUnits(sample.amount)
		if err != nil || units != sample.units {
			test.Fatalf("%s: %d %v", sample.amount, units, err)
		}
		roundtrip, err := depositUnits(depositAmount(units))
		if err != nil || roundtrip != units {
			test.Fatal("amount roundtrip failed")
		}
	}
	for _, amount := range []string{"0", "0.01", "-1", "+1", "01", "1e2", "1.0000001", "1000000.000001", "NaN", " 1", "1."} {
		if _, err := depositUnits(amount); !errors.Is(err, domain.ErrInvalid) {
			test.Fatalf("accepted %q", amount)
		}
	}
}

func TestDepositQuoteSnapshot(test *testing.T) {
	started := time.Now().UTC()
	order := db.DepositOrder{RequestedUnits: 20010001, GatewayOrderID: strings.Repeat("a", 32), GatewayOrigin: "https://gateway.example", MaxQuoteSeconds: 1800, CreateStartedAt: pgtype.Timestamptz{Time: started, Valid: true}}
	quote := epusdt.Transaction{OrderID: order.GatewayOrderID, TradeID: "synthetic-trade", Amount: json.Number("20.010001"), ActualAmount: json.Number("20.010002"), ReceiveAddress: testTronRecipient, Token: "USDT", Currency: "USDT", Status: 1, ExpirationTime: started.Add(15 * time.Minute).Unix(), PaymentURL: "https://gateway.example/pay/checkout-counter/synthetic-trade"}
	if _, _, _, _, err := normalizeDepositQuote(order, quote); err != nil {
		test.Fatal(err)
	}
	for _, sample := range []struct {
		name   string
		change func(*epusdt.Transaction)
	}{
		{"wrong order", func(candidate *epusdt.Transaction) { candidate.OrderID = strings.Repeat("b", 32) }},
		{"wrong amount", func(candidate *epusdt.Transaction) { candidate.Amount = "20" }},
		{"amount exponent", func(candidate *epusdt.Transaction) { candidate.Amount = "2.0010001e1" }},
		{"underpayment", func(candidate *epusdt.Transaction) { candidate.ActualAmount = "20.01" }},
		{"offset beyond limit", func(candidate *epusdt.Transaction) { candidate.ActualAmount = "20.010101" }},
		{"bad checksum", func(candidate *epusdt.Transaction) { candidate.ReceiveAddress = testTronRecipient[:33] + "1" }},
		{"hex instead of gateway address", func(candidate *epusdt.Transaction) { candidate.ReceiveAddress = testTronRecipientHex() }},
		{"wrong token", func(candidate *epusdt.Transaction) { candidate.Token = "TRX" }},
		{"wrong status", func(candidate *epusdt.Transaction) { candidate.Status = 2 }},
		{"foreign checkout", func(candidate *epusdt.Transaction) {
			candidate.PaymentURL = "https://evil.example/pay/checkout-counter/test"
		}},
		{"checkout credentials", func(candidate *epusdt.Transaction) {
			candidate.PaymentURL = "https://user@gateway.example/pay/checkout-counter/test"
		}},
		{"expired before creation", func(candidate *epusdt.Transaction) { candidate.ExpirationTime = started.Add(-time.Second).Unix() }},
		{"extended expiry", func(candidate *epusdt.Transaction) { candidate.ExpirationTime = started.Add(time.Hour).Unix() }},
	} {
		test.Run(sample.name, func(test *testing.T) {
			candidate := quote
			sample.change(&candidate)
			if _, _, _, _, err := normalizeDepositQuote(order, candidate); !errors.Is(err, epusdt.ErrQuoteMismatch) {
				test.Fatal("unsafe quote accepted", err)
			}
		})
	}
}
