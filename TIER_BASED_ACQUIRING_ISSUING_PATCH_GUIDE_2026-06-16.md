# NexusGlobal 3.0 Tier-Based Acquiring & Issuing Patch Guide

生成日期：2026-06-16  
适用对象：Trae / Mimo / 后续开发执行端  
补丁目标：将多模态收单与虚拟卡发卡统一改造为 Tier-Based Abstraction，禁止 provider-specific 业务逻辑散落在 service 层。

---

## 0. 总原则

本补丁只允许实现：

- provider tier 抽象。
- issuing tier 抽象。
- funding mode 抽象。
- 凭证 vault key 化。
- 风险分层路由。
- 后场异步资金调拨。
- 双轨账本 provenance 保留。
- Dashboard 动态配置和热重载。

本补丁禁止实现：

- 隐藏资金来源。
- 绕过 AML、KYC、制裁筛查或卡组织规则。
- provider hard-code 业务分支。
- production fallback mock。
- `BURN_MODE` 自动放行。
- 明文返回或落库 provider credentials。
- 卡号 PAN/CVV 明文落库、日志或 API 响应。

`BURN_MODE` 如因历史字段必须存在，代码层必须解释为：

```text
MANUAL_REVIEW_LOCKDOWN
```

即：更严格、更保守、进入人工复核，不得自动处理新交易或新开卡。

---

## 1. 数据库补丁目标

新增 migration：

```text
migrations/000028_tier_based_acquiring_issuing.up.sql
migrations/000028_tier_based_acquiring_issuing.down.sql
```

### 1.1 ENUM

```sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'provider_tier') THEN
        CREATE TYPE provider_tier AS ENUM (
            'TIER_1_HIGH_RISK',
            'TIER_2_COMPLIANT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'channel_strategy_mode') THEN
        CREATE TYPE channel_strategy_mode AS ENUM (
            'DEFAULT',
            'BURN_MODE'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'issuing_tier') THEN
        CREATE TYPE issuing_tier AS ENUM (
            'TIER_1_AGGREGATOR',
            'TIER_2_DIRECT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'issuing_funding_mode') THEN
        CREATE TYPE issuing_funding_mode AS ENUM (
            'CRYPTO_DESENSITIZED',
            'FIAT_DIRECT'
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'virtual_card_status') THEN
        CREATE TYPE virtual_card_status AS ENUM (
            'PENDING',
            'ACTIVE',
            'FROZEN',
            'TERMINATED',
            'UNKNOWN_EXTERNAL_STATE',
            'MANUAL_REVIEW'
        );
    END IF;
END $$;
```

### 1.2 `acquirer_channels`

```sql
CREATE TABLE IF NOT EXISTS acquirer_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id UUID NOT NULL REFERENCES corporate_entity(id),
    provider_tier provider_tier NOT NULL,
    provider_code TEXT NOT NULL,
    channel_code TEXT NOT NULL,
    credential_vault_key TEXT NOT NULL,
    strategy_mode channel_strategy_mode NOT NULL DEFAULT 'DEFAULT',
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    version INT NOT NULL DEFAULT 1,
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    routing_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_acquirer_channel_code UNIQUE (entity_id, channel_code),
    CONSTRAINT chk_acquirer_provider_code_not_empty CHECK (length(provider_code) > 0),
    CONSTRAINT chk_acquirer_vault_key_not_empty CHECK (length(credential_vault_key) > 0),
    CONSTRAINT chk_acquirer_provider_config_object CHECK (jsonb_typeof(provider_config) = 'object'),
    CONSTRAINT chk_acquirer_routing_config_object CHECK (jsonb_typeof(routing_config) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_acquirer_channels_entity ON acquirer_channels(entity_id);
CREATE INDEX IF NOT EXISTS idx_acquirer_channels_tier ON acquirer_channels(provider_tier);
CREATE INDEX IF NOT EXISTS idx_acquirer_channels_status ON acquirer_channels(status);
```

### 1.3 `issuing_providers`

```sql
CREATE TABLE IF NOT EXISTS issuing_providers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id UUID NULL REFERENCES corporate_entity(id),
    issuing_tier issuing_tier NOT NULL,
    funding_mode issuing_funding_mode NOT NULL,
    provider_code TEXT NOT NULL,
    pool_code TEXT NOT NULL,
    credential_vault_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    version INT NOT NULL DEFAULT 1,
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    velocity_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_issuing_pool_code UNIQUE (entity_id, pool_code),
    CONSTRAINT chk_issuing_provider_code_not_empty CHECK (length(provider_code) > 0),
    CONSTRAINT chk_issuing_vault_key_not_empty CHECK (length(credential_vault_key) > 0),
    CONSTRAINT chk_issuing_provider_config_object CHECK (jsonb_typeof(provider_config) = 'object'),
    CONSTRAINT chk_issuing_velocity_config_object CHECK (jsonb_typeof(velocity_config) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_issuing_providers_tier ON issuing_providers(issuing_tier);
CREATE INDEX IF NOT EXISTS idx_issuing_providers_funding_mode ON issuing_providers(funding_mode);
CREATE INDEX IF NOT EXISTS idx_issuing_providers_status ON issuing_providers(status);
```

