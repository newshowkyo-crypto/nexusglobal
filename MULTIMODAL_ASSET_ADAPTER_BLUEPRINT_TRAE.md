# NexusGlobal 2.0 通用多模态资产流转与热插拔适配器架构全景蓝图

版本：V2.0 Multi-Modal Refactor  
对象：Trae / 工程执行端  
边界：本文只面向合规支付、持牌账户、可审计资金流、KYC/KYB/AML、制裁筛查、PCI-DSS、链上地址白名单和受监管清结算场景。任何外部银行、数字银行、卡网、交易所、LP、链上节点连接器都必须在合法主体、授权账户、沙箱联调和生产审批下实施。

---

## 0. 总体纠偏

此前系统的 `ChannelRegistry` 和 checkout 链路已经完成卡网 mock/local 闭环，但抽象仍偏向 card acquiring。下一阶段必须把系统从“卡网收单平台”升级为“通用多模态资产流转中台”。

核心目标：

- 入金方式资产中性化：Card、Bank Collection、Crypto Pay 都是 Inbound Adapter。
- 通道对象资产中性化：不再把 channel 等同于 card acquirer。
- 账本资产中性化：USD、EUR、USDT、USDC、BTC 都是一等资产。
- 出金与对冲资产中性化：Bank Rail、Exchange、LP、On-chain Withdrawal 都是 Asset Bridge 的 step。
- 所有外部写请求 outbox 化，所有外部未知状态 fail-closed。

---

## 1. 多模态入金与异构出金全景拓扑图

### 1.1 全景拓扑

```text
                         +--------------------------------------+
                         |             Admin Console             |
                         | assets / adapters / bridge profiles   |
                         | routing / risk / limits / credentials |
                         +-------------------+------------------+
                                             |
                                             | config update
                                             v
                    +------------------------------------------------+
                    | Redis Pub/Sub + Config Version + Risk Windows  |
                    +-------------------+----------------------------+
                                        |
                                        | hot reload
                                        v
                         +-----------------------------+
                         | Universal Adapter Registry  |
                         | CARD / BANK / CRYPTO / FX   |
                         +--------------+--------------+
                                        |
                 +----------------------+----------------------+
                 |                                             |
                 v                                             v
      +----------------------+                       +----------------------+
      | Inbound Orchestrator |                       | Universal Sweep      |
      | order intent / quote |                       | Asset Bridge Engine  |
      +----------+-----------+                       +----------+-----------+
                 |                                              |
   +-------------+-------------+                    +-----------+------------+
   |             |             |                    |           |            |
   v             v             v                    v           v            v
+------+   +------------+   +-----------+       +-------+   +---------+   +---------+
| Card |   | Bank       |   | Crypto    |       | Bank  |   | Exchange|   | Onchain |
| PSP  |   | Collection |   | Pay       |       | Rail  |   | / LP    |   | Wallet  |
+--+---+   +-----+------+   +-----+-----+       +---+---+   +----+----+   +----+----+
   |             |                |                 |            |             |
   | auth/cap    | virtual acct   | address/RPC     | wire/sepa  | limit order | withdraw
   | webhook     | bank webhook   | chain listener   | status     | fill poll   | tx poll
   v             v                v                 v            v             v
+-------------------------------------------------------------------------------+
|                           Multi-Asset Ledger                                   |
| USD / EUR / GBP / USDT / USDC / BTC / fees / reserves / holds / disputes       |
+-------------------------------------------------------------------------------+
```

### 1.2 三种入金路径

#### A. 卡网入金 Card Acquiring

```text
Site App
  -> /public/v1/order-intents
  -> Anti-Carding Gateway
  -> Universal Adapter Registry
  -> CARD_ACQUIRER_ADAPTER
  -> Stripe / Nxtapay / other PSP
  -> provider webhook / query
  -> Ledger: PaymentCaptured / Receivable / Fees / Reserve
```

关键约束：

- real mode 禁止 raw PAN/CVV。
- 必须使用 tokenized payment method。
- provider timeout -> `UnknownExternalState`。
- 失败计数进入 Redis 风控窗口。

#### B. 数字银行 / 实体银行 Collection 入金

