# NexusGlobal 2.0 重构记录与 Trae 搬砖白皮书：Phase 0 真实收单适配器

适用对象：Trae  
阶段目标：实现 Stripe & Nxtapay Sandbox 真实收单适配器、真实 webhook 验签接收器、沙箱集成破坏性测试。  
验收原则：只认代码、测试、日志和可复现实验，不认文字自述。

---

## 0. 致 Trae 监工红线：四大不准原则

### 第一，不准使用任何形式的 Silent Mock

所有 Stripe/Nxtapay Sandbox 适配器必须使用真实外部 HTTP 请求或官方 SDK 调用。严禁在 sandbox/real/provider 分支写出以下欺诈代码：

- 硬编码返回 success。
- 捕获错误后返回 nil。
- timeout 后伪造 CAPTURED。
- provider 未配置时自动回退 mock。
- 测试 PASS 但主路径没有真实调用。

Mock 只能存在于明确的 `mock/local/test` 分支，且必须由配置显式启用。

### 第二，不准破坏已有核心防线

修改 `internal/checkout/`、`cmd/api/main.go`、路由或 provider factory 时，严禁删除、注释或绕过：

- `ChannelRegistry` 内存对象池。
- Redis Pub/Sub 热重载。
- Redis 滑动窗口 Anti-Carding 网关。
- real mode 禁止 raw PAN/CVV 的边界。
- 现有 mock/local demo 三态测试。
- dispute / ledger 事务一致性测试。

任何涉及这些边界的修改都必须在报告中逐项说明。

### 第三，不准在数据库事务内内嵌外部网络 I/O

Stripe/Nxtapay 真实 API 请求必须移出 PG 事务。

正确模式：

```text
API Handler / Service
  -> DB transaction creates local payment intent + outbox event
  -> commit
  -> outbox worker sends provider HTTP request
  -> worker persists provider result
  -> webhook/reconcile confirms final state
```

错误模式：

```text
BEGIN;
INSERT local payment;
CALL Stripe/Nxtapay HTTP;
INSERT ledger;
COMMIT;
```

该模式禁止。外部网络 I/O 会造成事务长时间占用、锁等待、重复提交和未知外部状态。

### 第四，必须产出 100% 覆盖率的 Contract/Integration Tests

任务交付标准不是“我已经完成”的文字报告，而是测试绿灯。

Phase 0 所有新增 adapter surface 必须有：

- Contract tests。
- Error mapping tests。
- Timeout tests。
- Webhook signature tests。
- Idempotency tests。
- Outbox state transition tests。
- Sandbox integration tests，缺少真实 secret 时必须显式 skip，不能 silent mock。

---

## 1. 总体验收命令

