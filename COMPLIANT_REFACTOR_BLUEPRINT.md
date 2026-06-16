# 多态通道适配、自动化财资与多租户解耦工程重构蓝图

> 版本：V1.0
>
> 适用范围：NexusGlobal MAMR-System 下一阶段重构规划。
>
> 重要边界：本蓝图仅面向合法、透明、可审计的多租户收单、清算、财资调度和合规出金系统。不得用于无履约、不对版收单、规避收单机构/银行/LP/监管审查、隐藏交易实质、误导消费者或规避 AML/KYC/KYB/制裁筛查。

---

## 0. 背景与目标

当前系统已经完成以下基础能力：

- 双轨账本；
- 本地事务隔离；
- Payout 冻结与 Outbox；
- Webhook Receiver；
- MAE 状态机；
- Sweep / Reconcile；
- mock / 本地演示闭环。

下一阶段目标是将系统升级为一套合规的多租户支付与财资中台：

1. 后台可动态配置多个收单通道和银行/LP/交易所连接器；
2. 前线站点通过标准 SDK 接入，而不需要关心具体收单通道；
3. 财资归集、换汇、结算、出金、冷钱包归集全部异步化、可审计、可暂停、可人工复核；
4. 任何外部资金动作都具备幂等、状态机、对账、熔断、限额、人工复核和异常兜底；
5. 多租户之间配置、资金、密钥、账本、Webhook 和限额隔离。

---

## 1. 合规边界与禁止事项

### 1.1 禁止目标

系统不得支持以下能力：

- 无履约或不对版收单；
- 使用前端混淆脚本规避银行、卡组、收单机构或风控识别；
- 隐藏真实商户、商品、服务、发货、退款、账单描述或最终受益人；
- 通过频繁切通道绕过额度、拒付率、争议率、KYC/KYB 或 AML 限制；
- 自动将资金快速转入交易所或冷钱包以规避审查、冻结、追回或对账；
- 为高风险站群提供“无限建站、后方隐藏通道”的规避型能力。

### 1.2 合规替代表述

本阶段建设目标应定义为：

```text
合法多租户商户
  -> 标准站点 SDK
  -> 透明订单与履约信息
  -> 合规收单路由
  -> 双轨账本
  -> 银行/LP/交易所合规财资调度
  -> 审计、风控、对账、人工复核
```

---

## 2. 总体架构

```text
Tenant / Merchant
  -> Site Integration SDK
  -> Public Checkout API
  -> Tenant Auth / App Credential Middleware
  -> Payment Orchestration Service
  -> Dynamic Acquirer Factory
  -> Acquirer Adapter: Stripe / Nxtapay / Other
  -> Webhook Receiver
  -> Ledger
  -> Treasury Position Engine
  -> Bank Sweep Worker
  -> Exchange / LP Funding Worker
  -> FX / Conversion Worker
  -> Withdrawal Worker
  -> Cold Wallet / Settlement Account
  -> Reconciliation + Manual Review
```

核心原则：

- 通道配置动态化，但通道启用必须经过后台审批和健康检查；
- SDK 标准化，但不得隐藏交易实质；
- 财资自动化，但每一步资金外呼都必须通过 Outbox / Worker / Saga；
- 真实资金移动必须可暂停、可回滚到人工复核、可对账；
- 每个租户都有独立配置、限额、密钥、审计和风险评分。

---

## 3. 动态通道驱动架构

### 3.1 设计目标

支持后台添加、更新、禁用收单通道，无需重启 API / Worker。

支持异构通道：

- Stripe；
- Nxtapay；
- Airwallex；
- 本地银行 APM；
- 持牌 LP；
- 后续其他收单网关。

### 3.2 表结构规划

新增或重构以下表。

#### `payment_provider`

