package deposit

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"fcard/internal/epusdt"
)

const NileGenesisID = "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc"

type Settings struct {
	RuntimeEnabled bool
	CreateEnabled  bool
	Order          OrderStoreConfig
	Gateway        epusdt.Config
	Wallets        []string
	Checkpoint     ScanCheckpoint
	Scanner        string
}

func SettingsFromEnvironment() (Settings, error) {
	settings := Settings{}
	for name, target := range map[string]*bool{"DEPOSIT_RUNTIME_ENABLED": &settings.RuntimeEnabled, "DEPOSIT_CREATE_ENABLED": &settings.CreateEnabled} {
		value := os.Getenv(name)
		if value != "" && value != "true" && value != "false" {
			return Settings{}, errors.New("deposit switches must be true or false")
		}
		*target = value == "true"
	}
	if !settings.RuntimeEnabled {
		if settings.CreateEnabled {
			return Settings{}, errors.New("deposit creation requires deposit runtime")
		}
		return settings, nil
	}
	if os.Getenv("DEPOSIT_ENVIRONMENT") != "nile" || os.Getenv("DEPOSIT_ASSET_REVIEWED") != "true" {
		return Settings{}, errors.New("Nile and explicitly reviewed test asset are required")
	}
	contract, err := canonicalTronAddress(os.Getenv("DEPOSIT_CONTRACT"))
	if err != nil || contract == "410000000000000000000000000000000000000000" {
		return Settings{}, errors.New("reviewed Nile token contract is required")
	}
	settings.Gateway = epusdt.Config{Environment: "nile", BaseURL: os.Getenv("EPUSDT_BASE_URL"), CheckoutOrigin: os.Getenv("EPUSDT_CHECKOUT_ORIGIN"), PID: os.Getenv("EPUSDT_PID"), Secret: os.Getenv("EPUSDT_SECRET")}
	client, err := epusdt.New(settings.Gateway)
	if err != nil {
		return Settings{}, errors.New("invalid deposit gateway configuration")
	}
	origin, err := url.Parse(settings.Gateway.CheckoutOrigin)
	if err != nil || origin == nil || origin.Scheme != "https" {
		return Settings{}, errors.New("explicit HTTPS checkout origin required")
	}
	settings.Gateway.CheckoutOrigin = origin.Scheme + "://" + origin.Host
	if !client.MatchesMerchant("nile", settings.Gateway.PID, settings.Gateway.CheckoutOrigin) {
		return Settings{}, errors.New("invalid merchant binding")
	}
	notify := os.Getenv("DEPOSIT_NOTIFY_URL")
	parsed, err := url.Parse(notify)
	if err != nil || !depositURL(notify, false) || parsed.Path != "/api/v1/deposits/notify" || parsed.RawQuery != "" {
		return Settings{}, errors.New("HTTPS callback must use /api/v1/deposits/notify")
	}
	wallets := strings.Split(os.Getenv("DEPOSIT_RECEIVING_WALLETS"), ",")
	if len(wallets) > 100 {
		return Settings{}, errors.New("too many deposit wallets")
	}
	for _, wallet := range wallets {
		address, err := canonicalTronAddress(wallet)
		if err != nil || len(wallet) != 34 || wallet[0] != 'T' || address == "410000000000000000000000000000000000000000" {
			return Settings{}, errors.New("approved public Nile receiving wallets required")
		}
	}
	settings.Wallets = wallets
	height, err := strconv.ParseInt(os.Getenv("DEPOSIT_SCAN_START_HEIGHT"), 10, 64)
	hash, hashErr := canonicalTronHash(os.Getenv("DEPOSIT_SCAN_START_BLOCK_ID"))
	if err != nil || height < 0 || hashErr != nil {
		return Settings{}, errors.New("explicit reviewed scan checkpoint required")
	}
	decoded, _ := hex.DecodeString(hash)
	if binary.BigEndian.Uint64(decoded[:8]) != uint64(height) || height == 0 && hash != NileGenesisID {
		return Settings{}, errors.New("scan checkpoint height mismatch")
	}
	settings.Checkpoint = ScanCheckpoint{Height: height, BlockID: hash}
	settings.Scanner = os.Getenv("DEPOSIT_SCANNER")
	if !scannerNamePattern.MatchString(settings.Scanner) {
		return Settings{}, errors.New("stable deposit scanner name required")
	}
	settings.Order = OrderStoreConfig{Enabled: settings.CreateEnabled, Chain: TronVerifierConfig{Environment: "nile", GenesisID: NileGenesisID, Contract: contract, Decimals: 6}, PID: settings.Gateway.PID, NotifyURL: notify, GatewayOrigin: settings.Gateway.CheckoutOrigin, MaxQuoteLifetime: 30 * time.Minute, Quarantine: 24 * time.Hour}
	maximum := os.Getenv("DEPOSIT_MAX_ORDER_AMOUNT")
	if maximum == "" {
		maximum = "1"
	}
	settings.Order.MaxOrderUnits, err = depositUnits(maximum)
	if err != nil {
		return Settings{}, errors.New("invalid maximum test order amount")
	}
	return settings, nil
}
