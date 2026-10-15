package epusdt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const maxNotificationBytes = 16 * 1024

var ErrInvalidNotification = errors.New("invalid Epusdt notification")
var ErrNotificationSignature = errors.New("invalid Epusdt notification signature")
var ErrQuoteMismatch = errors.New("Epusdt notification does not match the saved quote")

var tradePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var tronTransactionPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
var notificationTokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,15}$`)
var callbackKeyVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type CallbackKey struct {
	Version string
	Secret  string
}

type CallbackConfig struct {
	Environment string
	PID         string
	Keys        []CallbackKey
}

type CallbackVerifier struct {
	environment string
	pid         string
	keys        []CallbackKey
}

type Notification struct {
	Environment        string
	EventID            string
	KeyVersion         string
	PID                string
	TradeID            string
	OrderID            string
	AmountUnits        uint64
	ActualAmountUnits  uint64
	ReceiveAddress     string
	Token              string
	BlockTransactionID string
	Status             int
	RawPayload         []byte
}

type ExpectedPayment struct {
	Environment string
	PID         string
	Network     string
	Transaction Transaction
}

func NewCallbackVerifier(config CallbackConfig) (*CallbackVerifier, error) {
	if config.Environment != EnvironmentNile || config.PID == "" || strings.ContainsAny(config.PID, "&=\r\n") || len(config.Keys) < 1 || len(config.Keys) > 2 {
		return nil, errors.New("Nile environment and callback credentials are required")
	}
	for index, key := range config.Keys {
		if key.Secret == "" || !callbackKeyVersionPattern.MatchString(key.Version) {
			return nil, errors.New("callback key version and secret are required")
		}
		for previous := 0; previous < index; previous++ {
			if key.Version == config.Keys[previous].Version || key.Secret == config.Keys[previous].Secret {
				return nil, errors.New("duplicate callback key is not allowed")
			}
		}
	}
	return &CallbackVerifier{environment: config.Environment, pid: config.PID, keys: append([]CallbackKey(nil), config.Keys...)}, nil
}

func decodeNotificationFields(body []byte) (map[string]json.RawMessage, error) {
	if len(body) == 0 || len(body) > maxNotificationBytes {
		return nil, ErrInvalidNotification
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrInvalidNotification
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrInvalidNotification
		}
		if _, exists := fields[key]; exists {
			return nil, ErrInvalidNotification
		}
		switch key {
		case "pid", "trade_id", "order_id", "amount", "actual_amount", "receive_address", "token", "block_transaction_id", "signature", "status":
		default:
			return nil, ErrInvalidNotification
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrInvalidNotification
		}
		fields[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || len(fields) != 10 {
		return nil, ErrInvalidNotification
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrInvalidNotification
	}
	return fields, nil
}

func notificationAmount(raw json.RawMessage) (string, uint64, error) {
	if len(raw) == 0 || len(raw) > 64 || raw[0] == '"' {
		return "", 0, ErrInvalidNotification
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return "", 0, ErrInvalidNotification
	}
	canonical := strconv.FormatFloat(value, 'f', -1, 64)
	amount, units, err := normalizeAmount(canonical)
	if err != nil || units <= 10000 {
		return "", 0, ErrInvalidNotification
	}
	exact, ok := new(big.Rat).SetString(string(raw))
	if !ok || exact.Cmp(new(big.Rat).SetFrac(new(big.Int).SetUint64(units), big.NewInt(1000000))) != 0 {
		return "", 0, ErrInvalidNotification
	}
	return amount, units, nil
}

func (verifier *CallbackVerifier) Verify(body []byte) (Notification, error) {
	fields, err := decodeNotificationFields(body)
	if err != nil {
		return Notification{}, err
	}
	params := make(map[string]string, len(fields))
	for _, key := range []string{"pid", "trade_id", "order_id", "receive_address", "token", "block_transaction_id", "signature"} {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || value == "" || strings.ContainsAny(value, "&=\r\n") {
			return Notification{}, ErrInvalidNotification
		}
		params[key] = value
	}
	amount, units, err := notificationAmount(fields["amount"])
	if err != nil {
		return Notification{}, err
	}
	actual, actualUnits, err := notificationAmount(fields["actual_amount"])
	if err != nil {
		return Notification{}, err
	}
	status := bytes.TrimSpace(fields["status"])
	if !bytes.Equal(status, []byte("2")) || !orderPattern.MatchString(params["order_id"]) || !tradePattern.MatchString(params["trade_id"]) || !tronAddressPattern.MatchString(params["receive_address"]) || !tronTransactionPattern.MatchString(params["block_transaction_id"]) || !notificationTokenPattern.MatchString(params["token"]) {
		return Notification{}, ErrInvalidNotification
	}
	params["amount"] = amount
	params["actual_amount"] = actual
	params["status"] = "2"
	keyVersion := ""
	for _, key := range verifier.keys {
		if Verify(params, key.Secret, params["signature"]) {
			keyVersion = key.Version
		}
	}
	if params["pid"] != verifier.pid || keyVersion == "" {
		return Notification{}, ErrNotificationSignature
	}
	digest := sha256.Sum256([]byte(verifier.environment + "\n" + canonicalParams(params)))
	return Notification{
		Environment: verifier.environment, EventID: hex.EncodeToString(digest[:]), KeyVersion: keyVersion, PID: params["pid"],
		TradeID: params["trade_id"], OrderID: params["order_id"], AmountUnits: units, ActualAmountUnits: actualUnits,
		ReceiveAddress: params["receive_address"], Token: params["token"], BlockTransactionID: params["block_transaction_id"],
		Status: 2, RawPayload: append([]byte(nil), body...),
	}, nil
}

func MatchNotification(notification Notification, expected ExpectedPayment) error {
	quote := expected.Transaction
	_, amountUnits, amountErr := normalizeAmount(quote.Amount.String())
	_, actualUnits, actualErr := normalizeAmount(quote.ActualAmount.String())
	if amountErr != nil || actualErr != nil || amountUnits <= 10000 || actualUnits < amountUnits || actualUnits-amountUnits > 99 {
		return ErrQuoteMismatch
	}
	if expected.Environment != EnvironmentNile || notification.Environment != expected.Environment || expected.PID == "" || notification.PID != expected.PID || expected.Network != "tron" || quote.Status != 1 || notification.Status != 2 || !strings.EqualFold(quote.Currency, "USDT") || !strings.EqualFold(quote.Token, "USDT") || !strings.EqualFold(notification.Token, quote.Token) {
		return ErrQuoteMismatch
	}
	if notification.OrderID == "" || notification.OrderID != quote.OrderID || notification.TradeID == "" || notification.TradeID != quote.TradeID || notification.ReceiveAddress == "" || notification.ReceiveAddress != quote.ReceiveAddress || notification.AmountUnits != amountUnits || notification.ActualAmountUnits != actualUnits {
		return ErrQuoteMismatch
	}
	return nil
}
