-- Migration: 000021_init_bank_collection (DOWN)
-- Description: Rollback Bank Collection specific tables

DROP TABLE IF EXISTS bank_collection_reconciliation_log;
DROP TABLE IF EXISTS bank_collection_webhook_log;
DROP TABLE IF EXISTS bank_collection_virtual_account_pool;
DROP TABLE IF EXISTS bank_collection_intent;