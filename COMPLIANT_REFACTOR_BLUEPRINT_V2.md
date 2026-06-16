# NexusGlobal 多通道风控、自动化财资与逆向账本 V2.0 合规生产加固蓝图

> 版本：V2.0
>
> 本文件替代 `COMPLIANT_REFACTOR_BLUEPRINT.md` 中偏概念化的部分，聚焦下一轮可落地工程加固。
>
> 合规边界：本蓝图只支持合法、透明、有履约、有审计、有 KYC/KYB/AML/制裁筛查的多租户支付与财资系统。不得用于无履约、不对版收单、隐藏真实交易、规避收单机构/卡组织/银行/LP/交易所/监管审查，或将争议资金快速转移到不可逆链上地址。

---

## 0. V1.0 工程短板复盘与 V2.0 修正方向

### 0.1 动态通道工厂性能风险

V1.0 提出动态读取通道 JSONB 配置并实例化通道 client，但没有明确高并发交易路径的缓存边界。

风险：

- 每笔交易查 PG；
- 每笔交易解密密钥；
- 每笔交易创建 provider SDK client；
- HTTP 连接池无法复用；
- PG 连接池、KMS、CPU、GC 都会被放大打爆。

V2.0 修正：

- 引入 `Channel Registry Object Pool`；
- 交易热路径只读本地内存；
- 配置变更通过 Redis Pub/Sub 或版本轮询触发热重载；
- client 单例复用 HTTP transport / connection pool；
- 配置错误 fail-closed，不静默回退默认通道。

### 0.2 缺少前置 Carding / Bot 拦截

V1.0 将风控放在支付编排后段，没有明确在 `/public/v1/order-intents` 前做统一测卡与异常流量拦截。

风险：

- bot / carding 流量直接透传给后方收单通道；
- decline rate 快速拉高；
- provider 风险评分恶化；
- 正常商户也被同池流量牵连。

V2.0 修正：

- 在 Public API 最前置加入 Redis 滑动窗口风控中间件；
- 按 IP、App_ID、BIN、设备指纹、租户、商户多维计数；
- 命中阈值后直接拒绝，不再透传 provider；
- 拒绝必须返回真实风控错误，不使用误导性“mock 成功”。

### 0.3 自动换汇缺少滑点与流动性保护

V1.0 对自动化换汇只提出 worker 流程，没有明确 order book、滑点、分批、暂停和人工复核机制。

风险：

- 大额市价单吃穿盘口；
- 汇率剧烈波动时本金被异常磨损；
- 交易所限额、风控、入金延迟导致状态卡死。

V2.0 修正：

- Conversion 前必须读取 order book 或 quote；
- 引入 `max_allowed_slippage_bps`，默认 20 bps，即 0.2%；
- 支持分批限价单 / TWAP / iceberg；
- 超滑点进入 `ApprovalRequired`；
- timeout 进入 `UnknownExternalState`，只查最终态，不盲目重试资金动作。

### 0.4 逆向账务与风险准备金不足

V1.0 重点覆盖正向交易、冻结、代付和对账，但对退款、拒付、拒付反转、罚金、平台准备金隔离描述不足。

风险：

- 外部银行/通道已扣款，内部账本未冲正；
- 商户 settled 余额虚高；
- 平台风险准备金不足；
- 账实不符。

V2.0 修正：

- 新增 Dispute Ledger / Reserve Ledger；
- 新增 refund / chargeback / reversal 状态机；
- 所有逆向资金动作必须双轨入账；
- 外部扣款、罚金、商户余额、平台准备金同步核销；
- 商户负余额、保证金不足进入追缴和人工复核。

---

## 1. 动态通道客户端单例线程安全对象池

### 1.1 目标

高并发交易热路径满足：

```text
零 PG 查询
零 KMS 解密
零 SDK client 创建
零密钥明文日志
只读内存 registry
```

### 1.2 核心组件

```text
ChannelConfigStore
  -> 负责从 PG 读取 provider/channel 配置

CredentialDecryptor
  -> 负责启动/热重载时解密 credential_ref

ChannelClientFactory
  -> 根据 provider_code 创建 Stripe/Nxtapay/Airwallex/mock client

ChannelRegistryObjectPool
  -> 线程安全缓存 channel_id -> ClientBundle

ChannelConfigReloader
  -> 订阅 Redis Pub/Sub 热重载事件
```