```sql
id uuid primary key
provider_code varchar(64) unique not null
provider_name varchar(128) not null
provider_type varchar(32) not null
status varchar(32) not null
capabilities jsonb not null default '{}'
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

示例 `capabilities`：

```json
{
  "supports_card": true,
  "supports_apm": true,
  "supports_refund": true,
  "supports_capture": true,
  "supports_void": true,
  "currencies": ["USD", "EUR", "GBP"],
  "settlement_currencies": ["USD", "EUR"]
}
```

#### `tenant_acquirer_channel`

```sql
id uuid primary key
tenant_id uuid not null
merchant_id uuid not null
provider_id uuid not null references payment_provider(id)
channel_code varchar(64) not null
display_name varchar(128) not null
status varchar(32) not null
routing_weight integer not null default 100
priority integer not null default 100
jurisdiction varchar(16)
supported_currencies jsonb not null default '[]'
supported_payment_methods jsonb not null default '[]'
descriptor_policy jsonb not null default '{}'
risk_policy jsonb not null default '{}'
limit_policy jsonb not null default '{}'
provider_config jsonb not null default '{}'
credential_ref uuid not null
version integer not null default 1
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
unique (tenant_id, channel_code)
```

#### `provider_credential_vault`

```sql
id uuid primary key
tenant_id uuid not null
provider_id uuid not null
credential_type varchar(32) not null
key_version integer not null default 1
ciphertext bytea not null
metadata jsonb not null default '{}'
status varchar(32) not null
created_at timestamptz not null default now()
rotated_at timestamptz
```

要求：

- `provider_config` 存非敏感参数；
- API key、secret、private key、webhook secret 必须进入 `provider_credential_vault`；
- 所有 ciphertext 通过 Envelope Encryption；
- 禁止在 JSONB 中存明文密钥。

### 3.3 Provider Config JSONB 示例

Stripe：

```json
{
  "api_version": "2024-06-20",
  "capture_method": "automatic",
  "statement_descriptor_mode": "configured",
  "webhook_tolerance_seconds": 300,
  "retry_policy": {
    "max_attempts": 0,
    "timeout_is_unknown": true
  }
}
```

Nxtapay：

```json
{
  "api_base_url": "https://api.example-pay.com",
  "merchant_account_id": "acct_xxx",
  "settlement_mode": "T_PLUS_1",
  "webhook_signature_header": "X-Signature",
  "supported_methods": ["card", "bank_transfer"],
  "retry_policy": {
    "max_attempts": 0,
    "timeout_is_unknown": true
  }
}
```

### 3.4 Go 工厂模式

接口：

```text
AcquirerClient
  - Authorize(ctx, request)
  - Capture(ctx, request)
  - Refund(ctx, request)
  - QueryPayment(ctx, provider_ref)
  - VerifyWebhook(ctx, payload, headers)
```

动态实例化流程：

```text
1. Channel Registry 定时加载 Active channel
2. 解密 credential_ref 对应密钥
3. 校验 provider_config schema
4. 根据 provider_code 选择 AdapterFactory
5. 创建 client 并放入内存 registry
6. version 变化时热更新 client
7. status Disabled / Suspended 时从 registry 摘除
```

重要约束：

- 热更新只更新内存 client，不改动进行中的事务；
- 旧 client 可等待 inflight 请求自然完成；
- 通道配置变更必须写 audit log；
- 通道配置错误时 fail-closed，不可回退到默认通道。

### 3.5 Node.js SDK / Adapter 可选边界

若部分通道只有 Node SDK，可通过独立 adapter worker 包装：

```text
Go Core
  -> Outbox
  -> Node Adapter Worker
  -> Provider SDK
  -> Result Event
  -> Go Core Consumer
