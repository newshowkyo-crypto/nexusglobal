# NexusGlobal V2.0 系统拓扑、多租户接入与自动化财资全景技术白皮书

版本：V2.0  
日期：2026-06-14  
边界：本文以合规收单、反欺诈、资金安全、可审计清结算和沙箱到生产的工程演进为边界。所有真实外部通道、银行清算、交易所或 LP 连接器均必须在持牌主体、合规账户、KYC/KYB、AML、制裁筛查、PCI-DSS 与当地监管要求下实施。

---

## 0. 当前系统基线

截至 `PROJECT_PROGRESS_SNAPSHOT_2026-06-14`，NexusGlobal 已完成以下底层闭环：

- `ChannelRegistry`：动态通道客户端注册表、本地内存对象池、Redis Pub/Sub 热重载。
- `Acquirer Admin API`：provider/channel/credential 的创建、启停、reload、rotate-secret。
- `Mock Acquirer`：本地演示三态 `success / failure / timeout` 已真实从 `channel_config.scenarios` 生效。
- `Checkout Gateway`：checkout charge 主路径已通过 `ChannelRegistry`，不再硬编码 mock gateway。
- `Risk/Carding Gateway`：Redis 滑动窗口前置风控已接入 checkout。
- `Dispute Ledger`：refund、chargeback、chargeback reversed、平台争议准备金、商户负余额已完成核心状态机。
- `Real Mode Boundary`：真实模式禁止 raw PAN/CVV，必须使用 `payment_method_token`，缺少真实配置 fail-closed。

当前尚未完成：

- 真实 Stripe / Nxtapay adapter 的生产级 API 调用。
- Binance / OKX / LP 等 Exchange Connector 的生产级实现。
- ACH/Wire/SEPA Bank Clearing Rail 的生产级轮询、回调和对账实现。
- 完整资金上链或数字资产换汇能力仅处于设计阶段，不能视为生产可用。

---

## 1. 统一数据流与资金流向拓扑

### 1.1 全局逻辑拓扑

```text
                         +----------------------------------+
                         |          Admin Console            |
                         | provider / channel / weight / risk|
                         +-----------------+----------------+
                                           |
                                           | config update
                                           v
                         +----------------------------------+
                         | Redis Pub/Sub + Redis Risk Window |
                         +-----------------+----------------+
                                           |
                 hot reload                | sliding counters
                                           |
+----------------------+       +-----------v------------+       +-----------------------+
| Frontline Site Apps  | ----> | Public Gateway Layer   | ----> | ChannelRegistry Pool  |
| WP / Shopify / SaaS  |       | /public/v1/order-intents|      | in-memory clients     |
+----------+-----------+       +-----------+------------+       +-----------+-----------+
           |                               |                                |
           | App_ID/App_Secret             | anti-carding decision          | decrypted client
           | signed request                |                                |
           v                               v                                v
  +------------------+           +------------------+             +---------------------+
  | Checkout Token   |           | Risk Gateway     |             | Acquirer Adapter    |
  | Order Intent     |           | IP/App/BIN window|             | Stripe/Nxtapay/Mock |
  +------------------+           +------------------+             +----------+----------+
                                                                          |
                                                                          | auth/capture/refund/query
                                                                          v
                                                               +------------------------+
                                                               | External Acquirer      |
                                                               | sandbox / production   |
                                                               +-----------+------------+
                                                                           |
                                                                           | webhook / status query
                                                                           v
                                                               +------------------------+
                                                               | Ledger Injection Points|
                                                               | order/payment/dispute  |
                                                               +-----------+------------+
                                                                           |
                                                                           v
                                                               +------------------------+
                                                               | Treasury / Payout /    |
                                                               | Reconcile Workers      |
                                                               +------------------------+
```

### 1.2 入金路由线 Inbound Path

