# NexusGlobal 全局数据流与资金拓扑图

生成日期：2026-06-14  
用途：给后续开发 AI 和人工验收者提供系统全景图，避免只看单模块代码而误解资金、数据、状态边界。  
合规边界：本文描述的是合规支付、清算、风控、对账和资金调度系统，不包含规避监管、隐藏资金来源或绕过金融机构风控的设计。

## 1. 总体拓扑

```text
                         ┌──────────────────────────────────────┐
                         │          NexusGlobal Control Plane     │
                         │  Dashboard / Admin API / Config Store  │
                         └──────────────────────────────────────┘
                                           │
                                           │ 配置、启停、热重载、风险冻结
                                           ▼
┌────────────────────┐        ┌──────────────────────────┐        ┌────────────────────┐
│ Frontline Site App │        │ Public Checkout Gateway   │        │ Risk Gateway        │
│ WordPress/Shopify  │───────▶│ /api/checkout/*           │───────▶│ Redis SlidingWindow │
│ Custom Website     │        │ /checkout?token=...       │        │ IP/App/BIN          │
└────────────────────┘        └──────────────────────────┘        └────────────────────┘
                                           │                                  │
                                           │ 风控通过                         │ 风控失败
                                           ▼                                  ▼
                              ┌──────────────────────────┐         ┌────────────────────┐
                              │ ChannelRegistry           │         │ Block / Cooldown    │
                              │ In-memory Object Pool     │         │ 不透传下游通道       │
                              └──────────────────────────┘         └────────────────────┘
                                           │
                                           ▼
          ┌────────────────────────────────────────────────────────────────────┐
          │                    Inbound Adapter Layer                            │
          │ Card Acquirer / Bank Collection / Crypto Pay                        │
          └────────────────────────────────────────────────────────────────────┘
                │                         │                         │
                ▼                         ▼                         ▼
       ┌────────────────┐       ┌────────────────────┐      ┌────────────────────┐
       │ Stripe/Nxtapay │       │ Bank Virtual Acct   │      │ Crypto Gateway/RPC  │
       │ Mock/Sandbox   │       │ Airwallex/Payoneer  │      │ USDT/USDC/BTC/ETH   │
       └────────────────┘       └────────────────────┘      └────────────────────┘
                │                         │                         │
                └───────────────┬─────────┴───────────────┬─────────┘
                                ▼                         ▼
                   ┌──────────────────────┐      ┌──────────────────────┐
                   │ Webhook Receiver      │      │ Reconciliation Worker │
                   │ Signature Verify      │      │ Polling / Matching    │
                   └──────────────────────┘      └──────────────────────┘
                                │                         │
                                └──────────────┬──────────┘
                                               ▼
                                  ┌────────────────────────┐
                                  │ Idempotency + Ledger    │
                                  │ Double-entry Accounting │
                                  └────────────────────────┘
                                               │
                                               ▼
                                  ┌────────────────────────┐
                                  │ Treasury / Asset Bridge │
                                  │ Sweep / Convert / Payout│
                                  └────────────────────────┘
                                               │
                                               ▼
          ┌────────────────────────────────────────────────────────────────────┐
          │                    Outbound Adapter Layer                           │
          │ Bank Rail / Exchange / LP / Onchain Wallet                          │
          └────────────────────────────────────────────────────────────────────┘
                │                         │                         │
                ▼                         ▼                         ▼
       ┌────────────────┐       ┌────────────────────┐      ┌────────────────────┐
       │ ACH/Wire/SEPA  │       │ Exchange/LP         │      │ Wallet/Custody      │
       │ Bank Clearing  │       │ Quote/Order/Convert │      │ Withdraw/Confirm    │
       └────────────────┘       └────────────────────┘      └────────────────────┘
```

## 2. 入金路径（Inbound Path）

### 2.1 卡网收单路径

```text
Site App
  │
  │ 创建订单 / 获取 checkout token
  ▼
Public Checkout API
  │
  │ /api/checkout/token
  │ /checkout?token=...
  │ /api/checkout/charge
  ▼
Anti-Carding Gateway
  │
  ├─ buyer_ip sliding window
  ├─ merchant/app sliding window
  └─ card_bin sliding window
  │
  ├─ Block: 429/403，不调用 acquirer
  └─ Pass
       ▼
ChannelRegistry
  │
  ├─ local/mock: mock_default
  ├─ sandbox: stripe/nxtapay sandbox
  └─ real/prod: 缺 provider/credential 即 fail-closed
       ▼
Acquirer Adapter
  │
  ├─ Authorize
  ├─ Capture
  ├─ Refund
  ├─ QueryPayment
  └─ VerifyWebhook
       ▼
Webhook Receiver / Reconcile
       ▼
Ledger Injection Point
```

关键约束：

- real/prod 不允许 raw card_number/card_cvv。
- real/prod 必须使用 `payment_method_token`。
- 未知 provider 在 real/prod 必须 fail-closed。
- 不得在 DB transaction 内进行外部网络写操作。

### 2.2 银行虚拟户入金路径