```text
Site App
  -> /public/v1/order-intents
  -> Inbound Orchestrator
  -> BANK_COLLECTION_ADAPTER
  -> allocate virtual receiving account
     - ACH account/routing
     - IBAN
     - local clearing reference
  -> buyer bank transfer
  -> bank/neobank webhook or statement poller
  -> Ledger: BankCreditPending -> BankCreditConfirmed
```

关键约束：

- 每个订单必须有唯一 reconciliation reference。
- 虚拟户分配必须可回收、可过期、可冻结。
- 银行 webhook 不可信时必须通过 statement poller 二次确认。
- 金额/币种/付款人/引用不匹配时进入 `ReconcileMismatch`。

#### C. Crypto Pay 入金

```text
Site App
  -> /public/v1/order-intents
  -> CRYPTO_PAY_ADAPTER
  -> allocate deposit address or memo/tag
     - USDT-TRC20
     - USDT-ERC20
     - USDC
     - BTC
  -> chain/RPC/indexer listener
  -> confirmations threshold
  -> Ledger: CryptoDepositPending -> CryptoDepositConfirmed
```

关键约束：

- 地址必须来自受控地址池或合规托管账户。
- 链上确认数不足不得入账为 confirmed。
- 地址复用策略必须按风险策略配置。
- memo/tag 类资产必须强制校验 memo。
- 只允许白名单资产与网络。

### 1.3 通用出金与资产桥路径

```text
Inbound Confirmed Asset
  |
  v
[Multi-Asset Ledger Available Balance]
  |
  | check:
  | - reserve floor
  | - dispute reserve
  | - RiskHold
  | - compliance hold
  | - liquidity threshold
  v
[Asset Bridge Profile]
  |
  | route steps:
  | 1. bank sweep / internal transfer / crypto sweep
  | 2. exchange or LP deposit
  | 3. order book depth check
  | 4. max slippage 20 bps
  | 5. limit/sliced order
  | 6. withdrawal to allowlisted custody/cold wallet
  v
[Universal Sweep Worker]
  |
  | outbox per external write
  v
[External Connector]
  |
  | Bank Rail / Exchange / LP / Onchain Wallet
  v
[Reconcile Worker]
  |
  v
[Multi-Asset Ledger Finalization]
```

---

## 2. 多模态重构数据库迁移脚本规格

### 2.1 设计原则

必须新增通用资产、通用适配器、通用凭证、资产桥 profile，而不是继续把所有东西塞进 card acquirer 表。

设计原则：

- `asset` 是一等对象，不再只靠 `currency string`。
- `adapter` 是一等对象，不再只靠 `payment_provider`。
- `channel` 是 adapter 的实例，可代表卡网、银行虚拟户、crypto address allocator、exchange、LP、wallet。
- credential vault 必须通用化，敏感信息统一 AES-GCM 加密，AAD 绑定 adapter/channel/context。
- adapter config 使用 JSONB，但必须有 `adapter_type` 和 contract schema 约束。
- ledger account 必须支持 fiat 和 crypto asset。

### 2.2 Migration 草案：资产表

```sql
CREATE TABLE asset_definition (
    id UUID PRIMARY KEY,
    asset_code TEXT NOT NULL UNIQUE,
    asset_type TEXT NOT NULL CHECK (asset_type IN ('FIAT', 'CRYPTO', 'STABLECOIN')),
    network TEXT NULL,
    decimals INT NOT NULL,
    canonical_symbol TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_asset_definition_code_network
ON asset_definition(asset_code, COALESCE(network, ''));
```

示例：

```text
USD / FIAT / decimals=2
EUR / FIAT / decimals=2
USDT / STABLECOIN / TRON / decimals=6
USDT / STABLECOIN / ETHEREUM / decimals=6
USDC / STABLECOIN / ETHEREUM / decimals=6
BTC / CRYPTO / BITCOIN / decimals=8
```

### 2.3 Migration 草案：通用适配器仓库

