-- 000064_settlement_statement.up.sql
-- A2 日终对账单：按 (商户, 币种, 营业日) 生成支付/退款/结算三段明细与汇总。
--
-- 依赖时间戳：
--   * payment_attempts.captured_at —— 捕获发生时间（对账归属日）
--   * refund_request.succeeded_at —— 退款成功时间

ALTER TABLE payment_attempts ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE refund_request  ADD COLUMN IF NOT EXISTS succeeded_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_payment_attempts_captured_at
    ON payment_attempts(merchant_id, currency, captured_at)
    WHERE captured_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_refund_request_succeeded_at
    ON refund_request(merchant_id, currency, succeeded_at)
    WHERE succeeded_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS settlement_statement (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid        NOT NULL,
    merchant_id           uuid        NOT NULL,
    currency              varchar(3)  NOT NULL,
    business_date         date        NOT NULL,
    status                varchar(16) NOT NULL DEFAULT 'READY'
                            CHECK (status IN ('READY', 'VOID')),
    payment_count         integer     NOT NULL DEFAULT 0,
    payment_amount_minor  bigint      NOT NULL DEFAULT 0,
    refund_count          integer     NOT NULL DEFAULT 0,
    refund_amount_minor   bigint      NOT NULL DEFAULT 0,
    fee_amount_minor      bigint      NOT NULL DEFAULT 0,
    net_amount_minor      bigint      NOT NULL DEFAULT 0,
    settled_amount_minor  bigint      NOT NULL DEFAULT 0,
    generated_at          timestamptz NOT NULL DEFAULT now(),
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, currency, business_date)
);

CREATE INDEX IF NOT EXISTS idx_settlement_statement_merchant
    ON settlement_statement(merchant_id, business_date DESC);

CREATE TABLE IF NOT EXISTS settlement_statement_line (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    statement_id      uuid        NOT NULL REFERENCES settlement_statement(id) ON DELETE CASCADE,
    line_type         varchar(16) NOT NULL CHECK (line_type IN ('PAYMENT', 'REFUND', 'SETTLEMENT')),
    reference_id      uuid        NOT NULL,
    merchant_order_id varchar(128) NOT NULL DEFAULT '',
    amount_minor      bigint      NOT NULL,
    currency          varchar(3)  NOT NULL,
    occurred_at       timestamptz NOT NULL,
    description       varchar(256) NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_settlement_statement_line_stmt
    ON settlement_statement_line(statement_id, line_type, occurred_at);
