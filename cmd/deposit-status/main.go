// deposit-status reads the schema and scanner progress without exposing credentials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"fcard/internal/config"
	"fcard/internal/storage"
	"github.com/google/uuid"
)

func main() {
	order := flag.String("order", "", "inspect one local order without exposing merchant credentials")
	flag.Parse()
	if *order != "" {
		if _, err := uuid.Parse(*order); err != nil {
			fmt.Fprintln(os.Stderr, "invalid order UUID")
			os.Exit(1)
		}
	}
	if err := run(*order); err != nil {
		fmt.Fprintln(os.Stderr, "deposit status failed:", err)
		os.Exit(1)
	}
}

func run(order string) error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := storage.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer pool.Close()
	var version int
	var dirty bool
	var database string
	if err := pool.QueryRow(ctx, "SELECT current_database(), version,dirty FROM schema_migrations").Scan(&database, &version, &dirty); err != nil {
		return err
	}
	result := map[string]any{"database": database, "schema": version, "dirty": dirty, "runtime_enabled": settings.Deposits.RuntimeEnabled, "create_enabled": settings.Deposits.CreateEnabled}
	if version >= 10 && !dirty {
		var cursors []byte
		if err := pool.QueryRow(ctx, `SELECT COALESCE(json_agg(progress),'[]'::json) FROM (SELECT scanner,start_height,block_height,block_id,updated_at FROM chain_scan_cursors WHERE chain_environment='nile') progress`).Scan(&cursors); err != nil {
			return err
		}
		result["cursors"] = json.RawMessage(cursors)
		var counts []byte
		if err := pool.QueryRow(ctx, `SELECT json_build_object('orders',(SELECT count(*) FROM deposit_orders),'receipts',(SELECT count(*) FROM deposit_receipts),'inbox',(SELECT count(*) FROM deposit_callback_inbox),'blocks',(SELECT count(*) FROM chain_scan_blocks))`).Scan(&counts); err != nil {
			return err
		}
		result["counts"] = json.RawMessage(counts)
		if order != "" {
			for key, query := range map[string]string{
				"order":             `SELECT row_to_json(view) FROM (SELECT id,status,requested_units,actual_units,chain_environment,contract,quote_json->>'receive_address' AS receive_address,quote_json->>'payment_url' AS payment_url,create_started_at,expires_at,created_at,updated_at,review_reason FROM deposit_orders WHERE id=$1) view`,
				"receipts":          `SELECT COALESCE(json_agg(view),'[]'::json) FROM (SELECT tx_hash,event_index,confirmed_units,block_time,confirmed_at FROM deposit_receipts WHERE order_id=$1) view`,
				"jobs":              `SELECT COALESCE(json_agg(view),'[]'::json) FROM (SELECT kind,status,attempts,last_error,created_at,updated_at FROM deposit_jobs WHERE order_id=$1 ORDER BY created_at) view`,
				"events":            `SELECT COALESCE(json_agg(view),'[]'::json) FROM (SELECT action,created_at FROM deposit_events WHERE order_id=$1 ORDER BY created_at) view`,
				"observed_incoming": `SELECT COALESCE(json_agg(view),'[]'::json) FROM (SELECT t.tx_hash,t.event_index,t.raw_units,t.block_height,t.block_time FROM chain_transfers t JOIN deposit_orders o ON o.id=$1 AND t.chain_environment=o.chain_environment AND t.genesis_id=o.genesis_id AND t.contract=o.contract AND t.to_address=o.receive_address WHERE t.block_time>=o.created_at-interval '10 minutes' ORDER BY t.block_time DESC LIMIT 30) view`,
			} {
				var data []byte
				if err := pool.QueryRow(ctx, query, order).Scan(&data); err != nil {
					return fmt.Errorf("%s query: %w", key, err)
				}
				result[key] = json.RawMessage(data)
			}
		}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
