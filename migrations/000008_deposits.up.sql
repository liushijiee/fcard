CREATE TABLE deposit_orders (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    auth_version BIGINT NOT NULL CHECK (auth_version > 0),
    idempotency_key UUID NOT NULL,
    requested_units BIGINT NOT NULL CHECK (requested_units > 10000 AND requested_units <= 1000000000000),
    chain_environment TEXT NOT NULL CHECK (chain_environment = 'nile'),
    genesis_id TEXT NOT NULL CHECK (genesis_id ~ '^0000000000000000[0-9a-f]{48}$'),
    network TEXT NOT NULL CHECK (network = 'tron'),
    currency TEXT NOT NULL CHECK (currency = 'USDT'),
    contract TEXT NOT NULL CHECK (contract ~ '^41[0-9a-f]{40}$'),
    decimals INTEGER NOT NULL CHECK (decimals = 6),
    merchant_pid TEXT NOT NULL CHECK (length(merchant_pid) BETWEEN 1 AND 128),
    gateway_order_id TEXT NOT NULL UNIQUE CHECK (gateway_order_id ~ '^[0-9a-f]{32}$'),
    notify_url TEXT NOT NULL,
    redirect_url TEXT NOT NULL,
    gateway_origin TEXT NOT NULL,
    max_quote_seconds INTEGER NOT NULL CHECK (max_quote_seconds BETWEEN 60 AND 3600),
    quarantine_seconds INTEGER NOT NULL CHECK (quarantine_seconds BETWEEN 3600 AND 604800),
    status TEXT NOT NULL DEFAULT 'CREATING' CHECK (status IN ('CREATING', 'CREATE_UNKNOWN', 'WAITING', 'VERIFYING', 'CONFIRMED', 'EXPIRED', 'REVIEW_REQUIRED', 'FAILED')),
    create_token UUID,
    create_started_at TIMESTAMPTZ,
    trade_id TEXT CHECK (trade_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    actual_units BIGINT CHECK (actual_units >= requested_units AND actual_units - requested_units <= 99 AND actual_units <= 1000000000000),
    receive_address TEXT CHECK (receive_address ~ '^41[0-9a-f]{40}$'),
    quote_json JSONB,
    expires_at TIMESTAMPTZ,
    reserved_until TIMESTAMPTZ,
    review_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key),
    UNIQUE (chain_environment, merchant_pid, trade_id),
    CHECK ((create_token IS NULL) = (create_started_at IS NULL)),
    CHECK ((trade_id IS NULL AND actual_units IS NULL AND receive_address IS NULL AND quote_json IS NULL AND expires_at IS NULL AND reserved_until IS NULL)
        OR (trade_id IS NOT NULL AND actual_units IS NOT NULL AND receive_address IS NOT NULL AND quote_json IS NOT NULL AND create_started_at IS NOT NULL
            AND expires_at IS NOT NULL AND reserved_until IS NOT NULL AND expires_at > create_started_at
            AND expires_at <= create_started_at + max_quote_seconds * interval '1 second'
            AND reserved_until = expires_at + quarantine_seconds * interval '1 second')),
    CHECK (status NOT IN ('WAITING', 'VERIFYING', 'CONFIRMED', 'EXPIRED') OR trade_id IS NOT NULL)
);
CREATE INDEX deposit_orders_user_idx ON deposit_orders(user_id, created_at DESC, id DESC);
CREATE INDEX deposit_orders_reservation_idx ON deposit_orders(chain_environment, genesis_id, network, contract, receive_address, actual_units);

