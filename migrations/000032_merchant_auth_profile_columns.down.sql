-- Merchant Auth Profile Columns rollback

DROP INDEX IF EXISTS idx_corporate_entity_provider_tier;
DROP INDEX IF EXISTS idx_corporate_entity_sign_token_hash;

ALTER TABLE corporate_entity DROP COLUMN IF EXISTS provider_tier;
ALTER TABLE corporate_entity DROP COLUMN IF EXISTS sign_token_hash;
