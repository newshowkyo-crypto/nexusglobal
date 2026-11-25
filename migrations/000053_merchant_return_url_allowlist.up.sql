-- PAY-002: 商户回跳 URL 域名 allowlist。
-- Checkout Session / Payment Link 的 success_url / cancel_url 的 host
-- 必须先登记在此表，fail-closed：未登记 = 拒绝创建。
CREATE TABLE merchant_return_url_allowlist (
    merchant_id uuid        NOT NULL,
    host        text        NOT NULL CHECK (host = lower(host) AND host <> ''),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (merchant_id, host)
);

CREATE INDEX idx_merchant_return_url_allowlist_merchant
    ON merchant_return_url_allowlist (merchant_id);