```sql
CREATE TABLE adapter_provider (
    id UUID PRIMARY KEY,
    provider_code TEXT NOT NULL UNIQUE,
    provider_name TEXT NOT NULL,
    adapter_type TEXT NOT NULL CHECK (
        adapter_type IN (
            'CARD_ACQUIRER',
            'BANK_COLLECTION',
            'CRYPTO_PAY',
            'BANK_RAIL',
            'EXCHANGE',
            'LP',
            'ONCHAIN_WALLET'
        )
    ),
    provider_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE adapter_channel (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    merchant_id UUID NULL,
    provider_id UUID NOT NULL REFERENCES adapter_provider(id),
    channel_code TEXT NOT NULL,
    channel_name TEXT NOT NULL,
    adapter_type TEXT NOT NULL,
    supported_assets JSONB NOT NULL DEFAULT '[]'::jsonb,
    supported_networks JSONB NOT NULL DEFAULT '[]'::jsonb,
    channel_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    routing_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED', 'RISK_HOLD')),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, channel_code)
);

CREATE INDEX idx_adapter_channel_tenant_type
ON adapter_channel(tenant_id, adapter_type, status);
```

兼容映射：

- 原 `payment_provider` 可迁移到 `adapter_provider`，`adapter_type='CARD_ACQUIRER'`。
- 原 `tenant_acquirer_channel` 可迁移到 `adapter_channel`。
- Phase 迁移期允许双表并存，但新代码必须以通用 interface 为中心。

### 2.4 Migration 草案：通用凭证 Vault

```sql
CREATE TABLE adapter_credential_vault (
    id UUID PRIMARY KEY,
    provider_id UUID NOT NULL REFERENCES adapter_provider(id),
    channel_id UUID NULL REFERENCES adapter_channel(id),
    credential_type TEXT NOT NULL,
    key_version INT NOT NULL DEFAULT 1,
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    aad_context TEXT NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REVOKED', 'EXPIRED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_adapter_credential_provider_channel
ON adapter_credential_vault(provider_id, channel_id, credential_type, status);
```

`credential_type` 示例：

```text
CARD_SECRET_KEY
CARD_WEBHOOK_SECRET
BANK_API_KEY
BANK_API_SECRET
BANK_CLIENT_CERT
EXCHANGE_API_KEY
EXCHANGE_API_SECRET
EXCHANGE_WITHDRAW_KEY
CRYPTO_RPC_TOKEN
WALLET_SIGNING_KEY_REFERENCE
```

注意：

- 私钥原则上只保存 KMS/HSM reference，不保存明文。
- API 永远不返回明文 secret。
- 日志永远不打印 secret、token、私钥、完整 bank account。

### 2.5 Migration 草案：多资产账本

如果现有 ledger 只以 `currency` 表达资产，应新增资产 ID 或资产元数据字段。

```sql
ALTER TABLE ledger_account
ADD COLUMN IF NOT EXISTS asset_id UUID NULL REFERENCES asset_definition(id);

ALTER TABLE ledger_transaction_entry
ADD COLUMN IF NOT EXISTS asset_id UUID NULL REFERENCES asset_definition(id);
```

建议最终账户类型扩展：

```text
MERCHANT_RECEIVABLE
MERCHANT_SETTLED
MERCHANT_CRYPTO_SETTLED
PLATFORM_FEE
PLATFORM_RESERVE
PLATFORM_DISPUTE_RESERVE
BANK_CLEARING
EXCHANGE_CLEARING
ONCHAIN_CLEARING
ASSET_BRIDGE_IN_TRANSIT
ASSET_EXCHANGE_GAIN_LOSS
MERCHANT_NEGATIVE_BALANCE
```

### 2.6 Migration 草案：资产兑换与桥接

