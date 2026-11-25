-- Merchant Auth Profile Columns

-- Add signing token hash for merchant API authentication.
ALTER TABLE corporate_entity
ADD COLUMN IF NOT EXISTS sign_token_hash TEXT NULL;

-- Add merchant tier for rate limiting and risk segmentation.
ALTER TABLE corporate_entity
ADD COLUMN IF NOT EXISTS provider_tier TEXT NOT NULL DEFAULT 'TIER_2_COMPLIANT';

-- Index merchant authentication and tier lookup fields.
CREATE INDEX IF NOT EXISTS idx_corporate_entity_sign_token_hash
ON corporate_entity(sign_token_hash)
WHERE sign_token_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_corporate_entity_provider_tier
ON corporate_entity(provider_tier);

COMMENT ON COLUMN corporate_entity.sign_token_hash IS 'SHA256 hash of merchant API signing token for authentication';
COMMENT ON COLUMN corporate_entity.provider_tier IS 'Merchant tier for rate limiting: TIER_1_HIGH_RISK (10 RPS), TIER_2_COMPLIANT (50 RPS)';
