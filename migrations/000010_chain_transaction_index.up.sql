CREATE TABLE chain_scan_transactions (
    chain_environment TEXT NOT NULL,
    genesis_id TEXT NOT NULL,
    network TEXT NOT NULL,
    scanner TEXT NOT NULL,
    tx_hash TEXT NOT NULL CHECK (tx_hash ~ '^[0-9a-f]{64}$'),
    block_height BIGINT NOT NULL,
    block_id TEXT NOT NULL CHECK (block_id ~ '^[0-9a-f]{64}$'),
    block_time TIMESTAMPTZ NOT NULL,
    receipt_hash TEXT NOT NULL CHECK (receipt_hash ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (chain_environment, genesis_id, network, scanner, tx_hash),
    FOREIGN KEY (chain_environment, genesis_id, network, scanner, block_height)
        REFERENCES chain_scan_blocks(chain_environment, genesis_id, network, scanner, block_height)
);
CREATE INDEX chain_scan_transactions_lookup_idx ON chain_scan_transactions(chain_environment, genesis_id, network, tx_hash);
CREATE INDEX chain_scan_transactions_block_idx ON chain_scan_transactions(chain_environment, genesis_id, network, scanner, block_height);
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM chain_scan_blocks WHERE
        jsonb_typeof(convert_from(evidence_json,'UTF8')::JSONB->'Transactions') IS DISTINCT FROM 'array'
        AND jsonb_typeof(convert_from(evidence_json,'UTF8')::JSONB->'Transactions') IS DISTINCT FROM 'null') THEN
        RAISE EXCEPTION 'invalid legacy transaction manifest';
    END IF;
END
$$;
INSERT INTO chain_scan_transactions(chain_environment, genesis_id, network, scanner, tx_hash, block_height, block_id, block_time, receipt_hash)
SELECT block.chain_environment, block.genesis_id, block.network, block.scanner,
    evidence.value->>'TxHash', block.block_height, block.block_id, block.block_time, evidence.value->>'ReceiptHash'
FROM chain_scan_blocks AS block
CROSS JOIN LATERAL jsonb_array_elements(NULLIF(convert_from(block.evidence_json, 'UTF8')::JSONB->'Transactions','null'::JSONB)) AS evidence(value);
