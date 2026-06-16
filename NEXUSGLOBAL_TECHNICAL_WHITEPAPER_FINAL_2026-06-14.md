# NexusGlobal 技术全景白皮书（本地预上线最终版）

生成日期：2026-06-14  
系统定位：面向多租户、多模态入金、统一账本、自动化财资与可视化运维的合规清算中台。  
当前阶段：mock/local 预上线门禁完成；真实外部 sandbox 等待凭证验证。

## 1. 设计目标

NexusGlobal 的目标不是单一支付网关，而是一个可配置、可审计、可热重载的资金与状态中台。系统以统一账本为中心，将卡网收单、银行虚拟户入金、加密资产入金、银行清算、交易所/LP 转换、链上钱包出金等能力抽象为 adapter，并通过可视化后台管理。

核心目标：

- 前线接入简单。
- 中台动态管理通道。
- 后端状态机可审计。
- 所有资金动作可追溯。
- real/production 默认 fail-closed。
- mock/local 演示完整可用但不能污染真实路径。

## 2. 技术栈基线

| 层级 | 技术 |
|---|---|
| 语言 | Go |
| HTTP Router | chi |
| 数据库 | PostgreSQL 15 |
| DB Driver | pgx/v5 |
| SQL 生成 | sqlc（部分模块） |
| 缓存/队列 | Redis 7 |
| 消息模式 | Redis Streams / PubSub / transactional outbox |
| 加密 | mock KMS / Vault KMS 接口 |
| 测试 | Go test、testcontainers、httptest |
| 本地部署 | Docker Compose |
| 管理 UI | Go 内嵌 HTML Dashboard |

## 3. 架构原则

### 3.1 账本优先

任何资金状态变化都必须最终落到账本或可对账的状态表。业务状态不能替代账务分录。

### 3.2 外部 I/O 不进本地事务

数据库事务只处理本地原子状态变化。外部 HTTP 写动作通过 outbox、worker、idempotency key、reconcile 处理。

### 3.3 热路径零查密钥

交易热路径从内存 registry 获取已实例化 client，不在每笔交易中反复查库、解密、创建 HTTP client。

### 3.4 mock 与 real 硬隔离

mock/local 用于演示和 CI。real/production 禁止 silent mock、禁止未知 provider fallback、禁止 raw PAN/CVV。

### 3.5 风控前置

Anti-Carding 必须在 acquirer 透传前完成。触发阈值后直接拒绝请求，保护下游 provider 和商户主体。

## 4. 模块全景

```text
cmd/
  api/                 HTTP API + Dashboard + Checkout
  migrate/             migration runner
  seed/                local mock seed
  worker/              outbox / payout / reconcile worker

internal/
  checkout/            checkout token, hosted page, charge handler
  acquirer/            card acquirer registry + stripe/nxtapay/mock
  adapter/             universal adapter registry and providers
  bankcollection/      bank collection inbound flow
  cryptopay/           crypto deposit flow
  ledger/              double-entry ledger and multi-asset domain
  dispute/             refund/chargeback reverse ledger
  treasury/            sweep/bridge policy and orchestration
  payout/              payout request/address/freeze
  risk/carding/        sliding-window anti-carding
  webhook/             webhook verification and consumer
  walletpolicy/        allowlisted wallet policy
  entity/              corporate entity isolation
  platform/            config, httpx, crypto, postgres, redis, stream
```

## 5. 数据模型主线

### 5.1 Acquirer 专用表

- `payment_provider`
- `tenant_acquirer_channel`
- `provider_credential_vault`

用途：兼容卡网收单路径、mock_default、本地 checkout。

### 5.2 Universal Adapter 表

- `asset_definition`
- `adapter_provider`
- `adapter_channel`
- `adapter_credential_vault`

用途：统一表达 card/bank/crypto/exchange/LP/wallet 等 adapter。

### 5.3 账本表

- `ledger_account`
- `ledger_transaction`
- `ledger_entry`
- `account_balance`

用途：复式账本、余额、资产表达。

### 5.4 资产桥表

- `bridge_profile`
- `bridge_operation`
- `bridge_daily_tracking`
- `asset_bridge_profile`
- `asset_bridge_job`
- `asset_bridge_outbox`