完成后必须贴出完整命令和结果：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/acquirer/... -count=1 -v
go test ./internal/webhook/... -count=1 -v
go test ./internal/checkout/... -count=1 -v
go test ./test/integration -run TestAcquirerSandbox -count=1 -v
go test ./... -short -count=1
```

任何失败、skip、flaky、环境变量缺失都必须原样说明。

---

## 2. Task 0.1：Stripe Sandbox 驱动实现

### 涉及文件路径

必须新增：

```text
internal/acquirer/adapter/stripe.go
internal/acquirer/adapter/stripe_test.go
```

可能修改：

```text
go.mod
go.sum
internal/acquirer/domain/domain.go
internal/acquirer/registry/registry.go
internal/checkout/acquirer_gateway.go
internal/checkout/handler.go
internal/platform/config/config.go
```

### 核心逻辑规范

1. Provider factory

在 `ChannelRegistry.createClient()` 中支持 `provider_code = stripe`。

要求：

- 从 credential vault 读取 Stripe secret key。
- 从 channel config 读取 Stripe account / api version / timeout / capture mode。
- client 初始化后放入 `ClientBundle`。
- 热路径 `Get(channelID)` 仍只读内存。
- 不允许每次交易重新解密、重新创建 HTTP client。

2. Stripe SDK / HTTP Client 单例复用

要求：

- Stripe client 或底层 HTTP client 必须在 channel reload 时创建。
- 同一 channel 的 client 必须复用连接池。
- timeout 必须可配置。
- 不允许在每次 Authorize/Capture 里创建新的无复用 client。

3. `payment_method_token` 传参

真实模式只接受 token：

```text
AuthorizeRequest.PaymentMethodToken
```

要求：

- 如果 real/sandbox mode 下缺少 token，返回 `ErrPaymentMethodTokenRequired` 或既有等价错误。
- 如果请求带 raw PAN/CVV，返回 `ErrRawCardNotAllowed`。
- 不允许把 raw card 字段传入 Stripe。

4. Authorize / Capture

按内部接口实现：

```text
Authorize(ctx, req)
Capture(ctx, req)
Refund(ctx, req)
QueryPayment(ctx, req)
VerifyWebhook(ctx, req)
```

Stripe 推荐映射：

- authorize：创建 PaymentIntent，`capture_method=manual`。
- capture：capture PaymentIntent。
- refund：创建 Refund。
- query：查询 PaymentIntent。

5. 3D Secure 重定向路由

Stripe 返回 requires action 时：

- 内部状态映射为 `REQUIRES_ACTION`。
- `AuthorizeResponse.RequiresAction = true`。
- 填充 `RedirectURL` 或 next_action 信息。
- checkout API 不得把该状态误判为失败或成功。

需要在 checkout handler 中保留可扩展响应字段：

```json
{
  "status": "REQUIRES_ACTION",
  "requires_action": true,
  "redirect_url": "..."
}
```

6. API 异常映射

必须统一映射：

| Stripe 类别 | 内部错误 |
| --- | --- |
| card declined | `ErrPaymentDeclined` |
| authentication required | `ErrPaymentRequiresAction` |
| rate limit | `ErrProviderRateLimited` |
| invalid request | `ErrProviderInvalidRequest` |
| auth failed | `ErrProviderAuthFailed` |
| network timeout | `ErrUnknownExternalState` |
| 5xx / unknown body | `ErrUnknownExternalState` |

错误映射必须有单元测试。

### 异常 Fail-Closed 边界

- Stripe secret key 缺失：client 创建失败，channel 不可用。
- Stripe secret key 解密失败：client 创建失败，channel 不可用。
- PaymentMethodToken 缺失：拒绝请求，不调用 Stripe。
- raw PAN/CVV 出现在 real/sandbox：拒绝请求，不调用 Stripe。
- Stripe timeout：进入 `UnknownExternalState`，不得重复创建 PaymentIntent。
- Stripe 返回 requires action：不得 capture，必须交给前端完成 3DS。
- Stripe webhook 验签失败：拒绝事件。

### 拒绝虚报的提问 / 审计 checklist

Trae 必须逐项回答：

- `stripe.go` 中是否存在硬编码 success？给出搜索结果。
- Stripe client 是否在 registry reload 时创建，而不是每次请求创建？
- 缺少 token 时是否有测试证明不会调用 Stripe？
- raw PAN/CVV 在 real/sandbox 下是否被拒绝？
- timeout 是否映射为 `UnknownExternalState`？
- 3DS `requires_action` 是否有单元测试？
- provider factory 是否没有破坏 mock/local 三态？
- 是否新增 Stripe webhook fixture？

---

## 3. Task 0.2：Nxtapay Sandbox 驱动实现

### 涉及文件路径

必须新增：

```text
internal/acquirer/adapter/nxtapay.go
internal/acquirer/adapter/nxtapay_test.go
```

可能修改：

```text
internal/acquirer/domain/domain.go
internal/acquirer/registry/registry.go
internal/platform/config/config.go
go.mod
go.sum
```

### 核心逻辑规范

1. Provider factory

在 `ChannelRegistry.createClient()` 中支持：

```text
provider_code = nxtapay
```

要求：

- 从 credential vault 读取 API key / API secret / merchant id。
- 从 channel config 读取 base_url / timeout / capture mode。
- sandbox base URL 和 production base URL 必须由配置区分。
- 未配置 base URL 时 fail-closed。

2. Authorize/Capture 二阶段请求

内部接口：

```text
Authorize(ctx, req)
Capture(ctx, req)
```

Authorize 要求：

- 使用 `payment_method_token` 或 Nxtapay sandbox 支持的 tokenized method。
- 传入 order id、amount、currency、return_url、metadata。
- 带 idempotency key。
- 不得传 raw PAN/CVV。

Capture 要求：

- 使用 authorize 返回的 provider reference。
- 传入 capture amount。
- 带 idempotency key。
- 可处理 partial capture，但默认先实现 full capture。

3. 签名生成逻辑

必须实现独立签名函数：

```text
SignNxtapayRequest(secret, method, path, timestamp, nonce, bodyHash) string
```

测试必须覆盖：

- 固定输入生成固定签名。
- body 改变签名改变。
- timestamp 改变签名改变。
- secret 改变签名改变。

请求头建议：

```text
X-Nxtapay-Key
X-Nxtapay-Timestamp
X-Nxtapay-Nonce
X-Nxtapay-Signature
Idempotency-Key
```

如 Nxtapay 官方文档字段不同，以官方 sandbox 文档为准，但必须在报告中说明差异。

4. HTTP Client

要求：

- 可注入 base URL，便于 `httptest.Server` contract test。
- 可配置 timeout。
- 复用连接池。
- 所有请求记录 provider reference 和 raw status。
- 不记录 secret，不记录 payment method 敏感数据。

5. 错误映射

必须统一映射：

| Nxtapay 响应 | 内部错误 |
| --- | --- |
| declined | `ErrPaymentDeclined` |
| pending_action | `ErrPaymentRequiresAction` |
| timeout / network error | `ErrUnknownExternalState` |
| 401 / signature invalid | `ErrProviderAuthFailed` |
| 429 | `ErrProviderRateLimited` |
| 4xx invalid request | `ErrProviderInvalidRequest` |
| 5xx | `ErrUnknownExternalState` |

### 异常 Fail-Closed 边界

- API key/secret 缺失：client 创建失败。
- base URL 缺失：client 创建失败。
- 签名生成失败：不发送请求。
- payment_method_token 缺失：不发送请求。
- raw PAN/CVV：拒绝请求。
- HTTP timeout：`UnknownExternalState`，进入 query/reconcile。
- response body 无法解析：`UnknownExternalState`。
- provider reference 缺失：不得进入 captured。

### 拒绝虚报的提问 / 审计 checklist

Trae 必须逐项回答：

- `nxtapay.go` 是否存在硬编码 success？
- 签名函数是否有固定向量测试？
- `httptest.Server` 是否验证了真实 HTTP 请求路径、header、body？
- timeout 是否不会被映射成失败或成功？
- 是否支持 idempotency key？
- Authorize/Capture 是否分成两个真实请求？
- 是否没有在 DB transaction 内调用 Nxtapay？
- 是否没有把 secret 写进日志？

---

## 4. Task 0.3：真实 Webhook 安全验签接收器

### 涉及文件路径

必须新增：

```text
internal/webhook/provider/stripe.go
internal/webhook/provider/stripe_test.go
internal/webhook/provider/nxtapay.go
internal/webhook/provider/nxtapay_test.go
```

可能修改：

```text
internal/webhook/handler/handler.go
internal/webhook/consumer/consumer.go
internal/webhook/domain/domain.go
internal/webhook/repository/repository.go
cmd/api/main.go
migrations/000019_provider_webhook_events.up.sql
migrations/000019_provider_webhook_events.down.sql
```

### 核心逻辑规范

1. Provider webhook endpoint

建议新增或扩展：

```text
POST /webhooks/provider/stripe/{channel_id}
POST /webhooks/provider/nxtapay/{channel_id}
```

要求：

- 根据 channel_id 找到 provider credential。
- 获取 webhook secret。
- 验签通过后才解析事件。
- 原始 payload hash 入库。
- provider event id 做幂等键。

2. Stripe-Signature 真实验签

必须实现真实 Stripe-Signature 验签。

要求：

- 读取原始 body bytes。
- 使用 channel credential 中的 webhook secret。
- 校验 timestamp tolerance。
- 校验签名。
- 验签失败返回 400/401。
- 验签失败不得入账。
- 验签失败不得进入 consumer。

3. Nxtapay webhook 验签

按官方 sandbox 文档实现。

最低要求：

- timestamp。
- nonce。
- body hash。
- HMAC signature。
- replay protection。

4. 事件幂等反向映射入账

Provider webhook 事件映射到内部事件：

| Provider Event | Internal Event |
| --- | --- |
| payment_intent.succeeded / payment captured | PaymentCaptured |
| payment_intent.payment_failed | PaymentFailed |
| charge.refunded / refund.succeeded | RefundConfirmed |
| charge.dispute.created | ChargebackReceived |
| charge.dispute.closed won/reversed | ChargebackReversed |

要求：

- provider event id 唯一。
- 重复 webhook 返回 200 但不重复入账。
- 账本分录幂等。
- dispute 事件调用 `internal/dispute` 服务，不得绕过。
- refund 事件必须生成逆向分录。

5. Webhook 原始数据

允许保存：

- provider。
- event id。
- event type。
- channel id。
- payload hash。
- occurred_at。
- processing status。
- error code。

禁止保存：

- raw card。
- CVV。
- 明文 secret。
- 不必要的敏感 payment method 明细。

### 异常 Fail-Closed 边界

- channel_id 无效：拒绝。
- channel disabled：允许历史 webhook 验签和对账，但不得用于新交易。
- webhook secret 缺失：拒绝。
- 签名失败：拒绝，不入账。
- replay detected：返回 200 或 409 按策略，但不得重复处理。
- 事件类型未知：记录为 ignored，不入账。
- 金额/币种与本地订单不匹配：进入 `ApprovalRequired` 或 `ReconcileMismatch`。
- ledger 写入失败：事件保持 pending/failed，可重试。

### 拒绝虚报的提问 / 审计 checklist

Trae 必须逐项回答：

- 是否使用 raw body 做验签？
- Stripe-Signature 是否使用真实算法而非字符串 contains？
- 验签失败是否有测试证明不会入账？
- 重复 event id 是否有测试证明不会重复账本分录？
- refund/chargeback 是否调用现有 dispute/ledger 服务？
- channel disabled 下历史 webhook 是否仍可对账？
- payload 是否只保存 hash，不保存敏感原文？
- 是否有 webhook fixture？

---

## 5. Task 0.4：沙箱集成破坏性测试

### 涉及文件路径

必须新增：

```text
test/integration/acquirer_sandbox_test.go
```

可能新增：

```text
test/fixtures/stripe/
test/fixtures/nxtapay/
```

可能修改：

```text
README.md
Makefile
```

### 核心逻辑规范

1. 测试分层

必须区分两类测试：

```text
Contract Tests:
  - 使用 httptest.Server
  - 不依赖真实 secret
  - 验证请求路径、header、body、错误映射、timeout