```text
[Site App]
  |
  | 1. create order intent
  |    headers: App_ID, signature, timestamp, nonce
  |    body: order_id, amount, currency, buyer context, payment method token
  v
[/public/v1/order-intents]
  |
  | 2. authenticate site application
  |    - validate App_ID exists and active
  |    - verify HMAC with App_Secret
  |    - reject replay by nonce/timestamp
  v
[Anti-Carding Gateway]
  |
  | 3. Redis sliding-window checks
  |    - buyer_ip failure counter
  |    - app_id failure counter / failure rate
  |    - card_bin failure counter
  |    - merchant cooldown / RiskHold
  |
  | if blocked: return 429/403 and DO NOT call acquirer
  v
[Routing Decision]
  |
  | 4. resolve site_app -> route profile -> weighted channel list
  |    - only active channels
  |    - currency/payment_method compatible
  |    - risk status compatible
  |    - health score above threshold
  v
[ChannelRegistry Object Pool]
  |
  | 5. read pre-instantiated client from memory
  |    - no PG access on hot path
  |    - no KMS/decryption on hot path
  |    - fail-closed if channel missing/disabled
  v
[Acquirer Adapter]
  |
  | 6. Authorize / Capture / QueryPayment
  |    - Stripe adapter
  |    - Nxtapay adapter
  |    - Mock adapter in local mode
  v
[Ledger Injection Points]
  |
  | 7. write transactional records
  |    - payment accepted / authorized / captured
  |    - acquirer provider_ref
  |    - fee estimate
  |    - merchant receivable
  |    - platform reserve
  |    - webhook reconciliation marker
  v
[Treasury Reconcile]
```

核心原则：

- 风控在 acquirer 调用之前执行，命中拦截时不透传后方通道。
- 热路径只访问内存对象池与 Redis 风控计数，不访问数据库解密链路。
- 真实模式只接受 tokenized payment method，不接受 raw PAN/CVV。
- 所有外部状态必须最终通过账本、webhook、reconcile 三线闭环确认。

### 1.3 出金对冲线 Outbound Cashout Path

本文中的 outbound 设计仅适用于合规清结算、受监管资金划拨和持牌机构账户。该路径不得用于规避 AML、制裁筛查、税务、资金来源审查或收单机构规则。

```text
[Acquirer Settlement]
  |
  | T+N settlement report / balance available
  v
[Settlement Reconcile Worker]
  |
  | match: acquirer batch -> ledger receivable -> bank incoming
  v
[Bank Account]
  |
  | ACH / Wire / SEPA clearing status
  v
[Primary Sweep Engine]
  |
  | sweep rules:
  | - reserve floor
  | - dispute reserve
  | - merchant RiskHold
  | - compliance hold
  | - liquidity threshold
  v
[Secondary Sweep Engine]
  |
  | transfer to approved destination only:
  | - regulated exchange institutional account
  | - licensed LP account
  | - treasury custody account
  v
[Exchange / LP Connector]
  |
  | 1. poll deposit status
  | 2. fetch order book
  | 3. compute executable notional
  | 4. enforce max_allowed_slippage = 0.002
  | 5. submit limit order / sliced order
  | 6. reconcile fills
  v
[Conversion State Machine]
  |
  | Filled / PartiallyFilled / ApprovalRequired / UnknownExternalState
  v
[Withdrawal Connector]
  |
  | allowlisted destination only
  | daily limit / velocity / operator approval
  v
[Cold Wallet / Custody Account]
  |
  | proof, tx hash, reconciliation
  v
[Treasury Ledger Finalization]
```

### 1.4 账本注入点

入金与出金都必须有明确账本注入点：

| 阶段 | 账本/状态注入 |
| --- | --- |
| OrderIntentCreated | 创建订单意图、记录幂等键 |
| PaymentAuthorized | 记录授权状态，不释放可用余额 |
| PaymentCaptured | 增加商户待结算余额，记录通道引用 |
| AcquirerSettled | 待结算转 settled，记录结算批次 |
| RefundConfirmed | 逆向冲正，扣减商户余额 |
| ChargebackReceived | 扣减 settled / 增加负余额 / 增加平台争议准备金 |
| ChargebackReversed | 释放平台争议准备金并更新 dispute 状态 |
| BankSweepInitiated | 锁定待划拨资金，写 outbox |
| BankSweepSettled | 银行划拨完成，更新资金位置 |
| ExchangeDepositConfirmed | 交易所/LP 入账确认 |
| ConversionFilled | 记录成交均价、滑点、手续费 |
| WithdrawalCompleted | 记录链上 tx hash 或 custody reference |

---

## 2. 傻瓜式站群接入与多租户动态调度规范

### 2.1 多租户元数据模型

建议新增或完善以下逻辑表。字段仅为工程蓝图，实际 migration 应按现有命名风格落地。

#### `site_applications`

站群元数据表，描述每一个前线站点或业务应用。