### 1.3 ClientBundle

```text
ClientBundle
  channel_id
  tenant_id
  provider_code
  config_version
  status
  client
  capabilities
  risk_policy
  limit_policy
  loaded_at
  last_health_check_at
```

### 1.4 缓存结构

Go 实现建议：

```text
type ChannelRegistry struct {
  mu sync.RWMutex
  byChannelID map[uuid.UUID]*ClientBundle
  byTenantProvider map[string]*ClientBundle
  version map[uuid.UUID]int64
}
```

或：

```text
sync.Map[channel_id]ClientBundle
```

选择建议：

- 通道数量较小、重载频率低：`RWMutex + map`；
- 通道数量大、读非常多、写较少：`atomic.Value` 保存不可变 snapshot；
- 不建议交易热路径使用会淘汰活跃 client 的普通 LRU。

### 1.5 热重载状态机

```text
Stable
  -> ReloadRequested
  -> LoadingConfig
  -> DecryptingCredential
  -> BuildingClient
  -> HealthChecking
  -> Active
```

失败状态：

```text
ReloadFailed
Disabled
Suspended
StaleButServing
```

处理规则：

- 后台更新 channel 后写 DB 并发布 Redis Pub/Sub：

```text
channel.config.updated:{channel_id}:{version}
```

- API / Worker 收到事件后异步 reload；
- reload 成功后原子替换 bundle；
- reload 失败时：
  - 若旧 bundle 仍有效，可进入 `StaleButServing` 并报警；
  - 若密钥失效或状态为 Disabled，必须 fail-closed；
  - 不允许回退到其他通道。

### 1.6 交易热路径

```text
1. Router 选出 channel_id
2. registry.Get(channel_id)
3. 校验 bundle.status == Active
4. 校验 bundle.version >= route_required_version
5. 调用 bundle.client
```

禁止：

- 在交易请求内查 provider credential；
- 在交易请求内解密密钥；
- 在交易请求内创建 SDK client；
- 在 provider 调用失败时自动切换到未授权通道重复扣款。

---

## 2. Redis 滑动时间窗口统一测卡拦截网关

### 2.1 目标

在进入支付编排前，拦截 carding、bot、撞库、异常 decline 风险。

接入位置：

```text
Public Edge
  -> Request Auth
  -> AntiCarding Middleware
  -> Order Intent
  -> Routing
  -> Acquirer
```

### 2.2 维度

必须计数：

- buyer_ip；
- app_id；
- tenant_id；
- merchant_id；
- card_bin，前 6 或 8 位；
- device_fingerprint；
- email_hash；
- billing_country；
- user_agent_hash。

注意：

- 不存完整卡号；
- 不存 CVV；
- BIN 只用于风控统计；
- 敏感标识应 hash 或 token 化。

### 2.3 Redis Key

```text
risk:carding:ip:{ip_hash}:{window}
risk:carding:app:{app_id}:{window}
risk:carding:bin:{bin_hash}:{window}
risk:carding:tenant:{tenant_id}:{window}
risk:carding:device:{device_hash}:{window}
```

窗口：

```text
60s
5m
1h
24h
```

### 2.4 计数事件

```text
order_intent_created
payment_attempted
provider_declined
provider_timeout
issuer_declined
cvv_failed
avs_failed
three_ds_failed
velocity_rejected
```

### 2.5 阈值策略

示例：

```json
{
  "ip_60s_attempts": 10,
  "ip_5m_declines": 5,
  "app_60s_decline_rate_bps": 3000,
  "bin_5m_distinct_cards": 20,
  "device_1h_attempts": 15,
  "tenant_1h_decline_rate_bps": 2000
}
```

### 2.6 拦截行为

命中阈值：

```text
1. 不调用后方 acquirer
2. 写 risk_event
3. 写 payment_intent.status = RiskRejected
4. 返回 429 或 403 风控错误
5. 可触发 app/channel/tenant 临时降权或熔断
```

禁止：

- 返回假成功；
- 使用误导性 mock 成功掩盖拒绝；
- 继续透传 provider “试试看”。

### 2.7 Lua 原子脚本

