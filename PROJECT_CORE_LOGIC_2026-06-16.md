# NexusGlobal 3.0 核心逻辑说明

生成时间：2026-06-16  
目标读者：工程、测试、运营、合规复核人员

## 一、系统核心原则

NexusGlobal 3.0 的核心原则是：服务商可替换、风险分层可配置、资金链路可审计、生产环境 fail-closed。

系统不应把 Stripe、Nxtapay、Bridgecard、Maplerad 等具体服务商写成业务分支。代码只识别抽象层级、接口契约、配置与状态机；具体 provider 通过 registry、factory、credential vault key 和 dashboard 配置接入。

## 二、请求入口逻辑

### 1. Checkout 请求

```text
Client / Merchant
  -> Checkout API
  -> Order Source Provider
  -> Acquirer Gateway
  -> AcquirerAdapter
  -> Provider Sandbox/Production API
  -> Ledger / Webhook / MAE
```

关键逻辑：

- local 可以使用 mock 订单源。
- production 禁止 mock 订单源。
- real acquirer mode 禁止原始卡信息直传，必须使用 token/payment method。
- provider 超时、签名失败、未知 provider、缺凭证全部 fail-closed。

### 2. Webhook 请求

```text
Provider Webhook
  -> VerifyWebhook
  -> Idempotency / Duplicate Detection
  -> Domain Event
  -> Ledger / MAE / Risk / Audit
```

关键逻辑：

- 签名校验失败拒绝处理。
- webhook 重放通过幂等键/事件 ID 去重。
- 状态变化必须落审计。
- 不允许把外部 provider 的不可信状态直接写成最终资金状态。

## 三、收单抽象逻辑

统一接口：

```go
type AcquirerAdapter interface {
    Authorize(ctx context.Context, req AuthorizeRequest) (*AuthorizeResult, error)
    Capture(ctx context.Context, req CaptureRequest) (*CaptureResult, error)
    Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
    VerifyWebhook(ctx context.Context, req WebhookVerifyRequest) (*WebhookEvent, error)
}
```

路由逻辑：

```text
provider_tier + provider_code + channel_code
  -> registry lookup
  -> credential_vault_key decrypt/read
  -> adapter factory
  -> provider API
```

Fail-closed 边界：

- provider_code 没有注册 factory：拒绝。
- credential_vault_key 为空：拒绝。
- production 使用 mock provider：启动失败。
- webhook secret 缺失：生产启动失败或 webhook 拒绝。
- BURN_MODE：进入人工审查/锁定语义，不自动放行。

## 四、发卡抽象逻辑

统一接口：

```go
type IssuingAdapter interface {
    CreateCard(ctx context.Context, req CreateCardRequest) (*CreateCardResult, error)
    FreezeCard(ctx context.Context, req FreezeCardRequest) (*FreezeCardResult, error)
    TerminateCard(ctx context.Context, req TerminateCardRequest) (*TerminateCardResult, error)
    ProcessAuthWebhook(ctx context.Context, req AuthWebhookRequest) (*AuthWebhookResult, error)
}
```

发卡池配置：

```text
issuing_tier:
  TIER_1_AGGREGATOR
  TIER_2_DIRECT

funding_mode:
  CRYPTO_DESENSITIZED
  FIAT_DIRECT
```

关键逻辑：

- 发卡 provider 通过卡池配置接入，不在业务代码中硬编码。
- 发卡池凭证只引用 Vault key。
- 卡片 declined_count_sliding_window 超阈值时，商户开卡权限自动冻结。
- 卡片冻结、销毁、授权 webhook 必须进入审计和状态机。

## 五、VCC Funding 逻辑

目标：把商户可用结算余额安全、可审计地转入 VCC 可用余额。

核心流程：

```text
Merchant Settled Balance
  -> VCC Funding Request
  -> Tier/Funding Mode Check
  -> Ledger Double Entry
  -> Worker Execution
  -> VCC Available Balance
```

