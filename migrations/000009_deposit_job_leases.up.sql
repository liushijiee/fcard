DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deposit_jobs WHERE kind <> 'CREATE' AND status = 'IN_FLIGHT') THEN
        RAISE EXCEPTION 'review legacy in-flight deposit verification jobs before adding leases';
    END IF;
END
$$;
ALTER TABLE deposit_jobs ADD COLUMN lease_token UUID;
ALTER TABLE deposit_jobs ADD COLUMN lease_until TIMESTAMPTZ;
ALTER TABLE deposit_jobs ADD COLUMN last_error TEXT NOT NULL DEFAULT '' CHECK (last_error ~ '^[A-Z0-9_]{0,64}$');
ALTER TABLE deposit_jobs ADD CONSTRAINT deposit_job_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL));
ALTER TABLE deposit_jobs ADD CONSTRAINT deposit_job_verification_lease CHECK (
    (kind = 'CREATE' AND lease_token IS NULL)
    OR (kind <> 'CREATE' AND ((status = 'IN_FLIGHT') = (lease_token IS NOT NULL)))
);
CREATE INDEX deposit_job_ready_idx ON deposit_jobs(kind, next_attempt_at, created_at, id) WHERE status = 'PENDING';
CREATE INDEX deposit_job_expired_idx ON deposit_jobs(lease_until, id) WHERE lease_token IS NOT NULL;