CREATE FUNCTION protect_deposit_snapshot() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.id, NEW.user_id, NEW.auth_version, NEW.idempotency_key, NEW.requested_units,
        NEW.chain_environment, NEW.genesis_id, NEW.network, NEW.currency, NEW.contract, NEW.decimals,
        NEW.merchant_pid, NEW.gateway_order_id, NEW.notify_url, NEW.redirect_url, NEW.gateway_origin,
        NEW.max_quote_seconds, NEW.quarantine_seconds, NEW.created_at)
        IS DISTINCT FROM ROW(OLD.id, OLD.user_id, OLD.auth_version, OLD.idempotency_key, OLD.requested_units,
        OLD.chain_environment, OLD.genesis_id, OLD.network, OLD.currency, OLD.contract, OLD.decimals,
        OLD.merchant_pid, OLD.gateway_order_id, OLD.notify_url, OLD.redirect_url, OLD.gateway_origin,
        OLD.max_quote_seconds, OLD.quarantine_seconds, OLD.created_at) THEN
        RAISE EXCEPTION 'deposit request snapshot is immutable';
    END IF;
    IF OLD.create_token IS NOT NULL AND ROW(NEW.create_token, NEW.create_started_at)
        IS DISTINCT FROM ROW(OLD.create_token, OLD.create_started_at) THEN
        RAISE EXCEPTION 'deposit create claim is immutable';
    END IF;
    IF OLD.trade_id IS NOT NULL AND ROW(NEW.trade_id, NEW.actual_units, NEW.receive_address, NEW.quote_json, NEW.expires_at, NEW.reserved_until)
        IS DISTINCT FROM ROW(OLD.trade_id, OLD.actual_units, OLD.receive_address, OLD.quote_json, OLD.expires_at, OLD.reserved_until) THEN
        RAISE EXCEPTION 'deposit quote snapshot is immutable';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER deposit_snapshot_immutable BEFORE UPDATE ON deposit_orders
    FOR EACH ROW EXECUTE FUNCTION protect_deposit_snapshot();

CREATE TABLE deposit_callback_inbox (
    id UUID PRIMARY KEY,
    chain_environment TEXT NOT NULL CHECK (chain_environment = 'nile'),
    event_id TEXT NOT NULL CHECK (event_id ~ '^[0-9a-f]{64}$'),
    facts_hash TEXT NOT NULL CHECK (facts_hash ~ '^[0-9a-f]{64}$'),
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    key_version TEXT NOT NULL CHECK (key_version ~ '^[A-Za-z0-9._-]{1,64}$'),
    merchant_pid TEXT NOT NULL,
    gateway_order_id TEXT NOT NULL CHECK (gateway_order_id ~ '^[0-9a-f]{32}$'),
    trade_id TEXT NOT NULL,
    requested_units BIGINT NOT NULL CHECK (requested_units > 10000 AND requested_units <= 1000000000000),
    actual_units BIGINT NOT NULL CHECK (actual_units > 10000 AND actual_units <= 1000000000000),
    receive_address TEXT NOT NULL,
    token TEXT NOT NULL,
    tx_hash TEXT NOT NULL CHECK (tx_hash ~ '^[0-9a-f]{64}$'),
    raw_payload BYTEA NOT NULL CHECK (octet_length(raw_payload) BETWEEN 1 AND 16384),
    deliveries BIGINT NOT NULL DEFAULT 1 CHECK (deliveries >= 1),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain_environment, event_id)
);
CREATE INDEX deposit_callback_order_idx ON deposit_callback_inbox(chain_environment, merchant_pid, gateway_order_id);

CREATE TABLE deposit_jobs (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('CREATE', 'VERIFY_CALLBACK', 'VERIFY_CHAIN')),
    dedupe_key TEXT NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 128),
    order_id UUID REFERENCES deposit_orders(id),
    inbox_id UUID REFERENCES deposit_callback_inbox(id),
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'IN_FLIGHT', 'MANUAL_REVIEW', 'DONE')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kind, dedupe_key),
    CHECK ((kind = 'CREATE' AND order_id IS NOT NULL AND inbox_id IS NULL)
        OR (kind = 'VERIFY_CALLBACK' AND inbox_id IS NOT NULL)
        OR (kind = 'VERIFY_CHAIN' AND order_id IS NOT NULL))
);

