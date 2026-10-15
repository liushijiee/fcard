DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM chain_scan_cursors WHERE evidence_version = 2) THEN
        RAISE EXCEPTION 'cannot downgrade while version 2 chain evidence cursors exist';
    END IF;
END
$$;

ALTER TABLE chain_scan_cursors DROP CONSTRAINT chain_scan_cursors_evidence_version_check;
ALTER TABLE chain_scan_cursors ADD CONSTRAINT chain_scan_cursors_evidence_version_check CHECK (evidence_version = 1);
