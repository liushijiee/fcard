package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

const nileGenesis = "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc"

type receivingPolicy struct {
	Environment    string    `json:"environment"`
	Genesis        string    `json:"genesis"`
	Contract       string    `json:"contract"`
	Decimals       int       `json:"decimals"`
	BytecodeSHA256 string    `json:"bytecode_sha256"`
	Wallets        []string  `json:"wallets"`
	CheckoutOrigin string    `json:"checkout_origin"`
	ReviewedAt     time.Time `json:"reviewed_at"`
}

func validTronAddress(raw string) bool {
	if len(raw) != 34 || raw[0] != 'T' {
		return false
	}
	number := new(big.Int)
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, char := range raw {
		index := strings.IndexRune(alphabet, char)
		if index < 0 {
			return false
		}
		number.Mul(number, big.NewInt(58)).Add(number, big.NewInt(int64(index)))
	}
	decoded := number.Bytes()
	if len(decoded) != 25 || decoded[0] != 0x41 {
		return false
	}
	first := sha256.Sum256(decoded[:21])
	second := sha256.Sum256(first[:])
	return bytes.Equal(decoded[21:], second[:4]) && !bytes.Equal(decoded[1:21], make([]byte, 20))
}

func (policy receivingPolicy) validate() error {
	origin, err := url.Parse(policy.CheckoutOrigin)
	hash, hashErr := hex.DecodeString(policy.BytecodeSHA256)
	if policy.Environment != "nile" || policy.Genesis != nileGenesis || !validTronAddress(policy.Contract) || policy.Decimals != 6 || hashErr != nil || len(hash) != 32 || err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || policy.ReviewedAt.IsZero() || policy.ReviewedAt.After(time.Now().Add(time.Minute)) || len(policy.Wallets) < 1 || len(policy.Wallets) > 100 {
		return errors.New("invalid reviewed Nile receiving policy")
	}
	seen := map[string]bool{}
	for _, wallet := range policy.Wallets {
		if !validTronAddress(wallet) || seen[wallet] {
			return errors.New("invalid or duplicate reviewed wallet")
		}
		seen[wallet] = true
	}
	return nil
}

func loadReceivingPolicy() (*receivingPolicy, error) {
	encoded, err := os.ReadFile("/app/conf/nile-receiving.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || len(encoded) > 32*1024 {
		return nil, errors.New("receiving policy unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var policy receivingPolicy
	if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("malformed receiving policy")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if viper.GetString("app_uri") != policy.CheckoutOrigin {
		return nil, errors.New("checkout origin differs from reviewed policy")
	}
	return &policy, nil
}

func applyReceivingPolicy(tx *gorm.DB, policy *receivingPolicy) error {
	if policy == nil {
		return nil
	}
	if err := policy.validate(); err != nil {
		return err
	}
	var chain mdb.Chain
	if err := tx.Where("network = ?", "tron").First(&chain).Error; err != nil {
		return err
	}
	if err := tx.Model(&chain).Updates(map[string]interface{}{"enabled": true, "min_confirmations": 20, "scan_interval_sec": 3, "display_name": "TRON Nile testnet"}).Error; err != nil {
		return err
	}
	var token mdb.ChainToken
	if err := tx.Where("network = ? AND symbol = ?", "tron", "USDT").First(&token).Error; err != nil {
		return err
	}
	if err := tx.Model(&token).Updates(map[string]interface{}{"enabled": true, "contract_address": policy.Contract, "decimals": 6, "min_amount": 0.01}).Error; err != nil {
		return err
	}
	for _, address := range policy.Wallets {
		wallet := mdb.WalletAddress{Network: "tron", Address: address, Status: mdb.TokenStatusEnable, Source: mdb.WalletSourceImport, Remark: "Fcard reviewed Nile test receiving wallet"}
		if err := tx.Where("network = ? AND address = ?", "tron", address).Assign(map[string]interface{}{"status": mdb.TokenStatusEnable, "source": wallet.Source, "remark": wallet.Remark}).FirstOrCreate(&wallet).Error; err != nil {
			return err
		}
	}
	return nil
}

func verifyReceivingPolicy(db *gorm.DB, policy *receivingPolicy) error {
	var tokens []mdb.ChainToken
	var wallets []mdb.WalletAddress
	var chains []mdb.Chain
	if err := db.Where("enabled = ?", true).Find(&tokens).Error; err != nil {
		return err
	}
	if err := db.Where("status = ?", mdb.TokenStatusEnable).Find(&wallets).Error; err != nil {
		return err
	}
	if err := db.Where("enabled = ?", true).Find(&chains).Error; err != nil {
		return err
	}
	if policy == nil {
		if len(tokens)+len(wallets)+len(chains) != 0 {
			return errors.New("receiving requires an explicit reviewed Nile policy")
		}
		return nil
	}
	if len(tokens) != 1 || tokens[0].Network != "tron" || tokens[0].Symbol != "USDT" || tokens[0].ContractAddress != policy.Contract || tokens[0].Decimals != 6 || len(chains) != 1 || chains[0].Network != "tron" || chains[0].MinConfirmations < 20 || len(wallets) != len(policy.Wallets) {
		return errors.New("enabled asset or chain differs from reviewed policy")
	}
	allowed := map[string]bool{}
	for _, wallet := range policy.Wallets {
		allowed[wallet] = true
	}
	for _, wallet := range wallets {
		if wallet.Network != "tron" || !allowed[wallet.Address] {
			return errors.New("unreviewed receiving wallet is enabled")
		}
	}
	return nil
}