用途：资产调度、滑点保护、审批、异步执行。

### 5.5 风控与实体隔离表

- `risk_event`
- `corporate_entity`
- `entity_risk_profile`
- `entity_asset_limit`
- `entity_daily_flow`
- `transit_wallet_policy`

用途：风控事件、实体隔离、钱包白名单与限额。

## 6. 交易生命周期

### 6.1 Checkout Token

```text
CreateToken
  │
  ├─ order_id
  ├─ merchant_id
  ├─ allowed_origin
  ├─ max_uses
  └─ expires_at
```

local 默认 memory token store，也支持 Redis token store。

### 6.2 Charge

```text
Redeem Token
  │
  ▼
Load Order Details
  │
  ▼
Risk Gateway Check
  │
  ├─ blocked -> 429/403
  └─ passed
       ▼
Payment Gateway
       │
       ├─ mock -> success/failure/timeout
       └─ acquirer registry -> provider client
```

### 6.3 Webhook / Reconcile

```text
External Event
  │
  ▼
Webhook Receiver
  │
  ├─ verify signature
  ├─ idempotency check
  └─ persist event
       ▼
Consumer / Reconcile
       │
       └─ update ledger/state
```

## 7. Registry 与 Adapter

### 7.1 ChannelRegistry

职责：

- 加载 active acquirer channel。
- 创建 mock/stripe/nxtapay client。
- Redis Pub/Sub 接收 reload。
- real/prod 下 fail-closed。

### 7.2 UniversalAdapterRegistry

职责：

- 按 tenant、entity、adapter_type 管理 adapter channel。
- 支持 card/bank/crypto/exchange/LP/wallet。
- 提供统一 interface。
- local/mock 可演示，real/prod 不允许 silent mock。

### 7.3 Adapter Interface

主要接口族：

- `InboundAdapter`
- `BridgeAdapter`
- `ExchangeAdapter`
- `LPAdapter`
- `OnchainWalletAdapter`

这些接口把业务编排与外部 provider SDK 分离，后续接真实 provider 时只需要实现 adapter，不应改动账本核心。

## 8. 风控体系

当前 Anti-Carding 机制：

- buyer IP 阈值。
- app/merchant 阈值。
- card BIN 阈值。
- Redis 滑动窗口。
- 命中后不调用下游 acquirer。

风险事件进入：

- `/admin/risk/events`
- `/admin/risk/events/{id}`
- Dashboard 风控运维。

RiskHold 效果：

- 阻断 entity/channel/sweep。
- Dashboard 可设置与解除。
- 后续生产应增加审批流与审计备注。

## 9. 账本与争议处理

### 9.1 正向入账

卡网或其他入金确认后，应写入 merchant settled、platform fee、reserve 等账户。

### 9.2 退款

`ORDER_REFUND` 必须冲正原交易对应金额，不得只改订单状态。

### 9.3 拒付

`CHARGEBACK_RECEIVED` 处理：

- 扣减 merchant settled。
- settled 不足时进入 merchant negative balance。
- 增加 platform dispute reserve 或相关准备金账户。
- fee 单独分录。

### 9.4 拒付撤销

`CHARGEBACK_REVERSED` 必须按状态机反向恢复，避免重复恢复。

## 10. Asset Bridge

Asset Bridge 是资金调度的异步编排层。

核心参数：

- `buffer_percent`
- `max_slippage_bps`
- `batching_policy`
- `execution_policy`
- `withdrawal_policy`
- `compliance_policy`

核心状态：

- `CREATED`
- `PENDING_APPROVAL`
- `APPROVAL_REQUIRED`
- `EXECUTING`
- `UNKNOWN_EXTERNAL_STATE`
- `COMPLETED`
- `FAILED`

滑点规则：

- 默认最大 20 bps。
- 超过阈值进入审批，不自动执行。

## 11. Dashboard 技术状态

Dashboard 是当前本地预上线的主要验收面。

已完成：

- 全中文化。
- provider/channel 下拉管理。
- 风险冻结确认。
- 禁用操作确认。
- 风控事件详情 API。
- outbox 详情 API。
- 状态和类型中文翻译。
- Dashboard HTML 单元测试。