所有多维计数应通过 Lua pipeline 原子完成：

```text
INCR key
EXPIRE key ttl
GET key
返回所有维度计数
```

再由 Go 风控策略判断是否拒绝。

---

## 3. 法币自动上链对冲引擎：断点续传与滑点控制

### 3.1 合规目标

将企业自有、已结算、已过风控窗口的资金，在合规前提下自动调度到：

- 持牌 LP；
- 企业交易所账户；
- 托管钱包；
- 冷钱包。

### 3.2 不可动用资金

以下资金不得自动上链：

- pending balance；
- reserve balance；
- dispute hold；
- frozen payout balance；
- 未履约订单资金；
- 高退款/高拒付商户资金；
- manual_review 资金；
- 被监管、银行、LP 或内部风控 hold 的资金。

### 3.3 状态机

```text
Planned
  -> RiskChecking
  -> ApprovalRequired
  -> BankTransferLocked
  -> BankTransferSubmitted
  -> BankTransferConfirmed
  -> ExchangeFundingPending
  -> ExchangeFundingConfirmed
  -> QuoteRequested
  -> QuoteAccepted
  -> ConversionSubmitted
  -> ConversionFilled
  -> WithdrawalSubmitted
  -> WithdrawalConfirmed
  -> Reconciled
```

异常：

```text
Rejected
UnknownExternalState
PartiallyFilled
SlippageExceeded
LimitExceeded
ManualReview
Failed
```

### 3.4 Conversion Worker 步骤

```text
1. 加载 cashout_request
2. 校验状态为 ExchangeFundingConfirmed
3. 校验可换汇金额和限额
4. 查询交易所 order book 或 RFQ quote
5. 估算成交均价与滑点
6. 若 slippage > max_allowed_slippage_bps:
     status = ApprovalRequired
     reason = SlippageExceeded
     停止
7. 选择执行方式:
     小额：limit order
     中额：分批 limit order
     大额：TWAP / iceberg / RFQ
8. 提交订单
9. timeout -> UnknownExternalState
10. filled -> ConversionFilled
11. partial -> PartiallyFilled + 等待或人工复核
```

默认保护：

```text
max_allowed_slippage_bps = 20
即最大 0.2%
```

### 3.5 Order Book 检查

必须计算：

```text
expected_avg_price
available_depth_at_price
estimated_slippage_bps
min_received_amount
```

示例判断：

```text
if estimated_slippage_bps > policy.max_allowed_slippage_bps:
    move to ApprovalRequired
```

### 3.6 断点续传

每一步写入 `treasury_cashout_step`：

```text
step_type
provider_ref
request_payload_hash
status
attempt_count
next_retry_at
external_finality
```

恢复逻辑：

- 有 provider_ref：只查最终态；
- 无 provider_ref：可重新提交，但必须使用相同 idempotency_key；
- timeout 不允许盲目重提资金动作；
- partial fill 必须按实际成交金额入账。

---

## 4. 逆向账本双轨分录与风险准备金

### 4.1 新增账户类型

```text
MERCHANT_SETTLED
MERCHANT_PENDING
MERCHANT_RESERVE
MERCHANT_DISPUTE_HOLD
PLATFORM_DISPUTE_RESERVE
PLATFORM_CHARGEBACK_FEE
ACQUIRER_RECEIVABLE
BANK_CLEARING
```

### 4.2 新增状态机

Refund：

```text
RefundRequested
  -> RefundApproved
  -> RefundSubmitted
  -> RefundConfirmed
  -> RefundFailed
  -> UnknownExternalState
```

Chargeback：

```text
ChargebackReceived
  -> EvidenceRequired
  -> RepresentmentSubmitted
  -> ChargebackWon
  -> ChargebackLost
  -> ChargebackReversed
  -> ManualReview
```

### 4.3 表结构

#### `payment_dispute`

```sql
id uuid primary key
tenant_id uuid not null
merchant_id uuid not null
payment_intent_id uuid
provider_ref varchar(128)
dispute_type varchar(32) not null
amount numeric(20,4) not null
currency varchar(8) not null
fee_amount numeric(20,4) not null default 0
status varchar(32) not null
reason_code varchar(64)
evidence_due_at timestamptz
idempotency_key varchar(128) not null unique
payload_hash varchar(128) not null
created_at timestamptz not null default now()
updated_at timestamptz not null default now()
```