| 字段 | 说明 |
| --- | --- |
| `id` | UUID 主键 |
| `tenant_id` | 租户 ID |
| `merchant_id` | 商户 ID |
| `app_id` | 前线接入标识，公开但不可猜 |
| `app_secret_ciphertext` | 加密后的 HMAC secret |
| `site_domain` | 站点域名 |
| `platform_type` | WordPress / Shopify / Custom |
| `default_currency` | 默认币种 |
| `status` | Active / Disabled / RiskHold |
| `route_profile_id` | 当前路由策略 |
| `risk_profile_id` | 当前风控策略 |
| `created_at / updated_at` | 审计时间 |

#### `route_profiles`

路由配置主表。

| 字段 | 说明 |
| --- | --- |
| `id` | UUID 主键 |
| `tenant_id` | 租户 ID |
| `name` | 策略名称 |
| `status` | Active / Disabled |
| `version` | 乐观锁与热重载版本 |
| `updated_by` | 操作人 |

#### `route_profile_channels`

站点到通道的权重映射。

| 字段 | 说明 |
| --- | --- |
| `route_profile_id` | 路由策略 ID |
| `channel_id` | `tenant_acquirer_channel.id` |
| `weight` | 权重，0 表示暂停接流 |
| `priority` | 优先级 |
| `currency_allowlist` | 支持币种 |
| `payment_method_allowlist` | 支持 payment method |
| `min_amount / max_amount` | 金额区间 |
| `health_required` | 最低健康分 |
| `risk_mode` | Normal / Conservative / Block |

#### `tenant_acquirer_channel`

当前已存在的通道仓库表。它是后方收单能力的真实配置来源。

关键职责：

- 绑定 provider。
- 保存 channel code/name。
- 保存支持币种、支付方式和通道配置。
- 通过 credential vault 间接绑定密钥。
- 使用 `version` 与 Redis Pub/Sub 支持热重载。

### 2.2 三参数接入流程

前线站群后端只需要配置：

```text
Gateway_URL = https://gateway.example.com
App_ID      = app_xxxxxxxxx
App_Secret  = secret stored only in site backend
```

标准请求流程：

```text
1. Site backend receives local checkout request
2. Site backend builds canonical payload
3. Site backend computes signature:
   HMAC_SHA256(App_Secret, method + path + timestamp + nonce + body_hash)
4. Site backend calls:
   POST {Gateway_URL}/public/v1/order-intents
5. Gateway validates:
   - app_id exists
   - app status is Active
   - timestamp within tolerance
   - nonce not replayed
   - signature valid
   - domain/referrer optional allowlist
6. Gateway returns:
   - order_intent_id
   - checkout_token
   - hosted_checkout_url or client_secret
```

前线网站不需要知道：

- 当前走 Stripe、Nxtapay 还是其他 provider。
- 当前通道密钥。
- 当前路由权重。
- 当前风控阈值。
- 当前通道是否刚刚被切换。

### 2.3 站群 SDK 边界

建议 SDK 分为两层：

- Frontend JS SDK：只负责渲染、重定向、token 传递、基本字段校验。
- Backend Auth Middleware：负责 App_ID/App_Secret 签名、nonce、timestamp、body hash。

安全边界：

- App_Secret 只能存在站点后端，不进入浏览器。
- Frontend JS SDK 不持有通道信息。
- Real mode 不传 raw PAN/CVV 给 NexusGlobal；真实卡数据应由合规 tokenization provider 或 PSP hosted fields 处理。
- 所有前线请求必须可追踪到 `site_app_id -> tenant_id -> merchant_id`。

### 2.4 动态切流机制

运营后台修改路由权重后的传播路径：

```text
[Admin Console]
  |
  | update route_profile_channels / tenant_acquirer_channel
  v
[PostgreSQL Transaction]
  |
  | increment version
  | write audit log
  v
[Redis Pub/Sub]
  |
  | publish route_profile_id|version
  | publish channel_id|version
  v
[API Instances]
  |
  | subscriber receives event
  | compare local version
  | reload DB config only on newer version
  v
[In-memory Route Cache + ChannelRegistry]
  |
  | atomic replace
  v
[New traffic uses new route]
```

平滑切换原则：

- 新请求读取新权重。
- 已进入执行的请求保持原 channel，不中途换通道。
- 权重为 0 表示停止接收新流量，但不影响在途查询、退款、拒付处理。
- disable channel 必须保留 QueryPayment / Refund / Webhook 处理能力，避免历史订单无法对账。
- route reload 失败时 fail-closed 或继续 stale-but-serving，取决于风险等级。

