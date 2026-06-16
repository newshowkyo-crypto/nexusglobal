-- Migration: 000020_init_universal_adapter_registry (DOWN)
-- Description: Rollback Universal Adapter Registry

-- Drop tables in reverse order
DROP TABLE IF EXISTS corporate_entity_audit_log;
DROP TABLE IF EXISTS corporate_entity;
DROP TABLE IF EXISTS transit_wallet_policy;
DROP TABLE IF EXISTS asset_bridge_outbox;
DROP TABLE IF EXISTS asset_bridge_job;
DROP TABLE IF EXISTS asset_bridge_profile;
DROP TABLE IF EXISTS adapter_credential_vault;
DROP TABLE IF EXISTS adapter_channel;
DROP TABLE IF EXISTS adapter_provider;
DROP TABLE IF EXISTS asset_definition;

-- Remove columns from ledger tables
ALTER TABLE ledger_entry
DROP COLUMN IF EXISTS asset_id;

ALTER TABLE ledger_account
DROP COLUMN IF EXISTS asset_id;