```sql
CREATE TABLE asset_bridge_profile (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    profile_code TEXT NOT NULL,
    source_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    target_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    route_steps JSONB NOT NULL,
    max_allowed_slippage NUMERIC(10, 8) NOT NULL DEFAULT 0.002,
    min_sweep_amount NUMERIC(36, 18) NOT NULL DEFAULT 0,
    reserve_floor NUMERIC(36, 18) NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    version INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, profile_code)
);

CREATE TABLE asset_bridge_job (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    merchant_id UUID NULL,
    profile_id UUID NOT NULL REFERENCES asset_bridge_profile(id),
    source_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    target_asset_id UUID NOT NULL REFERENCES asset_definition(id),
    source_amount NUMERIC(36, 18) NOT NULL,
    target_amount NUMERIC(36, 18) NULL,
    state TEXT NOT NULL,
    current_step INT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL,
    external_refs JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk_flags JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_error_code TEXT NULL,
    last_error_message TEXT NULL,
    next_attempt_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE asset_bridge_outbox (
    id UUID PRIMARY KEY,
    bridge_job_id UUID NOT NULL REFERENCES asset_bridge_job(id),
    tenant_id UUID NOT NULL,
    adapter_channel_id UUID NOT NULL REFERENCES adapter_channel(id),
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',
    external_ref TEXT NULL,
    attempt_count INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NULL,
    last_error_code TEXT NULL,
    last_error_message TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (operation, idempotency_key)
);
```

`route_steps` 示例：

```json
[
  {
    "step": "BANK_TRANSFER",
    "channel_code": "airwallex_usd_sweep",
    "rail": "ACH",
    "destination": "exchange_usd_deposit_account"
  },
  {
    "step": "EXCHANGE_CONVERT",
    "channel_code": "binance_institutional",
    "symbol": "USDUSDT",
    "order_type": "LIMIT_SLICED",
    "max_slippage": "0.002"
  },
  {
    "step": "ONCHAIN_WITHDRAW",
    "channel_code": "binance_institutional",
    "asset": "USDT",
    "network": "TRON",
    "destination_wallet_policy": "cold_wallet_usdt_tron"
  }
]
```

### 2.7 `ASSET_EXCHANGE` 双轨复式分录

法币兑换为 USDT 示例：

```text
TxnType = ASSET_EXCHANGE

Entry 1:
  account = EXCHANGE_CLEARING:USD
  asset = USD
  direction = CREDIT
  amount = 10000.00

Entry 2:
  account = EXCHANGE_CLEARING:USDT
  asset = USDT
  direction = DEBIT
  amount = 9985.50

Entry 3:
  account = PLATFORM_FEE_OR_SLIPPAGE:USD
  asset = USD
  direction = DEBIT
  amount = 14.50 equivalent
```

要求：

- 单资产内部必须借贷平衡。
- 跨资产交易必须有 exchange rate、fee、slippage、provider reference。
- 不允许没有外部成交回执的 exchange 分录进入 finalized。
- 兑换失败、部分成交、未知状态必须进入 bridge job 状态机。

---

## 3. 给 Trae 的通用多模态重构搬砖指令

## 总红线

Trae 必须遵守：

1. 不准删除现有 `ChannelRegistry`、Anti-Carding、Dispute Ledger、mock/local 三态测试。
2. 不准把卡网逻辑硬塞进银行或 crypto adapter。
3. 不准在 DB transaction 内调用外部 HTTP/RPC。
4. 不准 silent mock。mock 只能在 `mock/local/test` 显式启用。
5. 不准保存 raw PAN、CVV、明文 API secret、私钥、助记词。
6. 不准对外部 unknown state 做成功假设。
7. 所有 adapter 必须有 contract tests。

验收命令：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/acquirer/... -count=1 -v
go test ./internal/ledger/... -count=1 -v
go test ./internal/checkout/... -count=1 -v
go test ./test/integration -run TestLocalDemo_FullFlow -count=1 -v
go test ./... -short -count=1
```

---

## Task 1.1：重构 ChannelRegistry 为 Universal Adapter Registry

### 涉及文件路径

预计新增：

```text
internal/adapter/domain/domain.go
internal/adapter/registry/registry.go
internal/adapter/registry/registry_test.go
internal/adapter/repository/repository.go
internal/adapter/provider/card.go
internal/adapter/provider/bank_collection.go
internal/adapter/provider/crypto_pay.go
internal/adapter/provider/exchange.go
internal/adapter/provider/onchain_wallet.go
```

预计修改：

```text
internal/acquirer/registry/registry.go
internal/acquirer/domain/domain.go
internal/checkout/acquirer_gateway.go
cmd/api/main.go
migrations/000020_init_universal_adapter_registry.up.sql
migrations/000020_init_universal_adapter_registry.down.sql
```

### 核心逻辑规范

新增通用接口：

```text
type AdapterType string

