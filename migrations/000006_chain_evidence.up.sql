CREATE TABLE chain_scan_cursors (
    chain_environment TEXT NOT NULL CHECK (chain_environment = 'nile'),
    genesis_id TEXT NOT NULL CHECK (genesis_id ~ '^[0-9a-f]{64}$'),
    network TEXT NOT NULL CHECK (network = 'tron'),
    scanner TEXT NOT NULL CHECK (scanner ~ '^[a-z][a-z0-9_-]{0,63}$'),
    contract TEXT NOT NULL CHECK (contract ~ '^41[0-9a-f]{40}$'),
    evidence_version INTEGER NOT NULL CHECK (evidence_version = 1),
    start_height BIGINT NOT NULL CHECK (start_height >= 0),
    start_block_id TEXT NOT NULL CHECK (start_block_id ~ '^[0-9a-f]{64}$'),
    block_height BIGINT NOT NULL CHECK (block_height >= start_height),
    block_id TEXT NOT NULL CHECK (block_id ~ '^[0-9a-f]{64}$'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_environment, genesis_id, network, scanner)
);

CREATE TABLE chain_scan_blocks (
    chain_environment TEXT NOT NULL,
    genesis_id TEXT NOT NULL,
    network TEXT NOT NULL,
    scanner TEXT NOT NULL,
    block_height BIGINT NOT NULL CHECK (block_height > 0),
    block_id TEXT NOT NULL CHECK (block_id ~ '^[0-9a-f]{64}$'),
    parent_id TEXT NOT NULL CHECK (parent_id ~ '^[0-9a-f]{64}$'),
    block_time TIMESTAMPTZ NOT NULL,
    evidence_hash TEXT NOT NULL CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    evidence_json BYTEA NOT NULL CHECK (octet_length(evidence_json) BETWEEN 1 AND 16777216),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_environment, genesis_id, network, scanner, block_height),
    FOREIGN KEY (chain_environment, genesis_id, network, scanner)
        REFERENCES chain_scan_cursors (chain_environment, genesis_id, network, scanner)
);

CREATE TABLE chain_transfers (
    chain_environment TEXT NOT NULL CHECK (chain_environment = 'nile'),
    genesis_id TEXT NOT NULL CHECK (genesis_id ~ '^[0-9a-f]{64}$'),
    network TEXT NOT NULL CHECK (network = 'tron'),
    tx_hash TEXT NOT NULL CHECK (tx_hash ~ '^[0-9a-f]{64}$'),
    event_index INTEGER NOT NULL CHECK (event_index >= 0),
    contract TEXT NOT NULL CHECK (contract ~ '^41[0-9a-f]{40}$'),
    from_address TEXT NOT NULL CHECK (from_address ~ '^41[0-9a-f]{40}$'),
    to_address TEXT NOT NULL CHECK (to_address ~ '^41[0-9a-f]{40}$'),
    raw_units TEXT NOT NULL CHECK (
        raw_units ~ '^(0|[1-9][0-9]{0,77})$' AND
        raw_units::NUMERIC <= 115792089237316195423570985008687907853269984665640564039457584007913129639935
    ),
    block_height BIGINT NOT NULL CHECK (block_height > 0),
    block_id TEXT NOT NULL CHECK (block_id ~ '^[0-9a-f]{64}$'),
    block_time TIMESTAMPTZ NOT NULL,
    receipt_hash TEXT NOT NULL CHECK (receipt_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_environment, genesis_id, network, tx_hash, event_index)
);

CREATE INDEX chain_transfers_recipient_idx
    ON chain_transfers (chain_environment, genesis_id, network, contract, to_address, block_height);