Sandbox Integration Tests:
  - 使用真实 Stripe/Nxtapay sandbox credentials
  - 缺少 secret 时显式 t.Skip
  - 不允许自动回退 mock
```

2. 三态集成测试

必须覆盖：

```text
TestAcquirerSandbox_StripeSuccess
TestAcquirerSandbox_StripeDeclined
TestAcquirerSandbox_StripeTimeoutUnknownState
TestAcquirerSandbox_NxtapaySuccess
TestAcquirerSandbox_NxtapayDeclined
TestAcquirerSandbox_NxtapayTimeoutUnknownState
```

如果 Nxtapay sandbox 暂时不可用，必须先完成 `httptest.Server` contract tests，并在 sandbox test 中明确 skip 原因。

3. 真实模拟网络超时

Contract test 必须用 `httptest.Server` 制造：

- response delay > client timeout。
- connection close。
- malformed JSON。
- 500 response。

断言：

- timeout -> `ErrUnknownExternalState`。
- malformed response -> `ErrUnknownExternalState`。
- 500 -> `ErrUnknownExternalState`。
- 不得返回 success。
- 不得重复提交第二次外部写请求。

4. 扣款失败

必须覆盖：

- provider declined。
- insufficient funds / card declined 等等价失败。
- 内部错误映射为 `ErrPaymentDeclined` 或 `ErrPaymentFailed`。
- risk failure counter 被正确记录。
- ledger 不生成 captured 分录。

5. 扣款成功

必须覆盖：

- authorize 成功。
- capture 成功。
- provider reference 入库或返回。
- ledger 生成 captured / receivable 等预期分录。
- webhook 重放不重复入账。

6. 账本分录幂等性

必须断言：

- 同一个 provider event id 重放两次，账本分录数量不增加。
- 同一个 idempotency key 重试，不重复创建 payment。
- chargeback webhook 重放，不重复扣减 settled balance。
- refund webhook 重放，不重复冲正。

### 异常 Fail-Closed 边界

- 缺少 sandbox secret：integration test 必须 `t.Skip("missing STRIPE_SANDBOX_SECRET")`，不得自动 mock success。
- sandbox provider 不可用：标记外部不可用，不得修改测试让它假过。
- timeout：只能进入 unknown state。
- ledger mismatch：测试必须失败。
- duplicate ledger entry：测试必须失败。

### 拒绝虚报的提问 / 审计 checklist

Trae 必须逐项回答：

- 哪些测试是真 sandbox？哪些是 httptest contract？
- 缺少 sandbox secret 时是否显式 skip？
- 是否存在任何 mock success 伪装 sandbox？
- timeout 是否真实由 server delay 触发？
- 是否断言 provider 请求次数？
- 是否断言 ledger entry 数量？
- 是否断言 webhook 重放不重复入账？
- 是否保留 `TestLocalDemo_FullFlow`？

---

## 6. 最终交付报告模板

Trae 完成后必须按以下格式报告，缺一项视为未完成：

```text
## Phase 0 完成报告