### 1.4 `virtual_cards`

```sql
CREATE TABLE IF NOT EXISTS virtual_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID NOT NULL,
    provider_id UUID NOT NULL REFERENCES issuing_providers(id),
    provider_card_ref TEXT NULL,
    last4 TEXT NULL,
    status virtual_card_status NOT NULL DEFAULT 'PENDING',
    currency TEXT NOT NULL DEFAULT 'USD',
    spending_limit_minor BIGINT NOT NULL DEFAULT 0,
    declined_count_sliding_window INT NOT NULL DEFAULT 0,
    declined_window_started_at TIMESTAMPTZ NULL,
    fraud_rate_bps INT NOT NULL DEFAULT 0,
    last_decline_reason TEXT NULL,
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    created_by TEXT NOT NULL,
    frozen_reason TEXT NULL,
    terminated_reason TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_virtual_card_idempotency UNIQUE (merchant_id, idempotency_key),
    CONSTRAINT chk_virtual_card_limit_nonnegative CHECK (spending_limit_minor >= 0),
    CONSTRAINT chk_virtual_card_declines_nonnegative CHECK (declined_count_sliding_window >= 0)
);

CREATE INDEX IF NOT EXISTS idx_virtual_cards_merchant ON virtual_cards(merchant_id);
CREATE INDEX IF NOT EXISTS idx_virtual_cards_provider ON virtual_cards(provider_id);
CREATE INDEX IF NOT EXISTS idx_virtual_cards_status ON virtual_cards(status);
```

---

## 2. Task 1.1 - Unified AcquirerAdapter

目标：所有收单服务商都只实现统一接口，业务层只看 `provider_tier` 和 channel config。

路径：

```text
internal/acquirer/adapter.go
internal/acquirer/domain.go
internal/acquirer/registry.go
internal/acquirer/service.go
internal/acquirer/adapter_contract_test.go
```

接口：

```go
type AcquirerAdapter interface {
    Authorize(ctx context.Context, req AuthorizeRequest) (*AuthorizeResult, error)
    Capture(ctx context.Context, req CaptureRequest) (*CaptureResult, error)
    Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
    VerifyWebhook(ctx context.Context, req VerifyWebhookRequest) (*VerifiedWebhookEvent, error)
}
```

禁止：

```go
if providerCode == "stripe" { ... }
if providerCode == "nxtapay" { ... }
```

允许：

```go
factory.Create(ctx, channel.ProviderCode, credential)
registry.SelectByTier(ctx, entityID, ProviderTierHighRisk)
```

Fail-closed：

- provider_code 未注册 factory。
- credential_vault_key 为空。
- Vault 解密失败。
- provider_tier 未知。
- `strategy_mode = BURN_MODE`。
- `status != ACTIVE`。
- webhook 验签失败。
- provider timeout/ambiguous status。

---

## 3. Task 1.2 - Unified IssuingAdapter

目标：所有发卡卡池都只实现统一接口，业务层只看 `issuing_tier` 和 `funding_mode`。

路径：

```text
internal/issuing/adapter.go
internal/issuing/domain.go
internal/issuing/registry.go
internal/issuing/service.go
internal/issuing/fraud_guard.go
internal/issuing/adapter_contract_test.go
```

接口：

```go
type IssuingAdapter interface {
    CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error)
    FreezeCard(ctx context.Context, req FreezeCardRequest) error
    TerminateCard(ctx context.Context, req TerminateCardRequest) error
    ProcessAuthWebhook(ctx context.Context, req ProcessAuthWebhookRequest) (*CardAuthEvent, error)
}
```

Fraud Guard：

```go
type FraudGuard interface {
    CheckMerchantIssuingAllowed(ctx context.Context, merchantID uuid.UUID) error
    RecordAuthEvent(ctx context.Context, event CardAuthEvent) error
}
```

Fail-closed：

- issuing provider 非 ACTIVE。
- credential 解密失败。
- funding_mode 与资金来源不匹配。
- merchant fraud rate 超阈值。
- card declined sliding window 超阈值。
- ProcessAuthWebhook 验签失败。
- provider timeout 后不得重复开卡。
- production 禁用 mock issuing adapter。

---

## 4. Task 1.3 - Dual-Rail Funding Router

目标：在资金进入 VCC funding 前，按收单 tier 和发卡 funding_mode 做硬约束。

路径：

