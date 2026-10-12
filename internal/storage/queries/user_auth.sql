-- name: GetUserLogin :one
SELECT sqlc.embed(users), sqlc.embed(user_credentials)
FROM users JOIN user_credentials ON user_credentials.user_id = users.id WHERE users.email = $1;

-- name: LockUserCredential :one
SELECT * FROM user_credentials WHERE user_id = $1 FOR UPDATE;

-- name: ResetUserCredential :exec
INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
must_change_password = true, auth_version = user_credentials.auth_version + 1, password_changed_at = now();

-- name: ChangeUserCredential :exec
UPDATE user_credentials SET password_hash = $2, must_change_password = false,
auth_version = auth_version + 1, password_changed_at = now() WHERE user_id = $1;

-- name: InvalidateUserCredential :exec
UPDATE user_credentials SET auth_version = auth_version + 1 WHERE user_id = $1;

-- name: RevokeUserSessions :exec
UPDATE user_sessions SET revoked = true WHERE user_id = $1;

-- name: GetCredentialState :one
SELECT EXISTS(SELECT 1 FROM user_credentials WHERE user_credentials.user_id = sqlc.arg(user_id)) AS login_enabled,
COALESCE((SELECT must_change_password FROM user_credentials WHERE user_credentials.user_id = sqlc.arg(user_id)), false)::boolean AS must_change_password;

-- name: CreateUserSession :exec
INSERT INTO user_sessions (id, user_id, token_hash, auth_version, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetUserSession :one
SELECT sqlc.embed(users), user_sessions.id AS session_id, user_credentials.auth_version, user_credentials.must_change_password
FROM user_sessions JOIN users ON users.id = user_sessions.user_id
JOIN user_credentials ON user_credentials.user_id = users.id
WHERE token_hash = $1 AND NOT revoked AND expires_at > now() AND users.status = 'ACTIVE'
AND user_sessions.auth_version = user_credentials.auth_version;

-- name: LockActiveUserSession :one
SELECT * FROM user_sessions WHERE id = $1 AND user_id = $2 AND NOT revoked AND expires_at > now() FOR UPDATE;

-- name: RevokeUserSession :exec
UPDATE user_sessions SET revoked = true WHERE id = $1 AND user_id = $2;

-- name: CreateUserSecurityEvent :exec
INSERT INTO user_security_events (id, user_id, actor_type, actor_id, action, request_id, changes) VALUES ($1, $2, $3, $4, $5, $6, $7);