#### `platform_dispute_reserve_ledger`

```sql
id uuid primary key
tenant_id uuid not null
merchant_id uuid not null
currency varchar(8) not null
reserve_balance numeric(20,4) not null default 0
dispute_hold_balance numeric(20,4) not null default 0
fee_hold_balance numeric(20,4) not null default 0
version integer not null default 1
updated_at timestamptz not null default now()
unique (tenant_id, merchant_id, currency)
```

### 4.4 双轨分录

#### ORDER_REFUND

退款确认时：

```text
Dr Merchant Settled Account
Cr Acquirer Receivable / Bank Clearing Account
```

同时更新：

```text
merchant_treasury_ledger.settled_balance -= refund_amount
```

#### CHARGEBACK_RECEIVED

拒付收到时：

```text
Dr Merchant Settled Account
Cr Platform Dispute Reserve Account
```

拒付罚金：

```text
Dr Merchant Settled Account
Cr Platform Chargeback Fee Account
```

同时更新：

```text
merchant_treasury_ledger.settled_balance -= dispute_amount + fee_amount
platform_dispute_reserve_ledger.dispute_hold_balance += dispute_amount
platform_dispute_reserve_ledger.fee_hold_balance += fee_amount
```

如果商户 settled 不足：

```text
1. 扣到 0
2. 差额记 merchant_negative_balance
3. merchant status -> RiskHold
4. 后续入账优先抵扣
```

#### CHARGEBACK_REVERSED

拒付赢回或反转：

```text
Dr Platform Dispute Reserve Account
Cr Merchant Settled Account
```

同时更新：

```text
platform_dispute_reserve_ledger.dispute_hold_balance -= amount
merchant_treasury_ledger.settled_balance += amount
```

### 4.5 原子事务

拒付入账必须在一个 DB 本地事务内完成：

```text
1. idempotency check
2. SELECT merchant_treasury_ledger FOR UPDATE
3. SELECT platform_dispute_reserve_ledger FOR UPDATE
4. insert payment_dispute
5. insert ledger_transaction
6. insert ledger_entry x2 or x4
7. update balances
8. commit
```

禁止：

- 直接手改余额；
- 只改状态不入账；
- webhook 里直接改账；
- 外部 API timeout 当失败处理。

---

## 5. 给 Mimo 的原子化开发指令

### 任务包 A：Channel Registry Object Pool

```text
目标：实现动态通道客户端单例对象池，消除交易热路径查库/解密/创建 client。

涉及文件：
- internal/acquirer/registry/
- internal/acquirer/factory/
- internal/acquirer/adapter/
- internal/platform/config/
- migrations/

要求：
1. 新增 payment_provider、tenant_acquirer_channel、provider_credential_vault 表。
2. 定义 AcquirerClient interface。
3. 实现 MockAcquirer adapter。
4. 实现 ChannelRegistry：
   - Get(channelID)
   - LoadAll(ctx)
   - Reload(channelID)
   - Disable(channelID)
5. 使用 RWMutex 或 atomic snapshot，交易热路径不得访问 PG/KMS。
6. Redis Pub/Sub 订阅 channel.config.updated 事件并触发 Reload。
7. reload 失败 fail-closed 或 stale-but-serving，必须打日志和指标。

验收：
- go test ./internal/acquirer/... -count=1
- 并发 1000 goroutine Get 不 race
- 修改 channel version 后 Reload 替换 bundle
- 禁用 channel 后 Get 返回 unavailable
```

### 任务包 B：Anti-Carding Redis Sliding Window Gateway

```text
目标：在 /public/v1/order-intents 前增加统一测卡/异常流量拦截。

涉及文件：
- internal/risk/carding/
- internal/platform/redis/
- internal/platform/httpx/
- cmd/api/main.go

要求：
1. 新增 RiskDecision：Allow / Block / Challenge / Cooldown。
2. 支持维度：
   - buyer_ip
   - app_id
   - tenant_id
   - merchant_id
   - card_bin_hash
   - device_hash
3. 使用 Redis Lua 原子 INCR + EXPIRE。
4. 支持 60s / 5m / 1h 窗口。
5. 命中阈值返回 429 或 403，不调用后方 acquirer。
6. 写 risk_event 审计表。
7. 禁止存完整卡号/CVV。

验收：
- 同 IP 60s 超阈值 -> Block
- 同 BIN 多卡失败 -> Block
- 正常低频请求 -> Allow
- Redis 不可用时根据配置 fail-open 或 fail-closed，生产默认 fail-closed
```