```text
internal/treasury/sweep.go
internal/treasury/vcc_funding_router.go
internal/treasury/vcc_funding_router_test.go
internal/worker/vcc_funding/worker.go
internal/worker/vcc_funding/worker_test.go
```

逻辑：

```text
TIER_1_HIGH_RISK + FIAT_DIRECT
    -> hard reject

TIER_1_HIGH_RISK + CRYPTO_DESENSITIZED
    -> async crypto bridge workflow

TIER_2_COMPLIANT + FIAT_DIRECT
    -> fiat direct allowed

TIER_2_COMPLIANT + CRYPTO_DESENSITIZED
    -> crypto bridge allowed
```

高风险路径必须异步：

```text
DB transaction:
1. lock merchant settled balance
2. debit merchant settled balance
3. credit pending crypto bridge balance
4. write outbox event: VCC_CRYPTO_BRIDGE_REQUESTED
5. commit

Worker:
1. claim outbox
2. call LP conversion
3. write crypto pool ledger entries
4. mark completed or UNKNOWN_EXTERNAL_STATE
5. trigger VCC funding event
```

必须保留 metadata：

```json
{
  "source_tier": "TIER_1_HIGH_RISK",
  "source_rail": "acquirer_channel_id",
  "source_ledger_tx_id": "...",
  "crypto_bridge_batch_id": "...",
  "lp_conversion_ref": "...",
  "target_issuing_provider_id": "..."
}
```

Fail-closed：

- high-risk + fiat direct。
- unknown source tier。
- unknown funding mode。
- amount <= 0。
- source balance insufficient。
- merchant risk profile missing。
- issuing provider missing。
- LP timeout -> UNKNOWN_EXTERNAL_STATE。
- conversion slippage > threshold。
- DB transaction 内出现外呼。
- missing provenance metadata。

---

## 5. Task 1.4 - Dashboard Dynamic Config

目标：运营可在 Dashboard 动态增加任意 provider，只选择 tier、funding_mode、vault key 和 JSON config；代码层无感热重载。

路径：

```text
internal/admin/v3_channels_handler.go
internal/admin/v3_issuing_pools_handler.go
internal/admin/v3_channels_handler_test.go
internal/admin/v3_issuing_pools_handler_test.go
internal/platform/httpx/dashboard.go
internal/platform/httpx/dashboard_test.go
cmd/api/main.go
```

API：

```text
GET    /admin/api/v3/channels
POST   /admin/api/v3/channels
PATCH  /admin/api/v3/channels/{id}
POST   /admin/api/v3/channels/{id}/enable
POST   /admin/api/v3/channels/{id}/disable
POST   /admin/api/v3/channels/{id}/reload

GET    /admin/api/v3/issuing/pools
POST   /admin/api/v3/issuing/pools
PATCH  /admin/api/v3/issuing/pools/{id}
POST   /admin/api/v3/issuing/pools/{id}/enable
POST   /admin/api/v3/issuing/pools/{id}/disable
POST   /admin/api/v3/issuing/pools/{id}/reload
```

Dashboard 新增 tab：

- 收单通道 Tier 管理。
- 发卡卡池 Tier 管理。

热重载 topic：

```text
config:acquirer_channel:reload
config:issuing_pool:reload
```

Fail-closed：

- provider_code 为空。
- credential_vault_key 为空。
- tier 未知。
- funding_mode 未知。
- provider_config 非 object。
- velocity_config 非 object。
- reload 失败时新配置不得进入 active cache。
- Dashboard 回显 credential 明文。
- Dashboard 按钮调用不存在 API。

---

## 6. 验收命令

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/acquirer/... -count=1 -v
go test ./internal/issuing/... -count=1 -v
go test ./internal/treasury/... -count=1 -v
go test ./internal/admin -count=1 -v
go test ./internal/platform/httpx -count=1 -v
go test ./... -count=1
```

---

## 7. 交付报告格式

```text
## Tier-Based Abstraction Patch 完成报告

### 1. 数据库迁移
- 000028 是否完成:
- acquirer_channels:
- issuing_providers:
- virtual_cards:

### 2. AcquirerAdapter
- 是否完成统一接口:
- 是否仍有 provider hard-code:
- BURN_MODE 是否 fail-closed:

### 3. IssuingAdapter
- 是否完成统一接口:
- Fraud Guard 是否完成:
- card/merchant 熔断是否完成:

### 4. Funding Router
- TIER_1_HIGH_RISK + FIAT_DIRECT 是否硬阻断:
- CRYPTO_DESENSITIZED 是否异步:
- provenance metadata 是否保留:

### 5. Dashboard
- /admin/api/v3/channels 是否完成:
- /admin/api/v3/issuing/pools 是否完成:
- 热重载是否完成:
- credential 是否无明文回显:

### 6. 测试结果
粘贴完整命令输出。

### 7. 未完成项
必须如实列出。
```

