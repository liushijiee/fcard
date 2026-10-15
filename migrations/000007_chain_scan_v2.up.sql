ALTER TABLE chain_scan_cursors DROP CONSTRAINT chain_scan_cursors_evidence_version_check;
ALTER TABLE chain_scan_cursors ADD CONSTRAINT chain_scan_cursors_evidence_version_check CHECK (evidence_version IN (1, 2));