### 2.5 多租户隔离模型

最低隔离要求：

- 所有业务表带 `tenant_id`。
- 所有 admin query 必须按 tenant scope 过滤。
- channel credential vault 按 provider/channel 上下文做 AAD 绑定。
- site app 的 App_ID 在全局唯一，解析后落到 tenant scope。
- ledger account 按 merchant + currency + account type 隔离。
- Redis key 命名必须包含 tenant/app/merchant 维度，避免串租户。

建议 Redis key：

```text
risk:{tenant_id}:{app_id}:ip:{ip}:fail
risk:{tenant_id}:{app_id}:bin:{bin}:fail
risk:{tenant_id}:{app_id}:app:fail_rate
route:{tenant_id}:{route_profile_id}:version
channel:{tenant_id}:{channel_id}:version
nonce:{app_id}:{nonce}
```

---

## 3. 生产上线前的最后两个核心 Adapter 实施标准

### 3.1 Exchange Connector API

Exchange Connector 仅面向合规机构账户、白名单提现地址与可审计 treasury 操作。禁止将其设计成绕过银行、收单机构或监管审查的工具。

#### 3.1.1 接口抽象

```text
ExchangeConnector
  - GetAccountBalances(ctx)
  - GetDepositStatus(ctx, depositRef)
  - GetOrderBook(ctx, symbol, depth)
  - EstimateExecution(ctx, symbol, side, amount)
  - PlaceLimitOrder(ctx, request)
  - CancelOrder(ctx, orderID)
  - GetOrderStatus(ctx, orderID)
  - Withdraw(ctx, request)
  - GetWithdrawalStatus(ctx, withdrawalID)
```

核心实体：

```text
ConversionJob
  id
  tenant_id
  source_asset
  target_asset
  source_amount
  max_allowed_slippage
  state
  external_account_id
  order_ids[]
  filled_amount
  avg_fill_price
  idempotency_key
  retry_count
  next_retry_at
  approval_reason
```

状态机：

```text
Created
  -> WaitingDepositConfirmation
  -> LiquidityCheck
  -> ReadyToPlaceOrder
  -> OrderSubmitted
  -> PartiallyFilled
  -> Filled
  -> WithdrawalPending
  -> WithdrawalCompleted
  -> LedgerFinalized

Exceptional:
  -> ApprovalRequired
  -> UnknownExternalState
  -> CancelPending
  -> Cancelled
  -> FailedClosed
```

#### 3.1.2 防死锁设计

死锁来源通常不是数据库锁，而是外部状态不可知导致 worker 不敢前进、不敢回滚。

防死锁原则：

- 每个外部请求必须有本地 outbox 记录。
- 每个外部请求必须有 idempotency key 或 client order id。
- worker 重启后先查本地 state，再查外部 state，最后决定补偿动作。
- 任何 `timeout` 不等于失败，必须进入 `UnknownExternalState` 或状态查询队列。
- 对同一 `ConversionJob` 使用数据库行锁或 advisory lock，避免并发 worker 双花式提交。

建议 worker 步骤：

```text
1. SELECT job FOR UPDATE SKIP LOCKED
2. Verify job state and idempotency key
3. If previous external_request exists:
   - query external state first
   - never submit duplicate order blindly
4. If no external_request:
   - create outbox record in same DB transaction
   - commit local state as ReadyToSend
5. Send external API request
6. Persist response or timeout marker
7. Schedule reconcile poll
```

#### 3.1.3 Limit Order 与冰山委托

禁止默认使用大额 market order。转换前必须执行深度检查：

```text
Inputs:
  symbol = USD/USDT or EUR/USDT equivalent
  amount = source fiat or stablecoin notional
  max_allowed_slippage = 0.002

Process:
  1. fetch order book
  2. compute VWAP for target amount
  3. compare VWAP with reference price
  4. if estimated_slippage > 0.002:
       state = ApprovalRequired
       reason = SlippageExceeded
       do not place order
  5. if liquidity ok:
       split amount into slices
       submit limit orders with bounded price
```

冰山/分批原则：

