-- name: ClaimDepositVerificationJob :one
WITH candidate AS (
    SELECT ready.id FROM deposit_jobs AS ready WHERE ready.kind = $1 AND ready.kind <> 'CREATE' AND ready.status = 'PENDING'
        AND ready.next_attempt_at <= clock_timestamp() AND ready.attempts < sqlc.arg(max_attempts)::INTEGER
    ORDER BY ready.next_attempt_at, ready.created_at, ready.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE deposit_jobs AS job SET status = 'IN_FLIGHT', lease_token = $2,
    lease_until = clock_timestamp() + sqlc.arg(lease_seconds)::INTEGER * interval '1 second',
    attempts = job.attempts + 1, updated_at = clock_timestamp()
FROM candidate WHERE job.id = candidate.id RETURNING job.*;

-- name: LockDepositJob :one
SELECT * FROM deposit_jobs WHERE id = $1 FOR UPDATE;

-- name: RenewDepositJobLease :one
UPDATE deposit_jobs SET lease_until = clock_timestamp() + sqlc.arg(lease_seconds)::INTEGER * interval '1 second', updated_at = clock_timestamp()
WHERE id = $1 AND kind <> 'CREATE' AND status = 'IN_FLIGHT' AND lease_token = $2 AND lease_until > clock_timestamp() RETURNING *;

-- name: SetLeasedDepositJobState :one
UPDATE deposit_jobs SET status = $3, order_id = $4, lease_token = NULL, lease_until = NULL, last_error = $5,
    next_attempt_at = clock_timestamp() + sqlc.arg(retry_seconds)::INTEGER * interval '1 second', updated_at = clock_timestamp()
WHERE id = $1 AND kind <> 'CREATE' AND status = 'IN_FLIGHT' AND lease_token = $2 AND lease_until > clock_timestamp() RETURNING *;

-- name: RecoverDepositVerificationJobs :execrows
WITH expired AS (
    SELECT ready.id FROM deposit_jobs AS ready WHERE ready.kind = sqlc.arg(kind)::TEXT AND ready.kind <> 'CREATE'
        AND ready.status = 'IN_FLIGHT' AND ready.lease_until <= clock_timestamp()
    ORDER BY ready.lease_until, ready.id FOR UPDATE SKIP LOCKED LIMIT 100
)
UPDATE deposit_jobs AS job SET status = CASE WHEN job.attempts >= sqlc.arg(max_attempts)::INTEGER THEN 'MANUAL_REVIEW' ELSE 'PENDING' END,
    lease_token = NULL, lease_until = NULL, last_error = 'LEASE_EXPIRED',
    next_attempt_at = clock_timestamp() + sqlc.arg(retry_seconds)::INTEGER * interval '1 second', updated_at = clock_timestamp()
FROM expired WHERE job.id = expired.id;

-- name: ExhaustDepositVerificationJobs :execrows
WITH exhausted AS (
    SELECT ready.id FROM deposit_jobs AS ready WHERE ready.kind = sqlc.arg(kind)::TEXT AND ready.kind <> 'CREATE'
        AND ready.status = 'PENDING' AND ready.attempts >= sqlc.arg(max_attempts)::INTEGER
    ORDER BY ready.created_at, ready.id FOR UPDATE SKIP LOCKED LIMIT 100
)
UPDATE deposit_jobs AS job SET status = 'MANUAL_REVIEW', last_error = 'ATTEMPT_LIMIT', updated_at = clock_timestamp()
FROM exhausted WHERE job.id = exhausted.id;

-- name: GetDepositJob :one
SELECT * FROM deposit_jobs WHERE id = $1;

-- name: GetDepositInbox :one
SELECT * FROM deposit_callback_inbox WHERE id = $1;

-- name: FindDepositCallbackOrder :one
SELECT * FROM deposit_orders WHERE chain_environment = $1 AND merchant_pid = $2 AND gateway_order_id = $3;