复式记账：

```text
DEBIT  merchant settled/acquiring balance
CREDIT merchant vcc available balance
```

Fail-closed 边界：

- `TIER_1_HIGH_RISK` 收单资金不得直连 `FIAT_DIRECT` 发卡池。
- 高风险资金进入发卡侧必须走合规隔离 funding mode，并保留完整资金来源链路。
- worker 失败不得产生半成品余额。
- ledger posting 必须幂等。

合规说明：

`CRYPTO_DESENSITIZED` 在本项目中只表示合规隔离 funding mode，不表示删除来源、隐藏来源或规避 AML/KYC。所有来源、转换、审批、审计必须保留。

## 六、MAE 与风控逻辑

MAE 是后场状态机与资金编排层，负责把交易、清算、风控、审计状态串起来。

典型状态：

```text
CREATED
AUTHORIZED
CAPTURED
SETTLED
FUNDING_PENDING
FUNDING_BLOCKED
MANUAL_REVIEW
COMPLETED
FAILED
```

风控触发：

- 通道风险策略。
- 商户风险 profile。
- carding/fraud guard。
- RFI 审核状态。
- 隔离组冻结状态。
- override audit log。

处理原则：

- 风控不明确时 fail-closed。
- 人工审核未完成不得自动释放。
- 任何 override 必须有 operator、reason、expires_at 或明确永久标记。

## 七、Dashboard 运营逻辑

Dashboard 的目标是“运营人员不改代码完成配置和观察”。

已具备入口：

- 收单通道管理。
- 发卡池管理。
- 风险配置。
- 商户保证金。
- 费用分摊。
- 隔离组冻结/解冻。
- RFI 审批/拒绝。
- override audit logs。

运营动作：

```text
新增 provider/channel/pool
  -> 填写 tier / funding mode / vault key
  -> 保存
  -> API 写库
  -> Redis Pub/Sub 通知 reload
  -> registry/service 热加载
```

危险操作：

- 禁用通道。
- 禁用卡池。
- 冻结隔离组。
- 解冻隔离组。
- RFI approve/reject。
- reload 配置。

这些操作都应有中文提示、确认框、失败原因、审计记录。

## 八、Production 配置逻辑

生产环境启动时必须拒绝：

- `KMS_MODE=mock`
- `EXTERNAL_MODE=mock`
- `ACQUIRER_MODE=mock`
- `ACQUIRER_PROVIDER=mock`
- `ISSUING_PROVIDER=mock`
- `VCC_FUNDING_PROVIDER=mock`
- `CHECKOUT_ORDER_PROVIDER=mock`
- 空或过短 `JWT_SECRET`
- 空或过短 `INTERNAL_TOKEN`
- 空或过短 `ADMIN_SESSION_SECRET`
- `REQUIRE_VAULT=true` 但缺 Vault 配置
- 非法 `DB_MIGRATION_MODE`

local 环境允许 mock，用于开发和集成测试。

## 九、真实 Sandbox 验收逻辑

当前状态：

```text
Stripe:  SKIPPED_MISSING_CREDENTIAL
Nxtapay: SKIPPED_MISSING_CREDENTIAL
VCC:     SKIPPED_MISSING_CREDENTIAL
```

凭证补齐后必须验证：

- authorize/capture/refund。
- webhook signature。
- timeout/retry/fail-closed。
- credential 不回显、不入库、不进日志。
- 发卡 create/freeze/terminate/auth webhook。

## 十、故障处理逻辑

常见故障与系统行为：

```text
Provider timeout         -> fail-closed / retry policy / audit
Unknown provider         -> reject
Missing vault key        -> reject
Invalid webhook          -> reject
Risk threshold exceeded  -> freeze / manual review
Ledger post failed       -> no partial balance
Worker crash             -> outbox / retry / idempotency
Redis unavailable        -> production fail-closed where applicable
```