CREATE TABLE deposit_receipts (
    id UUID PRIMARY KEY,
    order_id UUID NOT NULL UNIQUE REFERENCES deposit_orders(id),
    chain_environment TEXT NOT NULL,
    genesis_id TEXT NOT NULL,
    network TEXT NOT NULL,
    tx_hash TEXT NOT NULL,
    event_index INTEGER NOT NULL,
    confirmed_units BIGINT NOT NULL CHECK (confirmed_units > 10000 AND confirmed_units <= 1000000000000),
    block_time TIMESTAMPTZ NOT NULL,
    receipt_hash TEXT NOT NULL CHECK (receipt_hash ~ '^[0-9a-f]{64}$'),
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain_environment, genesis_id, network, tx_hash, event_index),
    FOREIGN KEY (chain_environment, genesis_id, network, tx_hash, event_index)
        REFERENCES chain_transfers(chain_environment, genesis_id, network, tx_hash, event_index)
);

CREATE TABLE deposit_events (
    id UUID PRIMARY KEY,
    order_id UUID REFERENCES deposit_orders(id),
    inbox_id UUID REFERENCES deposit_callback_inbox(id),
    actor TEXT NOT NULL CHECK (actor IN ('SYSTEM', 'USER', 'ADMIN')),
    actor_id UUID,
    action TEXT NOT NULL,
    facts JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((actor = 'SYSTEM' AND actor_id IS NULL) OR (actor <> 'SYSTEM' AND actor_id IS NOT NULL)),
    CHECK (order_id IS NOT NULL OR inbox_id IS NOT NULL)
);

CREATE FUNCTION reject_deposit_fact_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'deposit confirmation and audit facts are append-only';
END
$$;
CREATE TRIGGER deposit_receipts_append_only BEFORE UPDATE OR DELETE ON deposit_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_deposit_fact_mutation();
CREATE TRIGGER deposit_events_append_only BEFORE UPDATE OR DELETE ON deposit_events
    FOR EACH ROW EXECUTE FUNCTION reject_deposit_fact_mutation();

CREATE FUNCTION enforce_deposit_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_order UUID;
    saved_order deposit_orders;
    saved_receipt deposit_receipts;
BEGIN
    IF TG_TABLE_NAME = 'deposit_orders' THEN
        target_order := NEW.id;
    ELSE
        target_order := NEW.order_id;
    END IF;
    SELECT * INTO saved_order FROM deposit_orders WHERE id = target_order;
    SELECT * INTO saved_receipt FROM deposit_receipts WHERE order_id = target_order;
    IF (saved_order.status = 'CONFIRMED') <> (saved_receipt.id IS NOT NULL) THEN
        RAISE EXCEPTION 'confirmed order and unique receipt must be committed together';
    END IF;
    IF saved_receipt.id IS NOT NULL AND (
        saved_receipt.confirmed_units <> saved_order.actual_units OR
        saved_receipt.chain_environment <> saved_order.chain_environment OR
        saved_receipt.genesis_id <> saved_order.genesis_id OR saved_receipt.network <> saved_order.network OR
        NOT EXISTS (SELECT 1 FROM chain_transfers AS transfer
            WHERE transfer.chain_environment = saved_receipt.chain_environment AND transfer.genesis_id = saved_receipt.genesis_id
            AND transfer.network = saved_receipt.network AND transfer.tx_hash = saved_receipt.tx_hash AND transfer.event_index = saved_receipt.event_index
            AND transfer.contract = saved_order.contract AND transfer.to_address = saved_order.receive_address
            AND transfer.raw_units = saved_receipt.confirmed_units::TEXT AND transfer.block_time = saved_receipt.block_time
            AND transfer.receipt_hash = saved_receipt.receipt_hash
            AND transfer.block_time >= saved_order.create_started_at AND transfer.block_time <= saved_order.expires_at)
    ) THEN
        RAISE EXCEPTION 'deposit receipt does not match the saved quote and transfer';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER deposit_order_confirmation AFTER INSERT OR UPDATE ON deposit_orders
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_deposit_confirmation();
CREATE CONSTRAINT TRIGGER deposit_receipt_confirmation AFTER INSERT ON deposit_receipts
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_deposit_confirmation();
