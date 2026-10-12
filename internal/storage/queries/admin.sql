-- name: CreateAdmin :one
INSERT INTO admins (id, email, password_hash) VALUES ($1, $2, $3) RETURNING *;

-- name: GetAdminByEmail :one
SELECT * FROM admins WHERE email = $1;

-- name: LockAdminByEmail :one
SELECT * FROM admins WHERE email = $1 FOR UPDATE;

-- name: DisableAdmin :exec
UPDATE admins SET status = 'DISABLED' WHERE id = $1;

-- name: RevokeAdminSessions :exec
UPDATE admin_sessions SET revoked = true WHERE admin_id = $1;

-- name: CreateSession :exec
INSERT INTO admin_sessions (id, admin_id, token_hash, expires_at) VALUES ($1, $2, $3, $4);

-- name: GetSessionAdmin :one
SELECT admins.id, admins.email, admins.status, admins.created_at, admin_sessions.id AS session_id
FROM admin_sessions JOIN admins ON admins.id = admin_sessions.admin_id
WHERE token_hash = $1 AND NOT revoked AND expires_at > now() AND admins.status = 'ACTIVE';

-- name: RevokeSession :exec
UPDATE admin_sessions SET revoked = true WHERE id = $1;

-- name: ConsumeLoginAttempt :one
INSERT INTO login_rate_limits (key_hash, attempts, window_start) VALUES ($1, 1, now())
ON CONFLICT (key_hash) DO UPDATE SET
attempts = CASE WHEN login_rate_limits.window_start <= now() - interval '1 minute' THEN 1 ELSE login_rate_limits.attempts + 1 END,
window_start = CASE WHEN login_rate_limits.window_start <= now() - interval '1 minute' THEN now() ELSE login_rate_limits.window_start END
RETURNING attempts;

-- name: CleanupSecurityRecords :exec
WITH expired AS (
    DELETE FROM admin_sessions WHERE expires_at < now() - interval '1 day'
), expired_users AS (
    DELETE FROM user_sessions WHERE expires_at < now() - interval '1 day'
), expired_anonymous AS (
    DELETE FROM user_security_events WHERE actor_type = 'ANONYMOUS' AND created_at < now() - interval '7 days'
)
DELETE FROM login_rate_limits WHERE window_start < now() - interval '1 day';
