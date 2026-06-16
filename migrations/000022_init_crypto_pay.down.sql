-- Migration: 000022_init_crypto_pay (DOWN)
-- Description: Rollback Crypto Pay specific tables

DROP TABLE IF EXISTS crypto_pay_listener_config;
DROP TABLE IF EXISTS crypto_pay_chain_transaction;
DROP TABLE IF EXISTS crypto_pay_address_pool;
DROP TABLE IF EXISTS crypto_pay_deposit_intent;