Dashboard 测试覆盖：

```text
TestDashboardHTMLNoIllegalTags
TestDashboardHTMLNoGarbledCharacters
TestDashboardHTMLContainsChineseLabels
TestDashboardHTMLNoEnglishLabels
TestDashboardHTMLContainsStatusTranslations
TestDashboardHTMLContainsTypeTranslations
TestDashboardHTMLContainsErrMsgFunction
TestDashboardHTMLContainsConfirmDialogs
TestDashboardHTMLNoDatabaseQueryHints
TestDashboardHTMLDetailAPICalls
```

## 12. 本地演示模式

本地演示依赖：

- Docker Postgres
- Docker Redis
- mock KMS
- mock acquirer
- memory token store

本地启动：

```bash
docker compose up -d postgres redis

set POSTGRES_DSN=postgres://nexus:nexus@localhost:5432/nexus?sslmode=disable
set MASTER_KEY_BASE64=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=
set JWT_SECRET=dev-jwt-secret
set INTERNAL_TOKEN=dev-internal-token
set APP_ENV=local
set EXTERNAL_MODE=mock
set KMS_MODE=mock
set ACQUIRER_MODE=mock

go run ./cmd/migrate up
go run ./cmd/seed
go run ./cmd/api
```

Dashboard：

```text
http://localhost:8080/admin/dashboard
```

该地址需要 Authorization header。为了人工浏览，可使用本机预览代理：

```text
http://127.0.0.1:8090/
```

## 13. 真实模式切换边界

真实模式不是只改一个环境变量。至少需要：

```text
APP_ENV=staging 或 production
EXTERNAL_MODE=real
KMS_MODE=real
ACQUIRER_MODE=real
VAULT_BASE_URL
VAULT_TOKEN
LP_BASE_URL
BANK_BASE_URL
MAE_BASE_URL
Stripe/Nxtapay credentials
Webhook secrets
```

real 模式强制：

- raw card 拒绝。
- payment_method_token 必填。
- KMS_MODE 必须 real。
- 缺 credential fail-closed。
- unknown provider fail-closed。
- production 不允许 mock external mode。

## 14. 测试策略

### 14.1 每轮必跑

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./... -short -count=1
```

### 14.2 管理后台变更必跑

```bash
go test ./internal/platform/httpx -count=1 -v
go test ./internal/admin -count=1 -v
```

### 14.3 真实外部凭证到位后必跑

```bash
go test ./test/integration -run "TestAcquirerSandbox_Real" -count=1 -v
```

## 15. 当前未完成项

P0，仅差外部环境：

- 配置 Stripe sandbox 凭证并跑通真实请求。
- 配置 Nxtapay sandbox 凭证并跑通真实请求。
- 验证真实 webhook 验签与反向入账。

P1，体验增强：

- Dashboard 增加独立登录页，避免首屏 Bearer header 问题。
- Dashboard 增加抽屉式详情，不再用 alert 展示 JSON。
- README 清理历史乱码与过期命令。
- 增加浏览器 E2E 截图测试。

P2，生产增强：

- 完整审批流。
- 细粒度 RBAC。
- 操作审计日志。
- provider credential rotation UI。
- 更完整的 real provider adapter 监控和告警。

## 16. 给后续 AI 的硬约束

1. 不得把 mock 逻辑写入 real/prod 分支。
2. 不得删除或绕开 Anti-Carding。
3. 不得绕开账本直接改余额。
4. 不得在 DB transaction 内调用外部 API。
5. 不得只写报告不跑命令。
6. 不得用 placeholder 测试冒充集成测试。
7. 不得把“本地 mock 100%”描述成“真实生产 100%”。

## 17. 最终状态声明

截至 2026-06-14：

- NexusGlobal 本地 mock/可视化/管理 API/账本/风控/adapter registry 预上线门禁通过。
- 系统已经具备清晰的本地演示、开发交接和下一阶段真实 sandbox 联调基础。
- 真实生产上线仍需外部 sandbox 凭证验证与生产级 KMS/凭证/运维配置。

