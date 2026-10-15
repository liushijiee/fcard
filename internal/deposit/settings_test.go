package deposit

import (
	"testing"
)

func TestDepositSettings(test *testing.T) {
	test.Setenv("DEPOSIT_MAX_ORDER_AMOUNT", "")
	for _, name := range []string{"DEPOSIT_RUNTIME_ENABLED", "DEPOSIT_CREATE_ENABLED", "DEPOSIT_ENVIRONMENT", "DEPOSIT_ASSET_REVIEWED", "DEPOSIT_CONTRACT", "EPUSDT_BASE_URL", "EPUSDT_CHECKOUT_ORIGIN", "EPUSDT_PID", "EPUSDT_SECRET", "DEPOSIT_NOTIFY_URL", "DEPOSIT_RECEIVING_WALLETS", "DEPOSIT_SCAN_START_HEIGHT", "DEPOSIT_SCAN_START_BLOCK_ID", "DEPOSIT_SCANNER"} {
		test.Setenv(name, "")
	}
	if settings, err := SettingsFromEnvironment(); err != nil || settings.RuntimeEnabled || settings.CreateEnabled {
		test.Fatal(settings, err)
	}
	test.Setenv("DEPOSIT_CREATE_ENABLED", "true")
	if _, err := SettingsFromEnvironment(); err == nil {
		test.Fatal("create without runtime accepted")
	}
	test.Setenv("DEPOSIT_RUNTIME_ENABLED", "true")
	test.Setenv("DEPOSIT_ENVIRONMENT", "nile")
	test.Setenv("DEPOSIT_ASSET_REVIEWED", "true")
	test.Setenv("DEPOSIT_CONTRACT", "41"+"3333333333333333333333333333333333333333")
	test.Setenv("EPUSDT_BASE_URL", "http://localhost:19800")
	test.Setenv("EPUSDT_CHECKOUT_ORIGIN", "https://gateway.example")
	test.Setenv("EPUSDT_PID", "1000")
	test.Setenv("EPUSDT_SECRET", "synthetic-test-secret")
	test.Setenv("DEPOSIT_NOTIFY_URL", "https://fcard.example/api/v1/deposits/notify")
	test.Setenv("DEPOSIT_RECEIVING_WALLETS", testTronRecipient)
	test.Setenv("DEPOSIT_SCAN_START_HEIGHT", "0")
	test.Setenv("DEPOSIT_SCAN_START_BLOCK_ID", NileGenesisID)
	test.Setenv("DEPOSIT_SCANNER", "nile-tested-v2")
	if settings, err := SettingsFromEnvironment(); err != nil || !settings.CreateEnabled || settings.Order.Chain.Decimals != 6 || settings.Order.MaxOrderUnits != 1000000 {
		test.Fatal(err)
	}
	for _, invalid := range []struct{ name, value string }{{"DEPOSIT_ENVIRONMENT", "mainnet"}, {"DEPOSIT_ASSET_REVIEWED", "false"}, {"DEPOSIT_SCAN_START_HEIGHT", "1"}, {"DEPOSIT_RECEIVING_WALLETS", "invalid"}, {"DEPOSIT_NOTIFY_URL", "https://fcard.example/wrong"}, {"EPUSDT_CHECKOUT_ORIGIN", "http://localhost:19800"}} {
		test.Run(invalid.name, func(test *testing.T) {
			test.Setenv(invalid.name, invalid.value)
			if _, err := SettingsFromEnvironment(); err == nil {
				test.Fatal("unsafe setting accepted")
			}
		})
	}
	test.Setenv("DEPOSIT_MAX_ORDER_AMOUNT", "0.01")
	if _, err := SettingsFromEnvironment(); err == nil {
		test.Fatal("invalid test amount limit accepted")
	}
}
