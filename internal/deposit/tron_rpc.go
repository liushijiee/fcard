package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"
)

const maxTronResponseBytes = 4 * 1024 * 1024

type TronRPCConfig struct {
	Environment          string
	BaseURL              string
	AllowLocalTestServer bool
}

type TronRPC struct {
	base *url.URL
	http *http.Client
}

func (rpc *TronRPC) SourceID() string {
	return rpc.base.String()
}

func NewTronRPC(config TronRPCConfig) (*TronRPC, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || config.Environment != TronEnvironmentNile || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, ErrWrongChain
	}
	nile := base.Scheme == "https" && base.Host == "nile.trongrid.io"
	local := false
	if address := net.ParseIP(base.Hostname()); address != nil {
		local = config.AllowLocalTestServer && base.Scheme == "http" && address.IsLoopback()
	}
	if !nile && !local {
		return nil, ErrWrongChain
	}
	return &TronRPC{base: base, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (rpc *TronRPC) call(ctx context.Context, path string, input any, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return ErrInvalidChainEvidence
	}
	endpoint := rpc.base.ResolveReference(&url.URL{Path: "/walletsolidity/" + path})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return ErrChainUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := rpc.http.Do(request)
	if err != nil {
		return ErrChainUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrChainUnavailable
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return ErrInvalidChainEvidence
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTronResponseBytes+1))
	if err != nil {
		return ErrChainUnavailable
	}
	validationBody := body
	isReceiptArray := path == "gettransactioninfobyblocknum" && len(bytes.TrimSpace(body)) > 0 && bytes.TrimSpace(body)[0] == '['
	if isReceiptArray {
		validationBody = append([]byte(`{"receipts":`), body...)
		validationBody = append(validationBody, '}')
	}
	if len(body) > maxTronResponseBytes || !utf8.Valid(body) || rejectDuplicateRPCKeys(validationBody) != nil {
		return ErrInvalidChainEvidence
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(validationBody, &envelope) != nil {
		return ErrInvalidChainEvidence
	}
	if _, exists := envelope["Error"]; exists {
		return ErrChainUnavailable
	}
	if _, exists := envelope["error"]; exists {
		return ErrChainUnavailable
	}
	if json.Unmarshal(body, output) != nil {
		return ErrInvalidChainEvidence
	}
	return nil
}

// SolidReceipts reads all TransactionInfo records from the same Solidity RPC.
// The reader still checks an exact bijection with the block and validates every
// receipt; batching avoids one HTTP round trip per transaction while catching up.
func (rpc *TronRPC) SolidReceipts(ctx context.Context, height int64) ([]TronReceipt, error) {
	if height <= 0 {
		return nil, ErrInvalidChainEvidence
	}
	var receipts []TronReceipt
	if err := rpc.call(ctx, "gettransactioninfobyblocknum", map[string]any{"num": height, "visible": false}, &receipts); err != nil {
		return nil, err
	}
	return receipts, nil
}

func rejectDuplicateRPCKeys(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrInvalidChainEvidence
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if walkRPCJSON(decoder, 0) != nil {
		return ErrInvalidChainEvidence
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidChainEvidence
	}
	return nil
}

func walkRPCJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalidChainEvidence
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidChainEvidence
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]struct{}{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return ErrInvalidChainEvidence
			}
			if _, exists := seen[key]; exists {
				return ErrInvalidChainEvidence
			}
			seen[key] = struct{}{}
			if walkRPCJSON(decoder, depth+1) != nil {
				return ErrInvalidChainEvidence
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrInvalidChainEvidence
		}
	case json.Delim('['):
		for decoder.More() {
			if walkRPCJSON(decoder, depth+1) != nil {
				return ErrInvalidChainEvidence
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrInvalidChainEvidence
		}
	case json.Delim('}'), json.Delim(']'):
		return ErrInvalidChainEvidence
	}
	return nil
}

func (rpc *TronRPC) GenesisID(ctx context.Context) (string, error) {
	block, err := rpc.SolidBlock(ctx, 0)
	if err != nil {
		return "", err
	}
	return canonicalTronHash(block.ID)
}

func (rpc *TronRPC) SolidHead(ctx context.Context) (TronBlock, error) {
	var block TronBlock
	if err := rpc.call(ctx, "getnowblock", map[string]any{"visible": false}, &block); err != nil {
		return TronBlock{}, err
	}
	if _, err := tronBlockHeight(block); err != nil {
		return TronBlock{}, err
	}
	return block, nil
}

func (rpc *TronRPC) SolidBlock(ctx context.Context, height int64) (TronBlock, error) {
	if height < 0 {
		return TronBlock{}, ErrInvalidChainEvidence
	}
	var block TronBlock
	if err := rpc.call(ctx, "getblockbynum", map[string]any{"num": height, "visible": false}, &block); err != nil {
		return TronBlock{}, err
	}
	actual, err := tronBlockHeight(block)
	if err != nil || actual != height {
		return TronBlock{}, ErrInvalidChainEvidence
	}
	return block, nil
}

func (rpc *TronRPC) SolidReceipt(ctx context.Context, rawHash string) (TronReceipt, error) {
	hash, err := canonicalTronHash(rawHash)
	if err != nil {
		return TronReceipt{}, err
	}
	var receipt TronReceipt
	if err := rpc.call(ctx, "gettransactioninfobyid", map[string]any{"value": hash, "visible": false}, &receipt); err != nil {
		return TronReceipt{}, err
	}
	if receipt.ID == "" {
		return TronReceipt{}, ErrNotSolidified
	}
	id, err := canonicalTronHash(receipt.ID)
	if err != nil || id != hash {
		return TronReceipt{}, ErrInvalidChainEvidence
	}
	return receipt, nil
}