### 1. 修改文件列表
- 新增：
- 修改：
- 删除：

### 2. 是否触碰核心防线
- ChannelRegistry：是否保留，证据：
- Anti-Carding：是否保留，证据：
- real mode raw PAN/CVV 禁止：是否保留，证据：
- mock/local 三态：是否保留，证据：

### 3. Provider Adapter 说明
- Stripe Authorize/Capture/Refund/Query：
- Nxtapay Authorize/Capture/Refund/Query：
- 是否存在 silent mock：否，证据：

### 4. Outbox 说明
- 是否有 DB transaction 内外部 I/O：否，证据：
- outbox 状态机：
- UnknownExternalState 处理：

### 5. Webhook 说明
- Stripe-Signature 验签：
- Nxtapay 验签：
- 幂等键：
- 重放测试：

### 6. 测试结果
粘贴完整命令输出摘要：

### 7. 未完成项
必须如实列出，不得写“无”除非所有验收命令和 checklist 全部满足。
```

---

## 7. 总判定

Phase 0 的唯一目标是：真实 sandbox provider adapter 可调用、可失败、可超时、可验签、可幂等、可对账。

不要求本阶段完成生产出金、交易所、银行清算或完整多租户路由；但任何为了赶进度而破坏现有 mock/local、ChannelRegistry、Anti-Carding、Dispute Ledger、real mode fail-closed 的行为，都必须视为阻断级失败。