```

限制：

- Node worker 不持有账本权限；
- 只负责外呼 provider；
- 结果必须回写 Outbox / Event Stream；
- 超时进入 UnknownExternalState；
- 禁止在 Node worker 内直接改余额。

---

## 4. 前线站点极简接入 SDK

### 4.1 设计目标

让 WordPress、Shopify、自建站可以快速接入统一 checkout。

但 SDK 必须透明展示订单、商户、金额、退款政策和账单描述，不得用于隐藏真实交易。

### 4.2 三参数接入

站点侧配置：

```text
Gateway_URL
App_ID
App_Secret
```

后端校验：

```text
1. App_ID 定位 tenant_site_application
2. App_Secret 只用于服务端签名，不出现在浏览器
3. 前端 JS 只能拿 public_app_id
4. 后端订单创建必须由商户服务器签名
5. API Gateway 校验签名、时间戳、nonce、payload_hash
```

### 4.3 表结构

#### `tenant_site_application`

```sql
id uuid primary key
tenant_id uuid not null
merchant_id uuid not null
app_id varchar(64) unique not null
app_secret_ciphertext bytea not null
site_domain varchar(255) not null
allowed_origins jsonb not null default '[]'
webhook_url text
status varchar(32) not null
risk_profile_id uuid
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

#### `site_order_intent`

```sql
id uuid primary key
tenant_id uuid not null
merchant_id uuid not null
app_id varchar(64) not null
merchant_order_id varchar(128) not null
amount numeric(20,4) not null
currency varchar(8) not null
product_summary text not null
customer_email_hash varchar(128)
success_url text
cancel_url text
metadata jsonb not null default '{}'
status varchar(32) not null
idempotency_key varchar(128) not null
payload_hash varchar(128) not null
created_at timestamptz not null default now()
unique (tenant_id, app_id, merchant_order_id)
unique (idempotency_key)
```

### 4.4 API 边界

#### 创建订单意图

```text
POST /public/v1/order-intents
Headers:
  X-App-ID
  X-Timestamp
  X-Nonce
  X-Signature
  Idempotency-Key

Body:
  merchant_order_id
  amount
  currency
  product_summary
  success_url
  cancel_url
  metadata
```

返回：

```json
{
  "intent_id": "...",
  "checkout_token": "...",
  "checkout_url": "https://gateway.example.com/checkout?token=..."
}
```

### 4.5 前端 JS SDK

允许：

- 加载 checkout；
- 创建支付 iframe 或跳转；
- 显示商户、金额、币种、退款政策；
- 收集非敏感交互事件用于风控。

禁止：

- 混淆真实商户或商品；
- 隐藏订单信息；
- 规避风控；
- 将 `App_Secret` 放入浏览器；
- 收集或存储完整卡号/CVV。

---

## 5. 自动化财资调度与 Fiat-to-Crypto 合规闭环

### 5.1 目标

在银行资金归集完成后，将部分或全部资金合规调度到：

- 持牌 LP；
- 企业交易所账户；
- 托管钱包；
- 冷钱包。

用途：

- 多币种库存管理；
- 稳定币结算；
- 汇率风险对冲；
- 跨区域资金归集。

### 5.2 禁止目标

不得用于：

- 绕过银行、LP、交易所审查；
- 快速转移争议资金；
- 隐藏资金来源；
- 绕过冻结、追回或监管查询；
- 将未完成履约或高争议资金自动转入不可逆链上地址。

### 5.3 资金状态机

建议新增 `treasury_cashout_request`：

```text
Draft
  -> RiskChecking
  -> ApprovalRequired
  -> BankTransferLocked
  -> BankTransferSubmitted
  -> BankTransferConfirmed
  -> ExchangeFundingSubmitted
  -> ExchangeFundingConfirmed
  -> ConversionQuoted
  -> ConversionExecuted
  -> WithdrawalSubmitted
  -> WithdrawalConfirmed
  -> Reconciled
```

异常状态：

```text
Rejected
Failed
UnknownExternalState
ManualReview
Suspended
```

### 5.4 表结构

#### `treasury_cashout_policy`

