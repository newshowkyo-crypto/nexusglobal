# NexusGlobal 2.0 多模态资产流转 V3 合规增补蓝图

对象：Trae / 工程执行端  
关联文件：`MULTIMODAL_ASSET_ADAPTER_BLUEPRINT_TRAE.md`  
边界：本文只面向合规支付、持牌账户、可审计资金流、KYC/KYB/AML、制裁筛查、PCI-DSS、链上地址白名单和受监管清结算场景。任何被表述为规避风控、隐藏资金来源、反追踪、斩断链上关系、规避银行或交易所审查的需求，都必须改写为合规的风险隔离、流动性管理、限额控制、审计留痕、人工复核和 fail-closed 状态机。

---

## 1. 多模态入金与合规出金全景数据流拓扑图

```text
Inbound Layer
  Site App / API Client
    -> ProcessInbound()
       -> CARD_ACQUIRER adapter
          -> Stripe / Nxtapay / other PSP
          -> 3DS / tokenized payment method / webhook
       -> BANK_COLLECTION adapter
          -> Airwallex / Payoneer / bank virtual account
          -> ACH / Wire / SEPA / FPS collection reference
       -> CRYPTO_PAY adapter
          -> gateway / custody / self-hosted listener
          -> USDT / USDC / BTC address or memo/tag
    -> Risk Gateway
    -> Multi-Asset Ledger

Entity Isolation Layer
  tenant
    -> corporate_entity A
       -> site group A
       -> inbound channels A
       -> bank accounts A
       -> exchange / LP accounts A
       -> wallet policy A
    -> corporate_entity B
       -> site group B
       -> inbound channels B
       -> bank accounts B
       -> exchange / LP accounts B
       -> wallet policy B

Treasury Bridge Layer
  Asset_Bridge_Profile
    -> buffer_percent
    -> batching_policy
    -> fx_conversion_policy
    -> bridge route steps
    -> regulated exchange or licensed LP
    -> order book / quote check
    -> max slippage 20 bps
    -> limit / sliced order
    -> allowlisted withdrawal policy
    -> ledger finalization
    -> reconciliation
```

合规解释：

- `buffer_percent` 用于流动性准备、拒付缓冲、手续费储备和运营连续性，不得用于伪装或误导外部金融机构。
- `batching_policy` 用于削峰、银行 cutoff 管理、API 限流、执行风险分散和可审计调度，不得用于规避监控。
- `fx_conversion_policy` 用于合法币种敞口管理、手续费优化和支持不同清算走廊，不得用于隐藏资金来源或目的。
- `withdrawal_policy` 用于白名单地址、限额、托管隔离和链上对账，不得用于阻碍合法追踪。

---

## 2. 多模态通用配置中心表结构变更

### 2.1 Corporate Entity

```sql
CREATE TABLE corporate_entity (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    entity_code TEXT NOT NULL,
    legal_name TEXT NOT NULL,
    jurisdiction TEXT NOT NULL,
    kyb_status TEXT NOT NULL CHECK (kyb_status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED')),
    risk_status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (risk_status IN ('ACTIVE', 'RISK_HOLD', 'DISABLED')),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, entity_code)
);

ALTER TABLE adapter_channel
ADD COLUMN IF NOT EXISTS corporate_entity_id UUID NULL REFERENCES corporate_entity(id);

ALTER TABLE asset_bridge_profile
ADD COLUMN IF NOT EXISTS corporate_entity_id UUID NULL REFERENCES corporate_entity(id);
```

要求：

- A 实体 RiskHold 只能阻断 A 实体绑定的站点、通道、银行账户、交易所/LP 账户和钱包策略。
- 不允许在未配置审批策略时自动把 A 实体流量切到 B 实体。
- 所有实体状态变更必须写 audit log。

### 2.2 Asset Bridge Profile Policy Fields

```sql
ALTER TABLE asset_bridge_profile
ADD COLUMN IF NOT EXISTS buffer_percent NUMERIC(8, 6) NOT NULL DEFAULT 0,
ADD COLUMN IF NOT EXISTS batching_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS fx_conversion_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS execution_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS withdrawal_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS compliance_policy JSONB NOT NULL DEFAULT '{}'::jsonb;
```

示例：

```json
{
  "buffer_percent": "0.12",
  "batching_policy": {
    "mode": "SLICED",
    "min_delay_seconds": 3600,
    "max_delay_seconds": 14400,
    "max_batch_amount": "25000.00",
    "audit_reason_required": true
  },
  "fx_conversion_policy": {
    "enabled": true,
    "allowed_pairs": ["USD/EUR", "USD/GBP"],
    "max_spread_bps": 30,
    "quote_ttl_seconds": 60
  },
  "execution_policy": {
    "max_allowed_slippage": "0.002",
    "order_style": "LIMIT_SLICED",
    "market_order_allowed": false
  },
  "withdrawal_policy": {
    "allowlist_required": true,
    "max_single_withdrawal": "10000",
    "daily_limit": "50000",
    "confirmation_required": true
  }
}
```

### 2.3 Transit Wallet Policy

```sql
CREATE TABLE transit_wallet_policy (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    corporate_entity_id UUID NULL REFERENCES corporate_entity(id),
    policy_code TEXT NOT NULL,
    asset_id UUID NOT NULL REFERENCES asset_definition(id),
    network TEXT NOT NULL,
    destination_type TEXT NOT NULL CHECK (destination_type IN ('TRANSIT', 'COLD_WALLET', 'CUSTODY')),
    address TEXT NOT NULL,
    label TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED', 'RISK_HOLD')),
    max_single_withdrawal NUMERIC(36, 18) NULL,
    daily_limit NUMERIC(36, 18) NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, network, address)
);
```