### 任务包 C：Cashout Conversion Slippage Guard

```text
目标：为自动化换汇增加 order book 检查、滑点保护、断点续传。

涉及文件：
- internal/treasury/cashout/
- internal/worker/cashout/
- pkg/external/exchange/
- migrations/

要求：
1. 新增 treasury_cashout_policy、treasury_cashout_request、treasury_cashout_step 表。
2. 定义 ExchangeClient：
   - GetOrderBook
   - PlaceLimitOrder
   - QueryOrder
   - Withdraw
3. Conversion Worker 执行前必须读取 order book。
4. 默认 max_allowed_slippage_bps = 20。
5. 超滑点 -> ApprovalRequired，不下单。
6. timeout -> UnknownExternalState。
7. partial fill -> PartiallyFilled，并记录 actual_target_amount。
8. 所有外部调用必须在 DB 事务外。

验收：
- order book 深度不足 -> ApprovalRequired
- 滑点 <= 20bps -> 可提交 limit order
- timeout 不重复下单，只 query
- partial fill 不丢失实际成交金额
```

### 任务包 D：Dispute Ledger / Refund / Chargeback

```text
目标：补齐退款、拒付、拒付反转的逆向账务与平台风险准备金。

涉及文件：
- internal/dispute/domain/
- internal/dispute/repository/
- internal/dispute/service/
- internal/dispute/handler/
- internal/ledger/
- migrations/

要求：
1. 新增 payment_dispute 表。
2. 新增 platform_dispute_reserve_ledger 表。
3. 新增 ledger transaction types：
   - ORDER_REFUND
   - CHARGEBACK_RECEIVED
   - CHARGEBACK_REVERSED
   - CHARGEBACK_FEE
4. 实现 RefundConfirmed 入账。
5. 实现 ChargebackReceived 入账：
   - 扣 merchant settled
   - 增 platform dispute reserve
   - fee 单独入账
6. 实现 ChargebackReversed 入账。
7. settled 不足时记录 merchant_negative_balance 并 RiskHold。
8. 所有写入必须幂等。

验收：
- refund 后 merchant settled 正确减少
- chargeback 后 reserve 正确增加
- chargeback fee 正确入 fee account
- reversed 后 merchant settled 恢复
- 重复 webhook 不重复入账
- settled 不足进入 negative balance / RiskHold
```

### 任务包 E：Public Site API 与透明 SDK 边界

```text
目标：实现合规透明的站点接入 API，不实现规避型混淆。

涉及文件：
- internal/siteapp/
- internal/publicapi/
- web/sdk/
- migrations/

要求：
1. 新增 tenant_site_application 表。
2. 新增 site_order_intent 表。
3. 实现 X-App-ID / X-Timestamp / X-Nonce / X-Signature 中间件。
4. App_Secret 只允许服务端使用，不能进入 JS。
5. /public/v1/order-intents 创建订单意图。
6. 返回 checkout_url。
7. SDK 只负责加载 checkout、展示金额/商户/订单，不隐藏交易实质。

验收：
- 签名正确 -> 创建 intent
- 签名错误 -> 401
- nonce 重放 -> 409
- origin 不在 allowlist -> 403
- SDK 不包含 app_secret
```

---

## 6. 开发顺序

建议顺序：

```text
1. 任务包 D：Dispute Ledger
2. 任务包 B：Anti-Carding Gateway
3. 任务包 A：Channel Registry Object Pool
4. 任务包 E：Public Site API
5. 任务包 C：Cashout Conversion Slippage Guard
```

理由：

- 先补逆向账务，避免继续扩大资金账实风险；
- 再补前置风控，保护通道质量；
- 再优化通道热加载性能；
- 再开放标准站点接入；
- 最后做自动化 cashout，因为它对账务、风控和外部联调依赖最大。

