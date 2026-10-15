DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM chain_scan_transactions) THEN
        RAISE EXCEPTION 'cannot discard persisted chain transaction index';
    END IF;
END
$$;
DROP TABLE chain_scan_transactions;