CARD_ACQUIRER
BANK_COLLECTION
CRYPTO_PAY
BANK_RAIL
EXCHANGE
LP
ONCHAIN_WALLET
```

通用 registry 需要支持：

```text
Get(channelID) AdapterBundle
GetByType(tenantID, adapterType) []AdapterBundle
LoadAll(ctx)
Reload(ctx, channelID)
Disable(ctx, channelID)
SubscribeConfigUpdates(ctx)
```

通用 bundle：

```text
AdapterBundle
  ChannelID
  TenantID
  AdapterType
  ProviderCode
  ChannelCode
  Client
  SupportedAssets
  SupportedNetworks
  Version
  LoadedAt
```

保留兼容：

- 现有 `internal/acquirer` 不要直接删除。
- Card acquirer 可以先作为 Universal Adapter 的一个 provider 实现。
- `TestLocalDemo_FullFlow` 必须继续通过。

### Contract Interface

```text
type InboundAdapter interface {
  CreateInboundIntent(ctx, req) (resp, error)
  QueryInboundIntent(ctx, req) (resp, error)
  VerifyInboundWebhook(ctx, req) (event, error)
}

type BridgeAdapter interface {
  CreateTransfer(ctx, req) (resp, error)
  QueryTransfer(ctx, req) (resp, error)
}

type ExchangeAdapter interface {
  GetOrderBook(ctx, req) (resp, error)
  PlaceLimitOrder(ctx, req) (resp, error)
  QueryOrder(ctx, req) (resp, error)
  Withdraw(ctx, req) (resp, error)
}
```

### Fail-Closed 边界

- adapter type 不支持：启动失败或 channel load 失败。
- credential 缺失：channel 不进入 active bundle。
- channel disabled：新 intent 不得使用。
- reload 失败：按配置 fail-closed 或 stale-but-serving。
- registry miss：不得 fallback 到 mock。

### 审计 checklist

Trae 必须回答：

- 是否保留原 acquirer mock/local demo？
- 是否新增通用 adapter type？
- 热路径是否仍然不访问 PG/KMS？
- 是否有 registry reload 测试？
- 是否有 unsupported adapter type 测试？
- 是否存在 silent mock fallback？

---

## Task 1.2：Bank Collection Adapter

### 涉及文件路径

预计新增：

```text
internal/adapter/provider/bank_collection.go
internal/adapter/provider/bank_collection_test.go
internal/bankcollection/domain/domain.go
internal/bankcollection/service/service.go
internal/bankcollection/repository/repository.go
migrations/000021_init_bank_collection.up.sql
migrations/000021_init_bank_collection.down.sql
```

### 核心逻辑规范

实现 `BANK_COLLECTION_ADAPTER`：

```text
CreateInboundIntent:
  input: tenant_id, merchant_id, order_id, amount, asset, country, buyer_ref
  output: virtual account / IBAN / routing details / payment reference / expires_at

QueryInboundIntent:
  query provider collection status

VerifyInboundWebhook:
  verify bank/neobank webhook signature
  map bank credit event to internal confirmed event
```

必须支持两种模式：

- Dynamic Virtual Account：每个订单动态分配一个虚拟户。
- Static Virtual Account + Unique Reference：复用虚拟户，但每单唯一 reference。

### 数据模型要求

```text
bank_collection_intent
  id
  tenant_id
  merchant_id
  order_id
  asset_id
  expected_amount
  assigned_account_ref
  payment_reference
  status
  expires_at
  provider_ref
  created_at
