-- name: InitializeChainCursor :exec
INSERT INTO chain_scan_cursors (
    chain_environment, genesis_id, network, scanner, contract, evidence_version,
    start_height, start_block_id, block_height, block_id
) VALUES ($1, $2, $3, $4, $5, sqlc.arg(evidence_version), sqlc.arg(start_height), sqlc.arg(start_block_id), sqlc.arg(start_height), sqlc.arg(start_block_id))
ON CONFLICT DO NOTHING;

-- name: GetChainCursor :one
SELECT * FROM chain_scan_cursors
WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND scanner = $4;

-- name: LockChainCursor :one
SELECT * FROM chain_scan_cursors
WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND scanner = $4
FOR UPDATE;

-- name: GetChainScanBlock :one
SELECT * FROM chain_scan_blocks
WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND scanner = $4 AND block_height = $5;

-- name: InsertChainScanBlock :exec
INSERT INTO chain_scan_blocks (
    chain_environment, genesis_id, network, scanner, block_height, block_id,
    parent_id, block_time, evidence_hash, evidence_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: SaveChainTransfer :one
INSERT INTO chain_transfers (
    chain_environment, genesis_id, network, tx_hash, event_index, contract,
    from_address, to_address, raw_units, block_height, block_id, block_time, receipt_hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (chain_environment, genesis_id, network, tx_hash, event_index)
DO UPDATE SET tx_hash = chain_transfers.tx_hash
WHERE chain_transfers.contract = EXCLUDED.contract
    AND chain_transfers.from_address = EXCLUDED.from_address
    AND chain_transfers.to_address = EXCLUDED.to_address
    AND chain_transfers.raw_units = EXCLUDED.raw_units
    AND chain_transfers.block_height = EXCLUDED.block_height
    AND chain_transfers.block_id = EXCLUDED.block_id
    AND chain_transfers.block_time = EXCLUDED.block_time
    AND chain_transfers.receipt_hash = EXCLUDED.receipt_hash
RETURNING tx_hash;

-- name: InsertChainScanTransaction :exec
INSERT INTO chain_scan_transactions(chain_environment, genesis_id, network, scanner, tx_hash, block_height, block_id, block_time, receipt_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: ListChainScanTransactions :many
SELECT * FROM chain_scan_transactions WHERE chain_environment=$1 AND genesis_id=$2 AND network=$3 AND scanner=$4 AND block_height=$5;

-- name: AdvanceChainCursor :execrows
UPDATE chain_scan_cursors SET block_height = sqlc.arg(next_height), block_id = sqlc.arg(next_id), updated_at = now()
WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND scanner = $4
    AND block_height = sqlc.arg(previous_height) AND block_id = sqlc.arg(previous_id);
