-- name: GetCompanyIssuingProfile :one
SELECT * FROM company_issuing_profile WHERE singleton = true;

-- name: BindCompanyIssuingProfile :one
INSERT INTO company_issuing_profile(singleton, provider_cardholder_id, legal_name)
VALUES(true, $1, $2)
ON CONFLICT (singleton) DO UPDATE SET singleton = true
WHERE company_issuing_profile.provider_cardholder_id = EXCLUDED.provider_cardholder_id
RETURNING *;

-- name: CreateSandboxCard :one
INSERT INTO cards(id, user_id, provider_cardholder_id, nick_name, limit_cents)
VALUES($1, $2, $3, $4, $5) RETURNING *;

-- name: CreateCardOperation :one
INSERT INTO provider_operations(id, user_id, card_id, idempotency_key, input_hash, provider_request_id, request_payload, auth_version)
VALUES($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *;

-- name: GetCardOperationByKey :one
SELECT * FROM provider_operations WHERE user_id = $1 AND idempotency_key = $2;

-- name: GetUserCardOperation :one
SELECT * FROM provider_operations WHERE id = $1 AND user_id = $2;

-- name: GetUserCard :one
SELECT * FROM cards WHERE id = $1 AND user_id = $2;

-- name: ListUserCards :many
SELECT * FROM cards WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3;

-- name: CountUserCards :one
SELECT count(*) FROM cards WHERE user_id = $1;

-- name: CountOutstandingCards :one
SELECT count(*) FROM provider_operations WHERE user_id = $1 AND holds_card_slot;

-- name: ClaimCardOperation :one
UPDATE provider_operations SET status = 'PROCESSING', attempts = attempts + 1, started_at = now(), updated_at = now()
WHERE id = (SELECT id FROM provider_operations WHERE status = 'PENDING' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
RETURNING *;

-- name: ExpireCardOperations :exec
UPDATE provider_operations SET status = 'UNKNOWN', error_code = 'interrupted', updated_at = now()
WHERE status = 'PROCESSING' AND started_at < now() - interval '1 minute';

-- name: FinishCardOperation :execrows
UPDATE provider_operations SET status = $2, error_code = $3,
    holds_card_slot = CASE WHEN $2 = 'FAILED' THEN false ELSE holds_card_slot END,
    updated_at = now()
WHERE id = $1 AND status = 'PROCESSING' AND attempts = $4;

-- name: RetryUserCardOperation :one
UPDATE provider_operations SET status = 'PENDING', error_code = '', auth_version = $3, updated_at = now()
WHERE id = $1 AND user_id = $2 AND status = 'UNKNOWN' AND attempts < 5 RETURNING *;

-- name: UpdateIssuedCard :one
UPDATE cards SET provider_card_id = $3, status = $4, last_four = $5, updated_at = now()
WHERE id = $1 AND user_id = $2 AND (status <> 'CLOSED' OR $4 = 'CLOSED') RETURNING *;

-- name: ReleaseClosedCardSlot :execrows
UPDATE provider_operations AS operation
SET holds_card_slot = false, updated_at = now()
FROM cards AS card
WHERE operation.card_id = card.id AND operation.user_id = card.user_id
    AND card.id = $1 AND card.user_id = $2
    AND operation.status = 'SUCCEEDED' AND operation.holds_card_slot
    AND card.status = 'CLOSED' AND card.provider_card_id IS NOT NULL;

-- name: FailSandboxCard :exec
UPDATE cards SET status = 'FAILED', updated_at = now() WHERE id = $1 AND provider_card_id IS NULL;

-- name: GetCardOperationID :one
SELECT id FROM provider_operations WHERE card_id = $1 AND user_id = $2;

-- name: ReconcileCardOperation :execrows
UPDATE provider_operations SET status = 'SUCCEEDED', error_code = '', updated_at = now()
WHERE id = $1 AND user_id = $2 AND status = 'UNKNOWN';