```sql
id uuid primary key
tenant_id uuid not null
source_account_id uuid not null
target_type varchar(32) not null
target_account_ref uuid not null
source_currency varchar(8) not null
target_asset varchar(16) not null
min_amount numeric(20,4) not null
max_amount numeric(20,4) not null
daily_limit numeric(20,4) not null
slippage_bps integer not null
requires_approval boolean not null default true
risk_policy jsonb not null default '{}'
schedule_policy jsonb not null default '{}'
status varchar(32) not null
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

#### `treasury_cashout_request`

```sql
id uuid primary key
tenant_id uuid not null
policy_id uuid not null
source_account_id uuid not null
source_currency varchar(8) not null
source_amount numeric(20,4) not null
target_asset varchar(16) not null
expected_target_amount numeric(28,8)
actual_target_amount numeric(28,8)
status varchar(32) not null
idempotency_key varchar(128) not null unique
risk_score integer
manual_review boolean not null default false
manual_review_reason text
version integer not null default 1
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

#### `treasury_cashout_step`

```sql
id uuid primary key
cashout_request_id uuid not null
step_type varchar(64) not null
provider_type varchar(32) not null
provider_ref varchar(128)
status varchar(32) not null
request_payload_hash varchar(128)
response_payload_ciphertext bytea
error_code varchar(64)
error_message text
attempt_count integer not null default 0
next_retry_at timestamptz
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

### 5.5 Worker 流程

```text
1. Cashout Planner
   - 读取 cash position
   - 应用 policy
   - 检查争议率、退款率、未履约订单、reserve、rolling reserve
   - 生成 cashout_request

2. Risk Checker
   - AML / sanctions / velocity / dispute risk
   - 高风险进入 ApprovalRequired 或 ManualReview

3. Bank Transfer Worker
   - 锁定源资金
   - 通过 Outbox 调银行 API
   - timeout -> UnknownExternalState
   - confirmed -> 下一步

4. Exchange Funding Worker
   - 查询银行到账
   - 提交交易所入金确认
   - 等待交易所到账 webhook / query

5. FX / Conversion Worker
   - 拉取报价
   - 校验 slippage_bps
   - 超过滑点 -> ManualReview
   - 成交后写 conversion step

6. Withdrawal Worker
   - 校验提现限额、地址白名单、Travel Rule
   - 提交提现
   - timeout -> UnknownExternalState
   - confirmed -> Reconciled

7. Reconcile Worker
   - 查询 Bank / Exchange / LP 最终态
   - 推进 Confirmed / Failed / ManualReview
