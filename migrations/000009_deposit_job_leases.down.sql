DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deposit_jobs WHERE lease_token IS NOT NULL
        OR (kind <> 'CREATE' AND (attempts > 0 OR last_error <> ''))) THEN
        RAISE EXCEPTION 'cannot discard used deposit verification leases and recovery state';
    END IF;
END
$$;
DROP INDEX deposit_job_expired_idx;
DROP INDEX deposit_job_ready_idx;
ALTER TABLE deposit_jobs DROP CONSTRAINT deposit_job_verification_lease;
ALTER TABLE deposit_jobs DROP CONSTRAINT deposit_job_lease_pair;
ALTER TABLE deposit_jobs DROP COLUMN last_error;
ALTER TABLE deposit_jobs DROP COLUMN lease_until;
ALTER TABLE deposit_jobs DROP COLUMN lease_token;
