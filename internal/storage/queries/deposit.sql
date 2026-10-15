-- name: GetDepositByKey :one
SELECT * FROM deposit_orders WHERE user_id = $1 AND idempotency_key = $2;

-- name: ListPendingDepositCreates :many
SELECT job.* FROM deposit_jobs AS job JOIN deposit_orders AS orders ON orders.id = job.order_id
WHERE job.kind = 'CREATE' AND job.status = 'PENDING' AND job.attempts = 0
    AND orders.status = 'CREATING' AND orders.create_token IS NULL
ORDER BY job.created_at, job.id LIMIT 100;

-- name: LockDepositCreateJob :one
SELECT * FROM deposit_jobs WHERE kind = 'CREATE' AND order_id = $1 FOR UPDATE;

-- name: ListStaleDepositCreates :many
SELECT * FROM deposit_orders WHERE chain_environment = $1 AND genesis_id = $2 AND contract = $3
    AND status = 'CREATING' AND create_token IS NOT NULL AND trade_id IS NULL
    AND create_started_at <= clock_timestamp() - sqlc.arg(stale_seconds)::INTEGER * interval '1 second'
ORDER BY create_started_at, id LIMIT 100;

-- name: GetUserDeposit :one
SELECT * FROM deposit_orders WHERE id = $1 AND user_id = $2;

-- name: LockDeposit :one
SELECT * FROM deposit_orders WHERE id = $1 FOR UPDATE;

-- name: CreateDeposit :one
INSERT INTO deposit_orders (id, user_id, idempotency_key, requested_units, chain_environment, genesis_id, network, currency, contract,
    decimals, merchant_pid, gateway_order_id, notify_url, redirect_url, gateway_origin, max_quote_seconds, quarantine_seconds, auth_version)
VALUES ($1, $2, $3, $4, $5, $6, 'tron', 'USDT', $7, 6, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING *;

-- name: StartDepositCreate :one
UPDATE deposit_orders SET create_token = $2, create_started_at = now(), updated_at = now()
WHERE id = $1 AND status = 'CREATING' AND create_token IS NULL RETURNING *;

-- name: SaveDepositQuote :one
UPDATE deposit_orders SET trade_id = $2, actual_units = $3, receive_address = $4, quote_json = $5, expires_at = $6,
    reserved_until = $7, status = $8, review_reason = $9, updated_at = now()
WHERE id = $1 AND trade_id IS NULL RETURNING *;

-- name: SetDepositState :one
UPDATE deposit_orders SET status = $2, review_reason = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: LockDepositReservation :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(reservation_key)::TEXT, 0));

-- name: CountUnresolvedDepositCreates :one
SELECT count(*) FROM deposit_orders WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND contract = $4
    AND create_token IS NOT NULL AND trade_id IS NULL AND status IN ('CREATING', 'CREATE_UNKNOWN');

-- name: CountOverlappingDeposits :one
SELECT count(*) FROM deposit_orders
WHERE id <> $1 AND chain_environment = $2 AND genesis_id = $3 AND network = $4 AND contract = $5
    AND receive_address = $6 AND actual_units = $7
    AND create_started_at <= sqlc.arg(reserved_until) AND sqlc.arg(started_at) <= reserved_until;

-- name: CreateDepositJob :exec
INSERT INTO deposit_jobs (id, kind, dedupe_key, order_id, inbox_id, payload) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (kind, dedupe_key) DO NOTHING;

-- name: SetDepositCreateJobState :execrows
UPDATE deposit_jobs SET status = $2, attempts = attempts + sqlc.arg(attempt_delta)::INTEGER, updated_at = now()
WHERE kind = 'CREATE' AND order_id = $1;

-- name: FinishDepositChainJobs :exec
UPDATE deposit_jobs SET status = 'DONE', lease_token = NULL, lease_until = NULL, last_error = '', updated_at = now()
WHERE kind = 'VERIFY_CHAIN' AND order_id = $1 AND dedupe_key = $2;

-- name: SaveDepositInbox :one
INSERT INTO deposit_callback_inbox (id, chain_environment, event_id, facts_hash, payload_hash, key_version, merchant_pid, gateway_order_id,
    trade_id, requested_units, actual_units, receive_address, token, tx_hash, raw_payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (chain_environment, event_id) DO UPDATE SET deliveries = deposit_callback_inbox.deliveries + 1, last_received_at = now()
WHERE deposit_callback_inbox.facts_hash = EXCLUDED.facts_hash RETURNING *;

-- name: CreateDepositEvent :exec
INSERT INTO deposit_events (id, order_id, inbox_id, actor, actor_id, action, facts) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetDepositReceipt :one
SELECT * FROM deposit_receipts WHERE order_id = $1;

-- name: CreateDepositReceipt :one
INSERT INTO deposit_receipts (id, order_id, chain_environment, genesis_id, network, tx_hash, event_index, confirmed_units, block_time, receipt_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING *;

-- name: GetDepositTransfer :one
SELECT * FROM chain_transfers WHERE chain_environment = $1 AND genesis_id = $2 AND network = $3 AND tx_hash = $4 AND event_index = $5;

-- name: GetDepositBlockProof :one
SELECT block.evidence_json, block.evidence_hash, cursor.evidence_version FROM chain_scan_blocks AS block
JOIN chain_scan_cursors AS cursor USING (chain_environment, genesis_id, network, scanner)
WHERE block.chain_environment = $1 AND block.genesis_id = $2 AND block.network = $3 AND block.block_height = $4
    AND block.block_id = $5 AND cursor.contract = $6
ORDER BY cursor.evidence_version DESC, block.scanner LIMIT 1;
