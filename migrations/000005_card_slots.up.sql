ALTER TABLE provider_operations ADD COLUMN holds_card_slot BOOLEAN NOT NULL DEFAULT true;

UPDATE provider_operations AS operation
SET holds_card_slot = false
FROM cards AS card
WHERE operation.card_id = card.id AND operation.user_id = card.user_id
    AND (operation.status = 'FAILED'
        OR (operation.status = 'SUCCEEDED' AND card.status = 'CLOSED' AND card.provider_card_id IS NOT NULL));

DROP INDEX provider_operations_one_card_idx;
CREATE UNIQUE INDEX provider_operations_one_card_idx ON provider_operations(user_id) WHERE holds_card_slot;

ALTER TABLE provider_operations ADD CONSTRAINT provider_operations_slot_state_check
    CHECK ((status <> 'FAILED' OR NOT holds_card_slot)
        AND (holds_card_slot OR status IN ('FAILED', 'SUCCEEDED')));