- 单笔 slice 不超过当时可见深度的固定比例。
- slice 间隔加入最小时间间隔，避免瞬间扫薄盘口。
- 每个 slice 独立 client order id。
- 部分成交后重新评估剩余深度。
- 超时未成交的 limit order 进入 cancel/reprice 流程。
- reprice 仍不得突破 20 bps 滑点上限。

#### 3.1.4 `ApprovalRequired` 人工调单机制

必须进入人工审批的情况：

- 预估滑点超过 `0.002`。
- order book 深度不足。
- exchange API 返回异常限额、风控或账户状态错误。
- limit order 部分成交后剩余部分长时间无法成交。
- withdrawal destination 与白名单不匹配。
- 外部状态连续多次查询不一致。

人工审批 console 必须展示：

- job id、tenant、merchant、资金来源批次。
- source/target asset、金额、参考价格。
- order book 快照摘要。
- 预估滑点与原因。
- 已成交数量、均价、手续费。
- 可选动作：继续等待、取消剩余、降低金额、人工关闭、升级合规复核。

所有人工动作必须写 audit log。

### 3.2 Bank Clearing Rail

Bank Clearing Rail 只处理合规银行账户之间的清算状态。ACH、Wire、SEPA 都是异步网络，不能把 API 提交成功视为资金完成。

#### 3.2.1 接口抽象

```text
BankClearingConnector
  - CreateTransfer(ctx, request)
  - GetTransferStatus(ctx, transferID)
  - ListAccountTransactions(ctx, cursor)
  - GetBalance(ctx, accountID)
  - CancelTransfer(ctx, transferID) optional
```

统一状态：

```text
Draft
  -> PendingComplianceCheck
  -> Submitted
  -> Processing
  -> Settled
  -> Rejected
  -> Returned
  -> Cancelled
  -> UnknownExternalState
```

#### 3.2.2 ACH / Wire / SEPA 差异

| Rail | 特性 | 工程处理 |
| --- | --- | --- |
| ACH | 批处理、可能退回、T+N | 必须支持 Returned 状态和延迟对账 |
| FedWire | 实时性更强、费用高、不可逆性强 | 提交前强校验 beneficiary 与限额 |
| SEPA Credit Transfer | 区域内异步清算 | 支持 end-to-end id 和对账引用 |
| SEPA Instant | 更快但限额不同 | 必须按银行能力路由 |

#### 3.2.3 Outbox 断点续传

银行清算 worker 必须采用 outbox + 状态轮询：

```text
[DB Tx]
  - create bank_transfer
  - create outbox event BankTransferRequested
  - lock treasury balance
  - commit

[Worker]
  - pick outbox event
  - submit CreateTransfer with idempotency key
  - store external_transfer_id if returned
  - mark event Sent or UnknownExternalState

[Poller]
  - query external status by external_transfer_id or idempotency key
  - update local state
  - release/settle/reverse locked balance
  - emit next treasury event
```

超时处理：

- `CreateTransfer` 超时：不得立即重试创建新 transfer；先用 idempotency key 查询。
- `GetTransferStatus` 超时：保持 `UnknownExternalState`，指数退避重试。
- 银行返回 `Rejected`：释放锁定余额，记录失败原因。
- 银行返回 `Returned`：生成逆向账务分录并触发人工复核。
- 银行状态长期不变：进入 `ApprovalRequired` 或 operation queue。

#### 3.2.4 对账保障

最低对账输入：

- 银行交易流水。
- acquirer settlement report。
- treasury transfer record。
- exchange/LP deposit record。
- internal ledger transaction。

对账规则：

- 金额、币种、日期、reference、beneficiary 多维匹配。
- 允许时区和银行工作日延迟。
- 不允许无法解释的余额差异自动进入下一步换汇。
- 未对账资金必须保持 hold 状态。

---

## 4. 极端场景下的防御边界与异常 Fail-Closed 矩阵

### 4.1 Fail-Closed 总原则

系统默认策略：

- 无法确认身份：拒绝。
- 无法确认通道：拒绝。
- 无法确认风控状态：按高风险策略拒绝或降级。
- 无法确认外部资金状态：挂起，不继续下一跳。
- 无法确认账本平衡：阻断出金。
- 无法确认 webhook 签名：丢弃并记录安全事件。

### 4.2 异常矩阵

