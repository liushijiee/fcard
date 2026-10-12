CREATE TABLE provider_cardholders (
    user_id UUID PRIMARY KEY REFERENCES users(id),
    provider_cardholder_id TEXT NOT NULL UNIQUE,
    legal_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider_cardholder_id)
);
CREATE TABLE cards (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    provider_cardholder_id TEXT NOT NULL,
    provider_card_id TEXT UNIQUE,
    nick_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'REQUESTED',
    last_four TEXT NOT NULL DEFAULT '' CHECK (last_four = '' OR last_four ~ '^[0-9]{4}$'),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
    limit_cents BIGINT NOT NULL CHECK (limit_cents BETWEEN 1 AND 10000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, user_id),
    FOREIGN KEY (user_id, provider_cardholder_id) REFERENCES provider_cardholders(user_id, provider_cardholder_id)
);
CREATE INDEX cards_user_idx ON cards(user_id, created_at DESC);
CREATE TABLE provider_operations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    card_id UUID NOT NULL UNIQUE,
    idempotency_key TEXT NOT NULL,
    input_hash BYTEA NOT NULL,
    provider_request_id TEXT NOT NULL UNIQUE,
    request_payload JSONB NOT NULL,
    auth_version BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','PROCESSING','SUCCEEDED','FAILED','UNKNOWN')),
    error_code TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (card_id, user_id) REFERENCES cards(id, user_id),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX provider_operations_pending_idx ON provider_operations(created_at) WHERE status = 'PENDING';
CREATE UNIQUE INDEX provider_operations_one_card_idx ON provider_operations(user_id) WHERE status <> 'FAILED';
