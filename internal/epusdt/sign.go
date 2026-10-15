package epusdt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

func Sign(params map[string]string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonicalParams(params)))
	return hex.EncodeToString(mac.Sum(nil))
}

func canonicalParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key != "signature" && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+params[key])
	}
	return strings.Join(pairs, "&")
}

func Verify(params map[string]string, secret, signature string) bool {
	if secret == "" || len(signature) != 64 || signature != strings.ToLower(signature) {
		return false
	}
	actual, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	expected, _ := hex.DecodeString(Sign(params, secret))
	return hmac.Equal(actual, expected)
}