```text
Order Intent
  │
  ▼
Universal Adapter Registry
  │
  ▼
BANK_COLLECTION Adapter
  │
  ├─ CreateInboundIntent
  ├─ Allocate Virtual Account
  ├─ QueryInboundIntent
  └─ VerifyWebhook
       │
       ▼
Bank Collection Webhook / Polling
       │
       ├─ amount mismatch -> Review / Reject
       ├─ currency mismatch -> Review / Reject
       ├─ reference mismatch -> Review / Reject
       └─ matched
            ▼
          Ledger
```

### 2.3 Crypto Pay 入金路径

```text
Order Intent
  │
  ▼
CRYPTO_PAY Adapter
  │
  ├─ Allocate Deposit Address
  ├─ Listen Chain/RPC/Webhook
  ├─ Confirm tx hash idempotency
  └─ Check confirmations threshold
       │
       ▼
Chain Transaction Reconcile
       │
       ├─ wrong network -> Reject
       ├─ amount mismatch -> Review
       ├─ duplicate tx -> Idempotent no-op
       └─ confirmed
            ▼
          Ledger
```

## 3. 出金与资产桥路径（Outbound Cashout Path）

```text
Ledger Settled Balance
  │
  ▼
Treasury Evaluation
  │
  ├─ merchant/entity RiskHold? -> Block
  ├─ bank calendar available? -> Continue
  ├─ buffer_percent reserve -> Keep floor
  └─ sweep threshold met
       ▼
Asset Bridge Job
  │
  ├─ CREATED
  ├─ PENDING_APPROVAL / APPROVAL_REQUIRED
  ├─ EXECUTING
  ├─ STEP_RUNNING
  ├─ STEP_COMPLETED
  ├─ UNKNOWN_EXTERNAL_STATE
  ├─ COMPLETED
  └─ FAILED
       ▼
Transactional Outbox
  │
  ▼
Bridge Worker
  │
  ├─ BANK_RAIL: ACH / Wire / SEPA
  ├─ EXCHANGE: quote/order/convert
  ├─ LP: quote/accept/payout
  └─ ONCHAIN_WALLET: withdraw/confirm
       ▼
Ledger Settlement / Reconcile
```

## 4. 账本拓扑

```text
ledger_transaction
  │
  ├─ transaction_type
  │    ├─ ORDER_CAPTURED
  │    ├─ ORDER_REFUND
  │    ├─ CHARGEBACK_RECEIVED
  │    ├─ CHARGEBACK_REVERSED
  │    └─ ASSET_EXCHANGE
  │
  └─ ledger_entry[]
       ├─ debit account
       ├─ credit account
       ├─ amount
       ├─ currency / asset_id
       ├─ merchant_id
       └─ corporate_entity_id
```

账户类型示意：

```text
Merchant Settled
Merchant Frozen Payout
Merchant Reserve
Merchant Dispute Hold
Merchant Negative Balance
Platform Dispute Reserve
Platform Fee
Asset Bridge In Transit
Exchange Clearing
Onchain Clearing
Asset Exchange Gain/Loss
```

复式记账红线：

- 每笔交易借贷必须平衡。
- 多资产 exchange 需要资产侧与估值/损益侧都能表达。
- 拒付/退款必须反向冲正，不得只写业务状态。
- 账务写入必须原子化。

## 5. 风控与 Fail-Closed 矩阵

| 场景 | 期望行为 |
|---|---|
| buyer IP 连续失败 | Redis 计数触发，checkout 直接返回 429 |
| card BIN 连续失败 | Redis 计数触发，不透传 acquirer |
| merchant/app 失败率异常 | 进入 cooldown 或 RiskHold |
| real 模式收到 raw PAN/CVV | 拒绝请求 |
| real 模式缺 provider credential | fail-closed |
| unknown provider in production | fail-closed |
| 外部 API timeout | `UNKNOWN_EXTERNAL_STATE`，等待 reconcile |
| 滑点超过 20 bps | `APPROVAL_REQUIRED` |
| 法人实体 RiskHold | 阻断该 entity 下通道与 sweep |
| 拒付导致余额不足 | 进入 negative balance，不破坏账本平衡 |

## 6. 可视化管理拓扑

```text
Dashboard
  │
  ├─ 总览
  │    └─ /admin/stats
  │
  ├─ 通用适配器
  │    ├─ /admin/adapter/providers
  │    └─ /admin/adapter/channels
  │
  ├─ 法人实体
  │    └─ /admin/entities
  │
  ├─ 资产桥配置
  │    └─ /admin/asset-bridge/profiles
  │
  ├─ 钱包白名单
  │    └─ /admin/wallet-policies
  │
  ├─ 风控运维
  │    ├─ /admin/risk/events
  │    ├─ /admin/risk/events/{id}
  │    ├─ /admin/outbox
  │    └─ /admin/outbox/{id}
  │
  └─ 兼容管理
       └─ /admin/acquirer/*
```

Dashboard 当前验收状态：

- 全中文可见文案。
- 不含历史乱码。
- 不含非法 `< button`。
- 不含“请到数据库查询”。
- 危险操作带确认。
- 详情操作调用真实 API。

## 7. 当前缺口

唯一关键缺口是外部真实 sandbox：

```text
Stripe Sandbox Credential
Nxtapay Sandbox Credential
Webhook Secret
真实外部请求
真实 webhook 验签
真实错误码/3DS/timeout/decline 映射
真实 ledger idempotency 验证
```

在上述测试通过前，不得声明“真实生产预上线 100%”。