| 场景 | 检测点 | 本地状态 | 系统动作 | 是否调用外部通道 |
| --- | --- | --- | --- | --- |
| 海量测卡攻击 | Redis IP/App/BIN 窗口 | RiskBlocked | 返回 429/403，写 risk event | 否 |
| 单站点失败率暴涨 | app failure rate | AppRiskHold | 暂停该 App 新交易 | 否 |
| 商户被风控锁定 | merchant status | RiskHold | 阻断新入金与出金 | 否 |
| ChannelRegistry miss | registry.Get | ChannelUnavailable | fail-closed | 否 |
| 通道被 disable | channel active=false | ChannelDisabled | 新交易拒绝，历史查询保留 | 否/仅历史操作 |
| Redis 不可用 | risk middleware | RiskUnknown | 按配置 fail-closed 或保守降级 | 默认否 |
| PG 不可用 | API service | StorageUnavailable | 拒绝创建新交易 | 否 |
| Acquirer API timeout | adapter | UnknownExternalState | 查询外部状态，不重复提交 | 不重复提交 |
| Acquirer declined | adapter | PaymentFailed | 记录失败，增加风控计数 | 已调用一次 |
| Webhook 验签失败 | webhook handler | SecurityRejected | 丢弃、审计、报警 | 否 |
| Exchange API timeout | conversion worker | UnknownExternalState | 暂停下一步，轮询确认 | 不重复下单 |
| 滑点超过 20bps | conversion worker | ApprovalRequired | 人工复核，不下单 | 否 |
| Limit order 部分成交 | exchange poller | PartiallyFilled | 重新评估剩余流动性 | 受控 |
| Bank transfer timeout | clearing worker | UnknownExternalState | 用 idempotency key 查询 | 不重复创建 |
| Bank transfer returned | clearing poller | Returned | 逆向分录，人工复核 | 否 |
| Ledger imbalance | ledger validator | LedgerBlocked | 阻断 payout/sweep | 否 |
| Chargeback 超过 settled | dispute service | NegativeBalance | 增加商户负余额与准备金记录 | 否 |

### 4.3 测卡攻击熔断边界

Anti-Carding Gateway 必须部署在 `/public/v1/order-intents` 或 charge 之前。

多维计数：

```text
buyer_ip:
  window = 60s
  threshold = N failures

app_id:
  window = 1h
  threshold = failure_rate > configured value

card_bin:
  window = 5m
  threshold = N failures

merchant:
  state = Active / RiskHold / Disabled
```

拦截行为：

- 返回 mock-like generic error，避免暴露具体风控规则。
- 不调用后方通道。
- 写入 risk event。
- 对 app/merchant 可升级为 cooldown 或 RiskHold。
- 管理后台展示命中维度、时间窗口、影响订单数。

### 4.4 `UnknownExternalState` 处理

`UnknownExternalState` 是系统最重要的安全状态之一。它表示本地不知道外部是否已经执行成功，因此禁止盲目重试写操作。

适用对象：

- acquirer authorize/capture/refund。
- bank transfer create。
- exchange order place/cancel。
- withdrawal request。

处理原则：

```text
if write request timed out:
  mark UnknownExternalState
  persist idempotency key / client order id
  poll external status
  reconcile before next state transition
else if external confirms not found and idempotency window valid:
  allow controlled retry
else if external confirms success:
  advance local state
else:
  ApprovalRequired
```

### 4.5 平台争议准备金核销逻辑

`platform_dispute_reserve_ledger` 的职责不是吞掉损失，而是把外部拒付、罚款、退款等逆向事件变成可追踪、可核销、可审计的准备金流水。

#### Chargeback Received

```text
Input:
  dispute_amount
  chargeback_fee
  merchant_settled_balance

Process:
  1. create payment_dispute
  2. debit merchant settled balance up to available amount
  3. if settled insufficient:
       create merchant_negative_balance for shortage
  4. credit platform_dispute_reserve_ledger for dispute amount
  5. book chargeback fee separately
  6. if risk threshold exceeded:
       set merchant/site_app RiskHold
```

#### Chargeback Reversed

```text
Input:
  dispute_id
  reserve balance

Process:
  1. verify dispute status is OPEN or UNDER_REVIEW
  2. verify platform dispute reserve balance is sufficient
  3. update dispute status to REVERSED
  4. debit platform_dispute_reserve_ledger
  5. create ledger reversal transaction
  6. release merchant impact if policy allows
```

#### 商户负余额

当拒付金额和罚款超过商户 settled balance：