要求：

- 只允许 ACTIVE allowlisted address。
- 必须匹配 asset/network。
- 必须执行单笔限额和日限额。
- withdrawal 完成前不得 ledger finalize。
- tx hash 必须幂等。

---

## 3. 通用接口契约

### 3.1 ProcessInbound

将卡网中心化的 `Charge()` 升级为资产中性接口：

```text
type UniversalInboundProcessor interface {
  ProcessInbound(ctx, req) (InboundResult, error)
  QueryInbound(ctx, req) (InboundStatus, error)
  VerifyInboundEvent(ctx, req) (InboundEvent, error)
}
```

`ProcessInbound()` 必须兼容：

- 卡网：authorize/capture、3DS redirect、tokenized payment method。
- 银行虚拟户：virtual account / IBAN / routing number / payment reference。
- Crypto Pay：deposit address / memo-tag / confirmations threshold。

`Charge()` 可以保留为 CARD_ACQUIRER 兼容 wrapper，但新的 orchestration 不能继续绑定 card-only 语义。

### 3.2 Asset Bridge Step

```text
type AssetBridgeStep interface {
  Prepare(ctx, job) error
  Enqueue(ctx, job) error
  Reconcile(ctx, job) error
  Compensate(ctx, job) error
}
```

所有外部写动作必须 outbox 化：

- bank transfer create。
- FX conversion quote accept。
- exchange order place。
- LP quote accept。
- withdrawal request。

---

## 4. 给 Trae 的 V3 原子任务

### Task 1.1A：Corporate Entity Isolation

涉及文件：

```text
internal/entity/domain/domain.go
internal/entity/repository/repository.go
internal/entity/service/service.go
migrations/000025_init_corporate_entity.up.sql
migrations/000025_init_corporate_entity.down.sql
```

核心逻辑：

- 新增 `corporate_entity`。
- `adapter_channel` 和 `asset_bridge_profile` 支持绑定 `corporate_entity_id`。
- 实体 `RISK_HOLD` 时阻断该实体的新入金、sweep、bridge job。
- 不允许跨实体静默 fallback。

防虚报 checklist：

- 是否有实体 RiskHold 隔离测试？
- A 实体 RiskHold 是否不会阻断 B 实体？
- 是否禁止未授权跨实体 fallback？
- 是否写 audit log？

### Task 1.2A：Treasury Policy Fields

涉及文件：

```text
internal/assetbridge/domain/domain.go
internal/assetbridge/service/service.go
internal/assetbridge/worker/worker.go
migrations/000026_asset_bridge_policy_fields.up.sql
migrations/000026_asset_bridge_policy_fields.down.sql
```

核心逻辑：

- 新增 `buffer_percent`、`batching_policy`、`fx_conversion_policy`、`execution_policy`、`withdrawal_policy`、`compliance_policy`。
- sweepable amount 必须扣除 dispute reserve、RiskHold、compliance hold、buffer。
- batch schedule 必须持久化并可审计。
- FX quote 必须记录 quote、rate、spread、provider_ref。

防虚报 checklist：

- 是否有 buffer 计算测试？
- 是否有 dispute reserve + buffer 同时生效测试？
- batch 是否持久化？
- timeout 是否进入 UNKNOWN_EXTERNAL_STATE？
- 是否没有在 DB transaction 内调外部 API？

### Task 1.3A：Regulated LP / Exchange Bridge Slot

涉及文件：

```text
internal/adapter/provider/lp.go
internal/adapter/provider/exchange.go
internal/assetbridge/worker/exchange_step.go
internal/assetbridge/worker/lp_step.go
```

核心逻辑：

- 支持 `EXCHANGE` 和 `LP` adapter type。
- Exchange convert 必须先取 order book。
- LP convert 必须先取 quote，且 quote 有 TTL。
- `max_allowed_slippage = 0.002`。
- 默认禁止 market order。
- 使用 limit/sliced order 或 LP quote accept。
- slippage breach、quote expired、liquidity insufficient、compliance hold 均进入 `APPROVAL_REQUIRED`。

防虚报 checklist：

- 是否有 slippage > 20bps 测试？
- 是否有 quote expiry 测试？
- 是否禁止默认 market order？
- ASSET_EXCHANGE 是否有分录测试？
- 是否所有外部写动作走 outbox？

### Task 1.4A：Allowlisted Withdrawal Policy

涉及文件：

```text
internal/walletpolicy/domain/domain.go
internal/walletpolicy/repository/repository.go
internal/walletpolicy/service/service.go
internal/assetbridge/worker/withdrawal_step.go
migrations/000027_init_transit_wallet_policy.up.sql
migrations/000027_init_transit_wallet_policy.down.sql
```

核心逻辑：

- 新增 `transit_wallet_policy`。
- withdrawal 只能发往 ACTIVE allowlisted address。
- 强制 asset/network match。
- 强制单笔限额和日限额。
- 记录 tx hash、confirmations、reconciliation status。
- confirmations 不足不得 ledger finalize。

防虚报 checklist：

- 非白名单地址是否拒绝？
- wrong network 是否拒绝？
- 超限额是否拒绝？
- tx hash 是否幂等？
- confirmations 不足是否阻断 finalize？

---

## 5. V3 验收命令

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/adapter/... -count=1 -v
go test ./internal/entity/... -count=1 -v
go test ./internal/assetbridge/... -count=1 -v
go test ./internal/walletpolicy/... -count=1 -v
go test ./internal/ledger/... -count=1 -v
go test ./internal/checkout/... -count=1 -v
go test ./test/integration -run TestLocalDemo_FullFlow -count=1 -v
go test ./... -short -count=1
```
