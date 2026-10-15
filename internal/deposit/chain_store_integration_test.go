package deposit

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fcard/internal/storage"
	"fcard/migrations"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func chainIntegrationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL or run scripts/test-integration.ps1 for a disposable database")
	}
	ctx := context.Background()
	maintenance, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot connect to integration database server")
	}
	databaseName := "fcard_chain_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := maintenance.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		maintenance.Close(ctx)
		t.Fatal("cannot create disposable database; CREATEDB permission is required")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := maintenance.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("cannot remove disposable database", databaseName)
		}
		maintenance.Close(cleanup)
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("invalid integration database URL")
	}
	parsed.Path = "/" + databaseName
	if err := storage.Migrate(parsed.String()); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(parsed.String()); err != nil {
		t.Fatal("migration not repeatable")
	}
	pool, err := storage.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal("cannot open disposable database pool")
	}
	t.Cleanup(pool.Close)
	if err := storage.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func testChainStore(t testing.TB, pool *pgxpool.Pool, scanner string) *TronChainStore {
	t.Helper()
	store, err := NewTronChainStore(pool, TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: testTronGenesis(), Contract: testTronContract(), Decimals: 6}, scanner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Initialize(context.Background(), ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); err != nil {
		t.Fatal(err)
	}
	return store
}

func chainCounts(t testing.TB, pool *pgxpool.Pool) (int, int) {
	t.Helper()
	var blocks, events int
	if err := pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM chain_scan_blocks), (SELECT count(*) FROM chain_transfers)").Scan(&blocks, &events); err != nil {
		t.Fatal(err)
	}
	return blocks, events
}

