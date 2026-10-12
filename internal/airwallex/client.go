package airwallex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const SandboxURL = "https://api.sandbox.airwallex.com"

type Config struct {
	ClientID   string
	APIKey     string
	APIVersion string
}
type Error struct {
	Status int
	Code   string
}

func (failure *Error) Error() string { return "Airwallex request failed: " + failure.Code }
func Definitive(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && (failure.Status == 400 || failure.Status == 401 || failure.Status == 403 || failure.Status == 404 || failure.Status == 422)
}

type Client struct {
	config  Config
	http    *http.Client
	baseURL string
	mutex   sync.Mutex
	token   string
	expires time.Time
}

func New(config Config) *Client {
	return &Client{config: config, http: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, baseURL: SandboxURL}
}

type Cardholder struct {
	ID         string `json:"cardholder_id"`
	Status     string `json:"status"`
	Type       string `json:"type"`
	Individual struct {
		Name struct {
			First  string `json:"first_name"`
			Middle string `json:"middle_name"`
			Last   string `json:"last_name"`
		} `json:"name"`
	} `json:"individual"`
}

func (holder Cardholder) LegalName() string {
	return strings.Join(strings.Fields(holder.Individual.Name.First+" "+holder.Individual.Name.Middle+" "+holder.Individual.Name.Last), " ")
}

type Card struct {
	ID           string            `json:"card_id"`
	CardholderID string            `json:"cardholder_id"`
	Status       string            `json:"card_status"`
	Number       string            `json:"card_number"`
	RequestID    string            `json:"request_id"`
	Metadata     map[string]string `json:"metadata"`
}
type CreateRequest struct {
	CardholderID string `json:"cardholder_id"`
	CreatedBy    string `json:"created_by"`
	FormFactor   string `json:"form_factor"`
	Personalized bool   `json:"is_personalized"`
	Program      struct {
		Purpose string `json:"purpose"`
	} `json:"program"`
	RequestID string            `json:"request_id"`
	Nickname  string            `json:"nick_name"`
	Metadata  map[string]string `json:"metadata"`
	Controls  struct {
		Count      string   `json:"allowed_transaction_count"`
		Currencies []string `json:"allowed_currencies"`
		Limits     struct {
			Currency string  `json:"currency"`
			Limits   []Limit `json:"limits"`
		} `json:"transaction_limits"`
	} `json:"authorization_controls"`
}
type Limit struct {
	Amount   json.Number `json:"amount"`
	Interval string      `json:"interval"`
}

func BuildRequest(holderID, legalName, purpose, requestID, nickname, cardID, operationID string, cents int64) CreateRequest {
	request := CreateRequest{CardholderID: holderID, CreatedBy: legalName, FormFactor: "VIRTUAL", Personalized: false, RequestID: requestID, Nickname: nickname,
		Metadata: map[string]string{"fcard_card_id": cardID, "fcard_operation_id": operationID}}
	request.Program.Purpose = purpose
	request.Controls.Count = "MULTIPLE"
	request.Controls.Currencies = []string{"USD"}
	request.Controls.Limits.Currency = "USD"
	request.Controls.Limits.Limits = []Limit{{Amount: json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100)), Interval: "ALL_TIME"}}
	return request
}
func (client *Client) GetCardholder(ctx context.Context, id string) (Cardholder, error) {
	var holder Cardholder
	err := client.call(ctx, "GET", "/api/v1/issuing/cardholders/"+id, nil, &holder)
	return holder, err
}

func (client *Client) CreateCard(ctx context.Context, payload json.RawMessage) (Card, error) {
	var card Card
	err := client.call(ctx, "POST", "/api/v1/issuing/cards/create", payload, &card)
	return card, err
}
func (client *Client) GetCard(ctx context.Context, id string) (Card, error) {
	var card Card
	err := client.call(ctx, "GET", "/api/v1/issuing/cards/"+id, nil, &card)
	return card, err
}
func (client *Client) accessToken(ctx context.Context) (string, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.token != "" && time.Until(client.expires) > time.Minute {
		return client.token, nil
	}
	request, err := http.NewRequestWithContext(ctx, "POST", client.baseURL+"/api/v1/authentication/login", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", &Error{Code: "authentication_failed"}
	}
	request.Header.Set("x-client-id", client.config.ClientID)
	request.Header.Set("x-api-key", client.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	var result struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := client.send(request, &result); err != nil {
		return "", err
	}
	expires, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		expires, err = time.Parse("2006-01-02T15:04:05-0700", result.ExpiresAt)
	}
	if err != nil || result.Token == "" || !expires.After(time.Now().Add(time.Minute)) {
		return "", &Error{Code: "invalid_token_response"}
	}
	client.token, client.expires = result.Token, expires
	return client.token, nil
}
func (client *Client) call(ctx context.Context, method, path string, payload []byte, result any) error {
	token, err := client.accessToken(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-version", client.config.APIVersion)
	err = client.send(request, result)
	var failure *Error
	if errors.As(err, &failure) && failure.Status == 401 {
		client.mutex.Lock()
		if client.token == token {
			client.token = ""
		}
		client.mutex.Unlock()
	}
	return err
}
func (client *Client) send(request *http.Request, result any) error {
	response, err := client.http.Do(request)
	if err != nil {
		return &Error{Code: "network_error"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Status: response.StatusCode, Code: "provider_rejected"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 || json.Unmarshal(body, result) != nil {
		return &Error{Code: "invalid_response"}
	}
	return nil
}
