-- 000068_risk_rule_pooling.down.sql

DROP INDEX IF EXISTS idx_risk_event_site;
DROP INDEX IF EXISTS idx_risk_event_channel;

ALTER TABLE risk_event DROP COLUMN IF EXISTS site_id;
ALTER TABLE risk_event DROP COLUMN IF EXISTS channel_id;

DROP TABLE IF EXISTS risk_rule;
