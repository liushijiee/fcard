DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deposit_orders) OR EXISTS (SELECT 1 FROM deposit_callback_inbox) THEN
        RAISE EXCEPTION 'cannot discard persisted deposit orders or callback evidence';
    END IF;
END
$$;
DROP TABLE deposit_events;
DROP TABLE deposit_receipts;
DROP TABLE deposit_jobs;
DROP TABLE deposit_callback_inbox;
DROP TABLE deposit_orders;
DROP FUNCTION protect_deposit_snapshot();
DROP FUNCTION enforce_deposit_confirmation();
DROP FUNCTION reject_deposit_fact_mutation();
