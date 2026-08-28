ALTER TABLE assurance.intents
    ADD COLUMN IF NOT EXISTS model_family String AFTER attestation_level,
    ADD COLUMN IF NOT EXISTS model_id String AFTER model_family
