package epusdt

import "testing"

func TestCheckoutOriginBinding(test *testing.T) {
	for _, origin := range []string{"http://gateway.example", "https://user@gateway.example", "https://gateway.example/path", "https://gateway.example?secret=test", "https://gateway.example#fragment"} {
		if _, err := New(Config{Environment: EnvironmentNile, BaseURL: "http://localhost:19800", PID: "1000", Secret: "synthetic-secret", CheckoutOrigin: origin}); err == nil {
			test.Fatal("unsafe checkout origin accepted", origin)
		}
	}
	client, err := New(Config{Environment: EnvironmentNile, BaseURL: "http://localhost:19800", PID: "1000", Secret: "synthetic-secret", CheckoutOrigin: "https://gateway.example/"})
	if err != nil || !client.MatchesMerchant("nile", "1000", "https://gateway.example") || client.MatchesMerchant("mainnet", "1000", "https://gateway.example") || client.MatchesMerchant("nile", "other", "https://gateway.example") || client.MatchesMerchant("nile", "1000", "http://localhost:19800") {
		test.Fatal("merchant checkout binding invalid", err)
	}
}
