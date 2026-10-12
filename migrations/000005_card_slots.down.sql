DROP INDEX provider_operations_one_card_idx;
ALTER TABLE provider_operations DROP CONSTRAINT provider_operations_slot_state_check;
ALTER TABLE provider_operations DROP COLUMN holds_card_slot;
CREATE UNIQUE INDEX provider_operations_one_card_idx ON provider_operations(user_id) WHERE status <> 'FAILED';
