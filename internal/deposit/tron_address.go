package deposit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
)

const tronBase58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func canonicalTronAddress(raw string) (string, error) {
	if len(raw) == 42 && strings.EqualFold(raw[:2], "41") {
		decoded, err := hex.DecodeString(raw)
		if err == nil && len(decoded) == 21 {
			return hex.EncodeToString(decoded), nil
		}
	}
	if len(raw) != 34 || raw[0] != 'T' {
		return "", errors.New("invalid TRON address")
	}
	value := new(big.Int)
	for _, character := range raw {
		digit := strings.IndexRune(tronBase58Alphabet, character)
		if digit < 0 {
			return "", errors.New("invalid TRON address")
		}
		value.Mul(value, big.NewInt(58))
		value.Add(value, big.NewInt(int64(digit)))
	}
	decoded := value.Bytes()
	if len(decoded) != 25 || decoded[0] != 0x41 {
		return "", errors.New("invalid TRON address")
	}
	first := sha256.Sum256(decoded[:21])
	second := sha256.Sum256(first[:])
	if !bytes.Equal(decoded[21:], second[:4]) {
		return "", errors.New("invalid TRON address checksum")
	}
	return hex.EncodeToString(decoded[:21]), nil
}

func tronTopicAddress(raw string) (string, error) {
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 || !bytes.Equal(decoded[:12], make([]byte, 12)) {
		return "", ErrInvalidChainEvidence
	}
	return "41" + hex.EncodeToString(decoded[12:]), nil
}