```

状态：

```text
CREATED
WAITING_FUNDS
PARTIALLY_MATCHED
CONFIRMED
RECONCILE_MISMATCH
EXPIRED
FAILED
```

### Fail-Closed 边界

- webhook 签名失败：拒绝，不入账。
- 金额不匹配：`RECONCILE_MISMATCH`。
- 币种不匹配：`RECONCILE_MISMATCH`。
- 付款 reference 缺失：不得自动确认。
- provider timeout：`UnknownExternalState`。
- 虚拟户池耗尽：拒绝创建 intent。

### 审计 checklist

Trae 必须回答：

- 是否有虚拟户分配测试？
- 是否有 webhook 验签失败不入账测试？
- 是否有金额不匹配测试？
- 是否有重复 bank webhook 幂等测试？
- 是否没有把 bank account 完整敏感信息写入日志？
- 是否没有在事务中调用银行 API？

---

## Task 1.3：Crypto Pay Adapter

### 涉及文件路径

预计新增：

```text
internal/adapter/provider/crypto_pay.go
internal/adapter/provider/crypto_pay_test.go
internal/cryptopay/domain/domain.go
internal/cryptopay/service/service.go
internal/cryptopay/repository/repository.go
internal/cryptopay/listener/listener.go
internal/cryptopay/listener/listener_test.go
migrations/000022_init_crypto_pay.up.sql
migrations/000022_init_crypto_pay.down.sql
```

### 核心逻辑规范

实现 `CRYPTO_PAY_ADAPTER`：

```text
CreateInboundIntent:
  input: tenant_id, merchant_id, order_id, amount, asset, network
  output: deposit_address, memo/tag optional, expires_at, confirmations_required

QueryInboundIntent:
  query chain/indexer status

VerifyInboundWebhook:
  verify gateway webhook or normalize listener event
```

充值监听：

- 支持 RPC/indexer 适配层。
- 支持 confirmations threshold。
- 支持 reorg 回滚保护。
- 支持地址池锁定和释放。
- 支持 memo/tag 校验。

### 数据模型要求

```text
crypto_deposit_intent
  id
  tenant_id
  merchant_id
  order_id
  asset_id
  network
  expected_amount
  deposit_address
  memo
  confirmations_required
  status
  tx_hash
  observed_amount
  created_at
