package epusdt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 64 * 1024
const EnvironmentNile = "nile"

var amountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,6})?$`)
var orderPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var tronAddressPattern = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)

type Config struct {
	Environment    string
	BaseURL        string
	CheckoutOrigin string
	PID            string
	Secret         string
}

type Client struct {
	config   Config
	base     *url.URL
	checkout *url.URL
	http     *http.Client
}

type CreateRequest struct {
	OrderID     string
	Amount      string
	NotifyURL   string
	RedirectURL string
	Name        string
}

type Transaction struct {
	TradeID        string      `json:"trade_id"`
	OrderID        string      `json:"order_id"`
	Amount         json.Number `json:"amount"`
	Currency       string      `json:"currency"`
	ActualAmount   json.Number `json:"actual_amount"`
	ReceiveAddress string      `json:"receive_address"`
	Token          string      `json:"token"`
	Status         int         `json:"status"`
	ExpirationTime int64       `json:"expiration_time"`
	PaymentURL     string      `json:"payment_url"`
}

type Failure struct {
	HTTPStatus int
	Code       int
	Ambiguous  bool
}

func (failure *Failure) Error() string {
	return "Epusdt request failed; inspect the saved order before retrying"
}

func New(config Config) (*Client, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("invalid Epusdt base URL")
	}
	hostname := base.Hostname()
	local := hostname == "localhost" || hostname == "gateway" || hostname == "epusdt" || hostname == "host.docker.internal"
	if address := net.ParseIP(hostname); address != nil {
		local = address.IsLoopback()
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && local) {
		return nil, errors.New("Epusdt requires HTTPS or a local test endpoint")
	}
	if config.Environment != EnvironmentNile || config.PID == "" || config.Secret == "" {
		return nil, errors.New("Nile environment and merchant credentials are required")
	}
	checkout := base
	if config.CheckoutOrigin != "" {
		checkout, err = url.Parse(config.CheckoutOrigin)
		if err != nil || checkout.Scheme != "https" || checkout.Host == "" || checkout.User != nil || checkout.RawQuery != "" || checkout.Fragment != "" || checkout.Path != "" && checkout.Path != "/" {
			return nil, errors.New("invalid Epusdt checkout origin")
		}
	}
	return &Client{config: config, base: base, checkout: checkout, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (client *Client) MatchesMerchant(environment, pid, origin string) bool {
	return client != nil && client.config.Environment == environment && client.config.PID == pid && origin == client.checkout.Scheme+"://"+client.checkout.Host
}

func normalizeAmount(raw string) (string, uint64, error) {
	if !amountPattern.MatchString(raw) {
		return "", 0, errors.New("amount must be a decimal string with at most six decimal places")
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || whole > 1000000 {
		return "", 0, errors.New("amount is outside the test limit")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	scaled, err := strconv.ParseUint(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	if err != nil {
		return "", 0, errors.New("invalid amount")
	}
	units := whole*1000000 + scaled
	if units > 1000000000000 {
		return "", 0, errors.New("amount is outside the test limit")
	}
	canonical := strconv.FormatUint(whole, 10)
	if trimmed := strings.TrimRight(fraction, "0"); trimmed != "" {
		canonical += "." + trimmed
	}
	return canonical, units, nil
}

func validMerchantURL(raw string, optional bool) bool {
	if raw == "" {
		return optional
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func (client *Client) Create(ctx context.Context, input CreateRequest) (Transaction, error) {
	amount, units, err := normalizeAmount(input.Amount)
	if err != nil || units <= 10000 || !orderPattern.MatchString(input.OrderID) || !validMerchantURL(input.NotifyURL, false) || !validMerchantURL(input.RedirectURL, true) || strings.ContainsAny(input.Name, "&=\r\n") {
		return Transaction{}, errors.New("invalid Epusdt create request")
	}
	params := map[string]string{
		"pid": client.config.PID, "order_id": input.OrderID, "currency": "USDT", "token": "usdt", "network": "tron",
		"amount": amount, "notify_url": input.NotifyURL, "redirect_url": input.RedirectURL, "name": input.Name,
	}
	params["signature"] = Sign(params, client.config.Secret)
	values := url.Values{}
	for key, value := range params {
		if value != "" {
			values.Set(key, value)
		}
	}
	endpoint := client.base.ResolveReference(&url.URL{Path: "/payments/gmpay/v1/order/create-transaction"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return Transaction{}, errors.New("invalid Epusdt request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return Transaction{}, &Failure{Ambiguous: true}
	}
	defer response.Body.Close()
	failure := &Failure{HTTPStatus: response.StatusCode, Ambiguous: true}
	if response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return Transaction{}, failure
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return Transaction{}, failure
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Transaction{}, failure
	}
	var envelope struct {
		Code int             `json:"status_code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return Transaction{}, failure
	}
	failure.Code = envelope.Code
	if response.StatusCode != http.StatusOK || envelope.Code != 200 {
		if response.StatusCode == 400 || response.StatusCode == 401 {
			switch envelope.Code {
			case 401, 10003, 10004, 10005, 10006, 10009, 10014, 10016, 10041:
				failure.Ambiguous = false
			}
		}
		return Transaction{}, failure
	}
	var result Transaction
	if json.Unmarshal(envelope.Data, &result) != nil {
		return Transaction{}, failure
	}
	quoted, quotedUnits, err := normalizeAmount(result.ActualAmount.String())
	returnedAmount, _, amountErr := normalizeAmount(result.Amount.String())
	paymentURL, urlErr := url.Parse(result.PaymentURL)
	if err != nil || amountErr != nil || urlErr != nil || returnedAmount != amount || quotedUnits < units || quotedUnits-units > 99 || result.OrderID != input.OrderID || result.TradeID == "" || len(result.TradeID) > 128 || !strings.EqualFold(result.Currency, "USDT") || !strings.EqualFold(result.Token, "USDT") || result.Status != 1 || !tronAddressPattern.MatchString(result.ReceiveAddress) || result.ExpirationTime <= time.Now().Unix() || paymentURL.Scheme != client.checkout.Scheme || paymentURL.Host != client.checkout.Host || paymentURL.User != nil || paymentURL.Fragment != "" || !strings.HasPrefix(paymentURL.Path, "/pay/checkout-counter/") {
		return Transaction{}, failure
	}
	result.Amount = json.Number(amount)
	result.ActualAmount = json.Number(quoted)
	return result, nil
}
