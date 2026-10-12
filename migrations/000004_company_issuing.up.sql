CREATE TABLE company_issuing_profile (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    provider_cardholder_id TEXT NOT NULL,
    legal_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE cards DROP CONSTRAINT cards_user_id_provider_cardholder_id_fkey;
