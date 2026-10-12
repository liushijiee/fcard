-- name: CreateUser :one
INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetUser :one
SELECT sqlc.embed(users), (user_credentials.user_id IS NOT NULL)::boolean AS login_enabled,
COALESCE(user_credentials.must_change_password, false) AS must_change_password
FROM users LEFT JOIN user_credentials ON user_credentials.user_id = users.id WHERE users.id = $1;

-- name: LockUser :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: ListUsers :many
SELECT sqlc.embed(users), (user_credentials.user_id IS NOT NULL)::boolean AS login_enabled,
COALESCE(user_credentials.must_change_password, false) AS must_change_password
FROM users LEFT JOIN user_credentials ON user_credentials.user_id = users.id
ORDER BY users.created_at DESC, users.id DESC LIMIT $1 OFFSET $2;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUser :one
UPDATE users SET email = $2, display_name = $3, status = $4, updated_at = now() WHERE id = $1 RETURNING *;

-- name: CreateAudit :exec
INSERT INTO audit_logs (id, admin_id, action, target_id, request_id, changes) VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAuditLogs :many
SELECT * FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- name: CountAuditLogs :one
SELECT count(*) FROM audit_logs;