func TestChainStoreIntegration(t *testing.T) {
	pool := chainIntegrationDatabase(t)
	ctx := context.Background()
	store := testChainStore(t, pool, "receipt-v1")
	receipt := testTronReceipt()
	receipt.Logs[0].Data = strings.Repeat("f", 64)
	receipt.Logs = append(receipt.Logs, testTronLog())
	receipt.Logs[1].Data = strings.Repeat("0", 64)
	batch := testStoreBatch(t, receipt)

	t.Run("atomic write rollback", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "ALTER TABLE chain_transfers ADD CONSTRAINT test_fail_second_event CHECK (raw_units <> '0')"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(ctx, "ALTER TABLE chain_transfers DROP CONSTRAINT test_fail_second_event"); err != nil {
				t.Error(err)
			}
		})
		if committed, err := store.CommitSolidBlock(ctx, batch); err == nil || committed {
			t.Fatal("injected second event failure committed")
		}
		blocks, events := chainCounts(t, pool)
		cursor, err := store.Load(ctx)
		if err != nil || blocks != 0 || events != 0 || cursor.BlockHeight != 99 {
			t.Fatalf("partial state persisted: blocks=%d events=%d cursor=%d err=%v", blocks, events, cursor.BlockHeight, err)
		}
	})

	t.Run("cursor write failure rolls back all evidence", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "ALTER TABLE chain_scan_cursors ADD CONSTRAINT test_fail_advance CHECK (block_height < 100)"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(ctx, "ALTER TABLE chain_scan_cursors DROP CONSTRAINT test_fail_advance"); err != nil {
				t.Error(err)
			}
		})
		if committed, err := store.CommitSolidBlock(ctx, batch); err == nil || committed {
			t.Fatal("injected cursor failure committed")
		}
		blocks, events := chainCounts(t, pool)
		cursor, err := store.Load(ctx)
		if err != nil || blocks != 0 || events != 0 || cursor.BlockHeight != 99 {
			t.Fatal("cursor failure left partial evidence")
		}
	})

	t.Run("persist replay restart and uint256", func(t *testing.T) {
		committed, err := store.CommitSolidBlock(ctx, batch)
		if err != nil || !committed {
			t.Fatalf("commit failed: %v", err)
		}
		store = testChainStore(t, pool, "receipt-v1")
		if committed, err := store.CommitSolidBlock(ctx, batch); err != nil || committed {
			t.Fatalf("replay not idempotent: %v", err)
		}
		blocks, events := chainCounts(t, pool)
		var amount string
		if err := pool.QueryRow(ctx, "SELECT raw_units FROM chain_transfers WHERE event_index = 0").Scan(&amount); err != nil {
			t.Fatal(err)
		}
		if blocks != 1 || events != 2 || amount != "115792089237316195423570985008687907853269984665640564039457584007913129639935" {
			t.Fatalf("wrong persisted evidence: %d %d %s", blocks, events, amount)
		}
		cursor, err := store.Load(ctx)
		if err != nil || cursor.BlockHeight != 100 || cursor.StartHeight != 99 {
			t.Fatal("restart lost checkpoint")
		}
	})

	t.Run("conflicting replay", func(t *testing.T) {
		altered := testTronReceipt()
		if _, err := store.CommitSolidBlock(ctx, testStoreBatch(t, altered)); !errors.Is(err, ErrScanConflict) {
			t.Fatalf("changed replay accepted: %v", err)
		}
		other := testChainStore(t, pool, "other-scanner")
		if _, err := other.CommitSolidBlock(ctx, testStoreBatch(t, altered)); !errors.Is(err, ErrScanConflict) {
			t.Fatalf("conflicting global event accepted: %v", err)
		}
		blocks, events := chainCounts(t, pool)
		cursor, err := other.Load(ctx)
		if err != nil || blocks != 1 || events != 2 || cursor.BlockHeight != 99 {
			t.Fatal("conflicting event did not roll back block and cursor")
		}
		if committed, err := other.CommitSolidBlock(ctx, batch); err != nil || !committed {
			t.Fatalf("consistent cross-scanner replay failed: %v", err)
		}
		_, events = chainCounts(t, pool)
		if events != 2 {
			t.Fatal("duplicate global events")
		}
	})

	t.Run("scope and explicit start", func(t *testing.T) {
		if _, err := store.Initialize(ctx, ScanCheckpoint{Height: 100, BlockID: testTronBlockID(100)}); !errors.Is(err, ErrScanConflict) {
			t.Fatal("start height silently reset")
		}
		wrong := *store
		wrong.config.Contract = "41" + strings.Repeat("44", 20)
		if _, err := wrong.Load(ctx); !errors.Is(err, ErrScanConflict) {
			t.Fatal("contract silently changed")
		}
		config := store.config
		config.Environment = "mainnet"
		if _, err := NewTronChainStore(pool, config, "scanner"); err == nil {
			t.Fatal("mainnet allowed")
		}
		if _, err := NewTronChainStore(pool, store.config, "../scanner"); err == nil {
			t.Fatal("invalid scanner accepted")
		}
		isolated := *store
		isolated.config.GenesisID = "0000000000000000" + strings.Repeat("11", 24)
		if _, err := isolated.Load(ctx); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("genesis did not isolate cursor")
		}
	})

	t.Run("no skipped block or changed parent", func(t *testing.T) {
		if _, err := store.CommitSolidBlock(ctx, SolidBlockBatch{Block: testTronBlock(102)}); !errors.Is(err, ErrScanConflict) {
			t.Fatal("skipped block accepted")
		}
		wrongParent := testTronBlock(101)
		wrongParent.Header.RawData.ParentHash = testTronBlockID(100)[:16] + strings.Repeat("ee", 24)
		if _, err := store.CommitSolidBlock(ctx, SolidBlockBatch{Block: wrongParent}); !errors.Is(err, ErrScanConflict) {
			t.Fatal("wrong parent accepted")
		}
		incomplete := testTronBlock(101, strings.Repeat("ef", 32))
		if _, err := store.CommitSolidBlock(ctx, SolidBlockBatch{Block: incomplete}); !errors.Is(err, ErrInvalidChainEvidence) {
			t.Fatal("incomplete block accepted")
		}
	})

	t.Run("concurrent identical block", func(t *testing.T) {
		next := SolidBlockBatch{Block: testTronBlock(101)}
		var wait sync.WaitGroup
		results := make(chan bool, 8)
		failures := make(chan error, 8)
		for worker := 0; worker < 8; worker++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				committed, err := store.CommitSolidBlock(ctx, next)
				results <- committed
				failures <- err
			}()
		}
		wait.Wait()
		close(results)
		close(failures)
		writes := 0
		for committed := range results {
			if committed {
				writes++
			}
		}
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		if writes != 1 {
			t.Fatalf("expected one advance, got %d", writes)
		}
		if _, err := store.CommitSolidBlock(ctx, batch); err != nil {
			t.Fatal("historical exact replay failed")
		}
		cursor, err := store.Load(ctx)
		if err != nil || cursor.BlockHeight != 101 {
			t.Fatal("replay regressed cursor")
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.CommitSolidBlock(canceled, SolidBlockBatch{Block: testTronBlock(102)}); err == nil {
			t.Fatal("canceled commit succeeded")
		}
		cursor, err := store.Load(ctx)
		if err != nil || cursor.BlockHeight != 101 {
			t.Fatal("cancellation advanced cursor")
		}
	})

	t.Run("database rejects noncanonical uint256", func(t *testing.T) {
		for _, amount := range []string{"-1", "01", "1.0", "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
			_, err := pool.Exec(ctx, "UPDATE chain_transfers SET raw_units = $1 WHERE event_index = 0", amount)
			if err == nil {
				t.Fatalf("invalid uint256 accepted: %s", amount)
			}
		}
	})

	t.Run("different genesis isolates identical transaction events", func(t *testing.T) {
		config := store.config
		config.GenesisID = "0000000000000000" + strings.Repeat("11", 24)
		isolated, err := NewTronChainStore(pool, config, store.scanner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := isolated.Initialize(ctx, ScanCheckpoint{Height: 99, BlockID: testTronBlockID(99)}); err != nil {
			t.Fatal(err)
		}
		otherBatch := testStoreBatch(t, receipt)
		otherBatch.Transactions[0].GenesisID = config.GenesisID
		if committed, err := isolated.CommitSolidBlock(ctx, otherBatch); err != nil || !committed {
			t.Fatalf("genesis isolation failed: %v", err)
		}
		_, events := chainCounts(t, pool)
		if events != 4 {
			t.Fatal("events from different genesis were merged")
		}
	})
}

func TestChainMigrationRollbackIntegration(t *testing.T) {
	pool := chainIntegrationDatabase(t)
	databaseURL := pool.Config().ConnConfig.ConnString()
	connection, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	driver, err := postgres.WithInstance(connection, &postgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if err := migration.Steps(-5); err != nil {
		t.Fatal(err)
	}
	if err := storage.Ready(context.Background(), pool); !errors.Is(err, storage.ErrSchemaNotReady) {
		t.Fatal("schema v5 accepted by v10 backend")
	}
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass('chain_transfers') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("down migration did not remove chain table")
	}
	if err := migration.Up(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Ready(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}
