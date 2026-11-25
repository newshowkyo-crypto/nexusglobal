-- 000049: Provider 冻结同步状态机
-- 记录每个 CARD_FROZEN_SUSPEND 事件的 Provider 侧冻结同步状态。
-- 保证幂等：同一 event_id / idempotency_key / provider_request_key 最多产生一次有效 Provider 调用。

CREATE TABLE IF NOT EXISTS card_provider_freeze_syncs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id            TEXT NOT NULL,
    idempotency_key     TEXT NOT NULL,
    card_id             UUID NOT NULL,
    provider_id         UUID NOT NULL,
    provider_card_ref   TEXT NOT NULL,
    provider_request_key TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'PENDING',
    attempts            INT NOT NULL DEFAULT 0,
    last_error          TEXT NULL,
    locked_by           TEXT NULL,
    locked_at           TIMESTAMPTZ NULL,
    completed_at        TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 幂等约束：同一事件、同一幂等键、同一 Provider 请求键各只能存在一行
    CONSTRAINT uq_cpfs_event_id UNIQUE (event_id),
    CONSTRAINT uq_cpfs_idempotency_key UNIQUE (idempotency_key),
    CONSTRAINT uq_cpfs_provider_request_key UNIQUE (provider_request_key),

    -- status 只允许以下值
    CONSTRAINT ck_cpfs_status CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'FAILED', 'UNKNOWN_EXTERNAL_STATE')),

    -- attempts 非负
    CONSTRAINT ck_cpfs_attempts CHECK (attempts >= 0)
);

-- 索引：按 status 查询待处理任务
CREATE INDEX IF NOT EXISTS idx_cpfs_status ON card_provider_freeze_syncs (status);
-- 索引：按 card_id 查询同步状态
CREATE INDEX IF NOT EXISTS idx_cpfs_card_id ON card_provider_freeze_syncs (card_id);