```

### 5.6 异常兜底

| 异常 | 处理 |
| --- | --- |
| 银行 API timeout | 进入 UnknownExternalState，不盲重试资金提交，只查询 |
| 银行返回失败 | 释放锁定资金，写失败分录 |
| 银行已扣款但交易所未到账 | ManualReview + 查询银行凭证 + 查询交易所入金 |
| 交易所报价滑点过大 | 不成交，进入 ManualReview |
| 交易所成交部分成功 | 记录 actual amount，进入差额对账 |
| 提币超限 | 等待限额窗口或人工审批 |
| 提币地址未白名单 | 拒绝执行 |
| 链上长时间未确认 | UnknownExternalState，持续查询 tx hash |
| 对账差异 | 创建 reconciliation adjustment，不允许手改余额 |

### 5.7 风控对冲

必须支持：

- 每租户每日 cashout limit；
- 每通道每日 cashout limit；
- 未履约订单资金不可自动 cashout；
- dispute / refund spike 自动暂停 cashout；
- high-risk country / product category 自动人工复核；
- reserve 资金不可被 cashout；
- 冷钱包地址白名单；
- 多签审批；
- Travel Rule 数据留存；
- 完整审计链。

---

## 6. 多租户解耦

### 6.1 租户隔离维度

每个 tenant 必须隔离：

- app credentials；
- acquirer channels；
- provider credentials；
- webhook secrets；
- ledger accounts；
- treasury policies；
- risk policies；
- admin users；
- audit logs。

### 6.2 租户上下文

所有入口必须解析：

```text
tenant_id
merchant_id
app_id 或 internal principal
request_id
idempotency_key
payload_hash
```

所有写操作必须带：

```text
tenant_id
idempotency_key
payload_hash
audit_actor
```

---

## 7. 对外 API 重构边界

### 7.1 Public Site API

```text
POST /public/v1/order-intents
GET  /public/v1/order-intents/{id}
POST /public/v1/checkout-tokens
```

### 7.2 Admin API

```text
POST  /admin/providers
POST  /admin/channels
PATCH /admin/channels/{id}
POST  /admin/channels/{id}/rotate-secret
POST  /admin/channels/{id}/enable
POST  /admin/channels/{id}/disable
GET   /admin/channels
GET   /admin/channels/{id}
```

### 7.3 Treasury API

```text
POST /admin/treasury/cashout-policies
POST /admin/treasury/cashout-requests
POST /admin/treasury/cashout-requests/{id}/approve
POST /admin/treasury/cashout-requests/{id}/pause
POST /admin/treasury/cashout-requests/{id}/resume
GET  /admin/treasury/cashout-requests/{id}
```

### 7.4 Internal Worker API

```text
POST /internal/cashout/{id}/bank-submit
POST /internal/cashout/{id}/exchange-funding-check
POST /internal/cashout/{id}/convert
POST /internal/cashout/{id}/withdraw
POST /internal/cashout/{id}/reconcile
```

---

## 8. 迁移步骤

### Phase 1：数据模型与配置中心

- 新增 provider / channel / credential 表；
- 新增 site application / order intent 表；
- 新增 cashout policy / request / step 表；
- 增加 tenant_id 到关键表；
- 增加 audit log。

### Phase 2：动态通道工厂

- 抽象 AcquirerClient；
- 实现 Stripe adapter；
- 实现 Nxtapay adapter；
- 实现 mock adapter；
- 实现 Channel Registry 热加载；
- 实现配置 schema 校验。

### Phase 3：站点 SDK 与 Public API

- 实现 `/public/v1/order-intents`；
- 实现签名中间件；
- 实现 JS SDK；
- 实现 WordPress / Shopify 示例插件；
- 加 CORS / CSP / allowed origin。

### Phase 4：自动化财资 Cashout

- 实现 planner；
- 实现 bank transfer worker；
- 实现 exchange funding worker；
- 实现 conversion worker；
- 实现 withdrawal worker；
- 实现 reconcile worker；
- 实现 manual review console。

### Phase 5：风控、审计与上线

- 接 AML / sanctions；
- 接 Travel Rule 数据；
- 加 velocity rules；
- 加 dispute / refund spike 暂停策略；
- 跑 staging；
- 跑 k6；
- 灰度上线。

---

## 9. mimo 执行提示词

```text
请基于 COMPLIANT_REFACTOR_BLUEPRINT.md 执行下一阶段重构规划。

重要边界：
1. 只实现合法、透明、可审计的多租户支付与财资调度能力。
2. 不实现无履约、不对版收单、规避风控、隐藏真实交易或快速逃避审查的能力。
3. 所有外部资金动作必须走 Outbox / Worker / Saga。
4. DB 事务内禁止 LP / Bank / Exchange / MAE 外部网络 I/O。
5. 敏感密钥必须加密，不得存在 JSONB 明文中。

第一阶段只做规划落地准备，不写大规模业务代码：
- 输出 migration 设计草案；
- 输出 Go interface 设计；
- 输出 API 边界；
- 输出分阶段任务清单；
- 标出会影响现有代码的模块和风险点。

完成后回复：
1. 新增/修改的文件；
2. 表结构方案；
3. 接口边界；
4. 分阶段开发任务；
5. 风险与回滚策略。
```