```

状态：

```text
CREATED
WAITING_CHAIN_TX
CONFIRMING
CONFIRMED
AMOUNT_MISMATCH
NETWORK_MISMATCH
REORG_DETECTED
EXPIRED
FAILED
```

### Fail-Closed 边界

- 非白名单资产/网络：拒绝。
- 地址池为空：拒绝。
- confirmations 不足：不得确认。
- tx asset/network 不匹配：不得确认。
- memo/tag 不匹配：不得确认。
- reorg detected：回退到 confirming 或人工复核。
- RPC timeout：不确认，等待 poller。

### 审计 checklist

Trae 必须回答：

- 是否有地址分配幂等测试？
- 是否有 confirmations threshold 测试？
- 是否有 wrong network 测试？
- 是否有 amount mismatch 测试？
- 是否有 duplicate tx webhook/listener 测试？
- 是否没有保存私钥或助记词？

---

## Task 1.4：Multi-Asset Ledger 与 Universal Asset Bridge Engine

### 涉及文件路径

预计新增：

```text
internal/asset/domain/domain.go
internal/asset/repository/repository.go
internal/assetbridge/domain/domain.go
internal/assetbridge/service/service.go
internal/assetbridge/repository/repository.go
internal/assetbridge/worker/worker.go
internal/assetbridge/worker/worker_test.go
migrations/000023_init_multi_asset_ledger.up.sql
migrations/000023_init_multi_asset_ledger.down.sql
migrations/000024_init_asset_bridge.up.sql
migrations/000024_init_asset_bridge.down.sql
```

预计修改：

```text
internal/ledger/domain/domain.go
internal/ledger/service/service.go
internal/ledger/repository/*
internal/payout/service/service.go
internal/treasury/service/*
cmd/worker/main.go
```

### 核心逻辑规范

1. Multi-Asset Ledger

Ledger 必须支持：

- asset_id。
- asset decimals。
- fiat / crypto / stablecoin。
- `ASSET_EXCHANGE` transaction type。
- `ASSET_BRIDGE_IN_TRANSIT` account type。
- `EXCHANGE_CLEARING` account type。
- `ONCHAIN_CLEARING` account type。

2. Universal Asset Bridge Worker

根据 `asset_bridge_profile.route_steps` 执行：

```text
BANK_TRANSFER
EXCHANGE_DEPOSIT_POLL
EXCHANGE_CONVERT
ONCHAIN_WITHDRAW
ONCHAIN_WITHDRAW_POLL
LEDGER_FINALIZE
```

每个外部写动作必须创建 outbox：

- bank transfer create。
- exchange order place。
- exchange withdraw。

3. 20bps 滑点保护

Exchange convert 前必须：

- 拉 order book。
- 计算 VWAP。
- 比较 reference price。
- `estimated_slippage > 0.002` 时进入 `ApprovalRequired`。
- 不允许 market order 默认扫单。

4. 状态机

```text
CREATED
WAITING_SOURCE_CONFIRMATION
READY_TO_SWEEP
BANK_TRANSFER_SUBMITTED
BANK_TRANSFER_SETTLED
EXCHANGE_DEPOSIT_CONFIRMED
LIQUIDITY_CHECK
ORDER_SUBMITTED
PARTIALLY_FILLED
FILLED
WITHDRAWAL_SUBMITTED
WITHDRAWAL_CONFIRMED
LEDGER_FINALIZED

Exceptional:
UNKNOWN_EXTERNAL_STATE
APPROVAL_REQUIRED
RECONCILE_MISMATCH
FAILED_CLOSED
```

### Fail-Closed 边界

- merchant RiskHold：不得 sweep。
- dispute reserve 不足：不得 sweep。
- ledger imbalance：不得 sweep。
- bank transfer timeout：`UNKNOWN_EXTERNAL_STATE`。
- exchange timeout：`UNKNOWN_EXTERNAL_STATE`。
- slippage 超 20bps：`APPROVAL_REQUIRED`。
- withdrawal address 非白名单：拒绝。
- chain tx 未确认：不得 finalize。

### Contract Interface

```text
type AssetBridgeStep interface {
  Prepare(ctx, job) error
  Enqueue(ctx, job) error
  Reconcile(ctx, job) error
  Compensate(ctx, job) error
}
```

每个 step 必须可重入：

- 同一个 job 重跑不重复转账。
- 同一个 idempotency key 不重复下单。
- 同一个 tx hash 不重复入账。

### 审计 checklist

Trae 必须回答：

- Ledger 是否支持 asset_id？
- ASSET_EXCHANGE 是否有双轨分录测试？
- bridge worker 是否每个外部写动作都走 outbox？
- 是否有 slippage > 20bps 测试？
- 是否有 UnknownExternalState 测试？
- 是否有 RiskHold 阻断 sweep 测试？
- 是否有重复 job 不重复外部请求测试？

---

## 4. Trae 最终报告模板

```text
## Multi-Modal Refactor 完成报告

### 1. 修改文件列表
- 新增：
- 修改：
- 删除：

### 2. 通用适配器完成情况
- Universal Adapter Registry：
- CARD_ACQUIRER 兼容：
- BANK_COLLECTION：
- CRYPTO_PAY：
- EXCHANGE / BRIDGE 是否只是接口还是已实现：

### 3. 账本完成情况
- asset_definition：
- ledger asset_id：
- ASSET_EXCHANGE：
- 双轨平衡测试：

### 4. Asset Bridge 完成情况
- bridge profile：
- bridge job：
- outbox：
- slippage guard：
- UnknownExternalState：

### 5. 核心防线是否保留
- ChannelRegistry/mock local demo：
- Anti-Carding：
- Dispute Ledger：
- real mode raw PAN/CVV：

### 6. 测试结果
粘贴完整命令输出。

### 7. 未完成项
必须如实列出，不得写“无”除非所有 task 和测试全部满足。
```

---

## 5. 当前执行判断

本轮不是要删除既有卡网链路，而是把卡网链路降级为通用入金 adapter 的一种实现。

正确演进：

```text
Acquirer ChannelRegistry
  -> Universal Adapter Registry
  -> CARD_ACQUIRER / BANK_COLLECTION / CRYPTO_PAY / BANK_RAIL / EXCHANGE / ONCHAIN_WALLET
```

错误演进：

```text
删除现有 acquirer
删除 mock/local
把银行/crypto 写成 card if-else
在事务里直接打外部 API
用 mock success 冒充真实 adapter
```

Phase 1 的完成标准是：通用 adapter 抽象、通用凭证 vault、多资产 ledger、bank collection intent、crypto deposit intent、asset bridge job/outbox 的骨架和 contract tests 全部落地，同时现有 mock/local demo 继续 100% 通过。
