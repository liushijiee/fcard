package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/GMWalletApp/epusdt/config"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	applog "github.com/GMWalletApp/epusdt/util/log"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

const nileRPC = "https://nile.trongrid.io"
const environmentKey = "fcard.chain_environment"

type credentials struct {
	Environment string `json:"environment"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	PID         string `json:"pid"`
	Secret      string `json:"secret"`
}

func main() {
	if err := run(len(os.Args) == 2 && os.Args[1] == "--check"); err != nil {
		fmt.Fprintln(os.Stderr, "Nile safety bootstrap failed:", err)
		os.Exit(1)
	}
}

func run(checkOnly bool) error {
	config.SetConfigPath("/app/conf/.env")
	config.Init()
	applog.Init()
	if viper.GetString("db_type") != "sqlite" || viper.GetString("sqlite_database_filename") != "/app/data/epusdt.db" {
		return errors.New("this bootstrap only supports its isolated SQLite test database")
	}
	if err := dao.DBInit(); err != nil {
		return err
	}
	if err := dao.RuntimeInit(); err != nil {
		return err
	}
	if checkOnly {
		return verify()
	}
	if previous := data.GetSettingString(environmentKey, ""); previous != "" && previous != "nile" {
		return errors.New("database belongs to a different chain environment")
	}
	if err := initialize(); err != nil {
		return err
	}
	if err := saveCredentials(); err != nil {
		return err
	}
	return verify()
}

func initialize() error {
	policy, err := loadReceivingPolicy()
	if err != nil {
		return err
	}
	return dao.Mdb.Transaction(func(tx *gorm.DB) error {
		for _, model := range []interface{}{&mdb.Chain{}, &mdb.ChainToken{}, &mdb.RpcNode{}} {
			if err := tx.Model(model).Where("1 = 1").Update("enabled", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&mdb.WalletAddress{}).Where("1 = 1").Update("status", mdb.TokenStatusDisable).Error; err != nil {
			return err
		}
		var node mdb.RpcNode
		if err := tx.Where("network = ? AND url = ?", "tron", nileRPC).Limit(1).Find(&node).Error; err != nil {
			return err
		}
		if node.ID == 0 {
			node = mdb.RpcNode{Network: "tron", Url: nileRPC, Type: "http", Weight: 1, Enabled: true, Purpose: "both", Status: "unknown"}
			if err := tx.Create(&node).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&node).Updates(map[string]interface{}{"enabled": true, "purpose": "both"}).Error; err != nil {
			return err
		}
		for _, setting := range []mdb.Setting{
			{Group: "fcard", Key: environmentKey, Value: "nile", Type: "string"},
			{Group: "system", Key: mdb.SettingKeyAmountPrecision, Value: "6", Type: "int"},
			{Group: "okpay", Key: mdb.SettingKeyOkPayEnabled, Value: "false", Type: "bool"},
		} {
			if err := tx.Where("key = ?", setting.Key).Assign(setting).FirstOrCreate(&mdb.Setting{}).Error; err != nil {
				return err
			}
		}
		return applyReceivingPolicy(tx, policy)
	})
}

func saveCredentials() error {
	password, created, err := data.EnsureDefaultAdmin()
	if err != nil {
		return err
	}
	if _, err := data.EnsureDefaultApiKey(); err != nil {
		return err
	}
	key, err := data.GetEnabledApiKey("1000")
	if err != nil || key.ID == 0 {
		return errors.New("missing test merchant credential")
	}
	path := "/app/conf/credentials.json"
	if encoded, err := os.ReadFile(path); err == nil {
		var existing credentials
		if created || json.Unmarshal(encoded, &existing) != nil || existing.Environment != "nile" || existing.PID != key.Pid || existing.Secret != key.SecretKey {
			return errors.New("credential file and database do not match; refusing to overwrite")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if !created {
		return errors.New("admin exists but credential file is missing; recover manually")
	}
	encoded, err := json.MarshalIndent(credentials{Environment: "nile", Username: "admin", Password: password, PID: key.Pid, Secret: key.SecretKey}, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func allowedRPC(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "nile.trongrid.io" && parsed.User == nil && (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == ""
}

func verify() error {
	data.ReloadSettings()
	if data.GetSettingString(environmentKey, "") != "nile" {
		return errors.New("Nile marker is missing")
	}
	var nodes []mdb.RpcNode
	if err := dao.Mdb.Where("enabled = ?", true).Find(&nodes).Error; err != nil {
		return err
	}
	if len(nodes) == 0 {
		return errors.New("no Nile RPC configured")
	}
	for _, node := range nodes {
		if node.Network != "tron" || node.Type != "http" || !allowedRPC(node.Url) {
			return errors.New("non-Nile RPC is enabled")
		}
	}
	policy, err := loadReceivingPolicy()
	if err != nil {
		return err
	}
	if err := verifyReceivingPolicy(dao.Mdb, policy); err != nil {
		return err
	}
	if data.GetSettingBool(mdb.SettingKeyOkPayEnabled, false) {
		return errors.New("payment channels must stay disabled for this deployment spike")
	}
	if policy == nil {
		fmt.Println("Nile safety checks passed; receiving remains disabled")
	} else {
		fmt.Println("Nile safety checks passed; only reviewed test receiving is enabled")
	}
	return nil
}
