ALTER TABLE cards ADD CONSTRAINT cards_user_id_provider_cardholder_id_fkey
    FOREIGN KEY (user_id, provider_cardholder_id)
    REFERENCES provider_cardholders(user_id, provider_cardholder_id);

DROP TABLE company_issuing_profile;
