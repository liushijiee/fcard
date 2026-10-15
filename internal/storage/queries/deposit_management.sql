-- name: LockDepositAdminSession :one
SELECT sessions.id FROM admin_sessions AS sessions JOIN admins ON admins.id = sessions.admin_id
WHERE sessions.id = $1 AND admins.id = $2 AND admins.status = 'ACTIVE'
    AND NOT sessions.revoked AND sessions.expires_at > clock_timestamp()
FOR UPDATE OF admins, sessions;

-- name: GetManagedDeposit :one
SELECT * FROM deposit_orders WHERE id = $1;

-- name: ListUserDeposits :many
SELECT * FROM deposit_orders WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3;

-- name: CountUserDeposits :one
SELECT count(*) FROM deposit_orders WHERE user_id = $1;

-- name: CountUserOpenDeposits :one
SELECT count(*) FROM deposit_orders WHERE user_id=$1 AND status IN ('CREATING','CREATE_UNKNOWN','WAITING','VERIFYING','REVIEW_REQUIRED');

-- name: ListManagedDeposits :many
SELECT * FROM deposit_orders ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- name: CountManagedDeposits :one
SELECT count(*) FROM deposit_orders;

-- name: ListDepositReviewJobs :many
SELECT id, kind, order_id, inbox_id, status, attempts, last_error, updated_at
FROM deposit_jobs WHERE status = 'MANUAL_REVIEW' ORDER BY updated_at DESC, id DESC LIMIT 100;

-- name: ListDepositUnmatchedTransfers :many
SELECT transfers.* FROM chain_transfers AS transfers
WHERE transfers.chain_environment = $1 AND transfers.genesis_id = $2 AND transfers.contract = $3
    AND NOT EXISTS (SELECT 1 FROM deposit_receipts AS receipts WHERE receipts.chain_environment = transfers.chain_environment
        AND receipts.genesis_id = transfers.genesis_id AND receipts.network = transfers.network
        AND receipts.tx_hash = transfers.tx_hash AND receipts.event_index = transfers.event_index)
ORDER BY transfers.block_time DESC, transfers.tx_hash, transfers.event_index LIMIT 100;

-- name: ListDepositOrderEvents :many
SELECT id, actor, actor_id, action, facts, created_at FROM deposit_events WHERE order_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: ListExpiringDeposits :many
SELECT id FROM deposit_orders WHERE status IN ('WAITING','VERIFYING') AND expires_at <= clock_timestamp()
ORDER BY expires_at, id LIMIT 100;

-- name: RejectPendingDepositCreate :execrows
UPDATE deposit_jobs SET status = 'MANUAL_REVIEW', last_error = $2, updated_at = clock_timestamp()
WHERE kind = 'CREATE' AND order_id = $1 AND status = 'PENDING' AND attempts = 0;

-- name: RequeueDepositVerification :one
UPDATE deposit_jobs SET status = 'PENDING', attempts = 0, last_error = '', lease_token = NULL, lease_until = NULL,
    next_attempt_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND kind <> 'CREATE' AND status = 'MANUAL_REVIEW' RETURNING *;

-- name: GetDepositOperationalCounts :one
SELECT
    (SELECT count(*) FROM deposit_orders WHERE create_token IS NOT NULL AND trade_id IS NULL AND status IN ('CREATING','CREATE_UNKNOWN')) AS unresolved_creates,
    (SELECT count(*) FROM deposit_jobs WHERE status = 'MANUAL_REVIEW') AS review_jobs,
    (SELECT count(*) FROM deposit_jobs WHERE status = 'PENDING') AS pending_jobs;