- 不允许平台账本静默不平。
- 差额进入 `merchant_negative_balance`。
- 后续入金先冲抵负余额。
- 商户处于 RiskHold 时不得继续 payout/sweep。

### 4.6 RiskHold 对全局资金归集的影响

RiskHold 是跨服务硬边界，而不是 UI 状态。

进入 RiskHold 后：

- 新 order intent 拒绝或进入人工审核。
- checkout charge 阻断。
- payout 创建阻断。
- sweep engine 排除该 merchant 的可划拨资金。
- treasury worker 不将该 merchant 的资金推进到下一跳。
- dashboard 显示锁定原因、触发事件、解除条件。
- 所有解除动作必须有双人复核或高权限审计，具体按合规策略配置。

### 4.7 服务级 Fail-Closed 边界

| 服务 | Fail-Closed 边界 |
| --- | --- |
| Public Gateway | 签名失败、nonce 重放、App disabled、RiskHold 时拒绝 |
| Risk Gateway | Redis 不可用时默认保守拒绝，或进入只允许低风险白名单 |
| ChannelRegistry | channel missing/disabled/stale beyond TTL 时拒绝新交易 |
| Acquirer Adapter | real mode 禁止 raw PAN/CVV，provider error 不回退 mock |
| Ledger Service | 分录不平、账户不存在、币种不一致时拒绝提交 |
| Dispute Service | 事务内写 dispute、reserve、negative balance，任一失败整体回滚 |
| Treasury Worker | 未对账、未过 reserve floor、RiskHold 时不 sweep |
| Bank Rail Worker | timeout 进入 UnknownExternalState，不重复创建 transfer |
| Exchange Worker | 滑点超标、深度不足、API 状态未知时 ApprovalRequired |
| Withdrawal Worker | 非白名单地址、超限额、状态未知时拒绝或挂起 |
| Dashboard/Admin | 所有通道、路由、风控变更写 audit log 并发布版本事件 |

---

## 5. 落地优先级

### P0：真实 Acquirer Adapter

目标：从 mock/local 演示进入 sandbox 真实 provider 验收。

交付标准：

- Stripe sandbox Authorize / Capture / Refund / QueryPayment。
- Stripe webhook 验签与事件映射。
- Nxtapay sandbox Authorize / Capture / Refund / QueryPayment。
- Nxtapay webhook 验签与事件映射。
- provider-specific 错误码统一映射。
- 不存 PAN/CVV。
- adapter contract tests。
- sandbox integration tests。

### P1：Multi-tenant Routing

目标：完成站群元数据和通道权重动态切流。

交付标准：

- `site_applications`。
- `route_profiles`。
- `route_profile_channels`。
- App_ID/App_Secret 签名鉴权。
- route cache + Redis Pub/Sub 热重载。
- dashboard route weight 编辑。
- 切流审计日志。

### P2：Exchange Connector Sandbox

目标：完成可审计、可挂起、可人工复核的 conversion worker。

交付标准：

- order book 检索。
- VWAP/slippage estimate。
- max slippage 20 bps。
- limit order / sliced order。
- UnknownExternalState。
- ApprovalRequired console。
- fill reconciliation。

### P3：Bank Clearing Rail Sandbox

目标：完成 ACH/Wire/SEPA 的异步状态机与 outbox 断点续传。

交付标准：

- create transfer。
- status poller。
- Returned/Rejection reversal。
- balance lock/release。
- statement reconciliation。
- operation queue。

### P4：Production Readiness

目标：上线前安全、合规、审计和可靠性验收。

交付标准：

- PCI-DSS scope review。
- KYB/KYC/AML policy integration。
- secrets rotation drill。
- disaster recovery drill。
- load test。
- reconciliation dry run。
- incident runbook。
- dashboard manual acceptance。

---

## 6. 最终结论

NexusGlobal 当前已完成从“本地 mock 支付系统”到“动态通道、前置风控、逆向账本和管理后台骨架”的关键跃迁。系统已经具备站群统一接入和一键切换真实通道的工程基础，但真实生产能力仍取决于后续三个未完成层：

1. 真实 acquirer adapter 与 webhook 对账。
2. 多租户路由权重与站群 App 鉴权模型。
3. 银行清算与 exchange/LP connector 的合规沙箱实现。

下一阶段的工程重点不是继续扩展 mock，而是把真实外部连接器按 fail-closed、idempotent、reconcilable、auditable 四个标准逐个落地。
