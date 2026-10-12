CREATE TABLE user_credentials (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    password_hash TEXT NOT NULL,
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    auth_version BIGINT NOT NULL DEFAULT 1 CHECK (auth_version > 0),
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    token_hash BYTEA NOT NULL UNIQUE,
    auth_version BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_sessions_user_idx ON user_sessions(user_id);

CREATE TABLE user_security_events (
    id UUID PRIMARY KEY,
    user_id UUID REFERENCES users(id),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('ADMIN', 'USER', 'ANONYMOUS')),
    actor_id UUID,
    action TEXT NOT NULL,
    request_id TEXT NOT NULL,
    changes JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((actor_type = 'ANONYMOUS' AND actor_id IS NULL) OR (actor_type <> 'ANONYMOUS' AND actor_id IS NOT NULL))
);
CREATE INDEX user_security_events_user_idx ON user_security_events(user_id, created_at DESC);
