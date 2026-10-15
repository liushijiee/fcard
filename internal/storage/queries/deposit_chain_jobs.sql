-- name: GetDepositTransactionProof :one
WITH candidates AS (
    SELECT indexed.block_height, indexed.block_id, indexed.block_time, indexed.receipt_hash,
        block.evidence_json, block.evidence_hash, cursor.evidence_version, indexed.scanner,
        block.block_id AS journal_block_id, block.block_time AS journal_block_time
    FROM chain_scan_transactions AS indexed
    JOIN chain_scan_blocks AS block USING (chain_environment, genesis_id, network, scanner, block_height)
    JOIN chain_scan_cursors AS cursor USING (chain_environment, genesis_id, network, scanner)
    WHERE indexed.chain_environment=$1 AND indexed.genesis_id=$2 AND indexed.network=$3 AND indexed.tx_hash=$4
        AND cursor.contract=$5 AND cursor.block_height >= indexed.block_height
)
SELECT candidates.*, (SELECT count(DISTINCT ROW(block_height,block_id,block_time,receipt_hash)) FROM candidates)::BIGINT AS variants
FROM candidates ORDER BY evidence_version DESC, scanner LIMIT 1;

-- name: ListDepositTransactionTransfers :many
SELECT * FROM chain_transfers WHERE chain_environment=$1 AND genesis_id=$2 AND network=$3 AND tx_hash=$4 AND contract=$5 ORDER BY event_index;

-- name: GetDepositChainJob :one
SELECT * FROM deposit_jobs WHERE kind='VERIFY_CHAIN' AND order_id=$1 AND dedupe_key=$2;

-- name: ListDepositChainCandidates :many
SELECT DISTINCT orders.id AS order_id, transfer.tx_hash
FROM deposit_orders AS orders
JOIN chain_transfers AS transfer ON transfer.chain_environment=orders.chain_environment AND transfer.genesis_id=orders.genesis_id
    AND transfer.network=orders.network AND transfer.contract=orders.contract AND transfer.to_address=orders.receive_address
    AND transfer.raw_units=orders.actual_units::TEXT AND transfer.block_time >= orders.create_started_at AND transfer.block_time <= orders.reserved_until
WHERE orders.chain_environment=$1 AND orders.genesis_id=$2 AND orders.network=$3 AND orders.contract=$4
    AND orders.status IN ('WAITING','VERIFYING','EXPIRED','CONFIRMED','REVIEW_REQUIRED')
    AND NOT EXISTS (SELECT 1 FROM deposit_jobs AS job WHERE job.kind='VERIFY_CHAIN' AND job.order_id=orders.id
        AND job.dedupe_key=orders.id::TEXT || '/' || transfer.tx_hash)
ORDER BY orders.id, transfer.tx_hash LIMIT 100;
