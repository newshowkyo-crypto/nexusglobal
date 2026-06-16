# NexusGlobal V2.0 重构与历史操作归档日志（001号）

记录日期：2026-06-14  
阶段：Phase 0 - 真实收单适配器启动前归档  
关联设计文件：

- `PROJECT_PROGRESS_SNAPSHOT_2026-06-14.md`
- `NEXUSGLOBAL_V2_TECHNICAL_WHITEPAPER.md`

---

## 1. 当前代码基线

### 1.1 技术栈基线

当前 NexusGlobal 是 Go 模块化单体项目，核心技术栈如下：

| 层级 | 技术 |
| --- | --- |
| Language | Go |
| HTTP Router | `github.com/go-chi/chi/v5` |
| Database | PostgreSQL |
| DB Driver | `github.com/jackc/pgx/v5` |
| SQL Codegen | sqlc |
| Cache / PubSub / Risk Window | Redis |
| Migration | `github.com/golang-migrate/migrate/v4` |
| Decimal | `github.com/shopspring/decimal` |
| Test | Go test + testify + testcontainers |
| Observability | slog + Prometheus + OpenTelemetry |

当前 `go.mod` 关键基线：

- `github.com/jackc/pgx/v5`
- `github.com/redis/go-redis/v9`
- `github.com/stretchr/testify`
- `github.com/testcontainers/testcontainers-go`
- `github.com/shopspring/decimal`

### 1.2 已完成工程骨架

当前系统已完成以下关键骨架：

- `internal/acquirer`
  - provider/channel/credential 管理。
  - `ChannelRegistry` 内存对象池。
  - Redis Pub/Sub 配置热重载。
  - Mock Acquirer 三态注入。

- `internal/checkout`
  - checkout token。
  - hosted checkout。
  - charge API。
  - `AcquirerGateway` 通过 `ChannelRegistry` 获取通道 client。
  - real mode 禁止 raw PAN/CVV。

- `internal/risk/carding`
  - Redis 滑动窗口前置拦截。
  - IP / App / BIN 多维失败计数。
  - 命中后不调用 acquirer。

- `internal/dispute`
  - refund。
  - chargeback received。
  - chargeback reversed。
  - platform dispute reserve ledger。
  - merchant negative balance。

- `internal/webhook`
  - webhook proxy / consumer 基础。
  - 幂等处理基础。
  - 后续需要接真实 provider webhook 验签和事件映射。

- `internal/ledger`
  - 双轨账本与 balance 校验。
  - 已新增 chargeback / dispute 相关 transaction/account type。

### 1.3 当前已通过验收

最近一次归档前已经通过：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./test/integration -run TestLocalDemo_FullFlow -count=1 -v
go test ./... -short -count=1
```

`TestLocalDemo_FullFlow` 已硬断言：

- `order_success` -> `CAPTURED`
- `order_fail` -> `ErrPaymentFailed`
- `order_timeout` -> `ErrPaymentTimeout`

结论：

- Mock / Local Demo：100% 完成。
- ChannelRegistry 主路径：已完成。
- Dashboard / Admin Acquirer：已基本完成。
- Real acquirer adapter：尚未完成。

---

## 2. 本次重构核心动机

Phase 0 的目标是结束“只有 mock/local 可演示”的状态，进入真实 sandbox provider 联调。

核心动机：

1. 剥离真实分支中的任何 silent mock。
2. 保持现有 `ChannelRegistry`、Anti-Carding、real mode fail-closed 边界不被破坏。
3. 将真实 Stripe / Nxtapay API 调用纳入可审计、可重试、可幂等、可对账的 Outbox 异步链路。
4. 用 Contract Tests 和 Sandbox Integration Tests 代替文字汇报。
5. 为后续真实 webhook、dispute、refund、reconcile 打下 provider adapter contract。

---

## 3. 变更边界

### 3.1 预计新增文件

Phase 0 预计新增：

```text
internal/acquirer/adapter/stripe.go
internal/acquirer/adapter/stripe_test.go
internal/acquirer/adapter/nxtapay.go
internal/acquirer/adapter/nxtapay_test.go
internal/acquirer/adapter/errors.go
internal/acquirer/adapter/http_client.go
internal/acquirer/outbox/domain.go
internal/acquirer/outbox/repository.go
internal/acquirer/outbox/worker.go
internal/acquirer/outbox/worker_test.go
internal/webhook/provider/stripe.go
internal/webhook/provider/stripe_test.go
internal/webhook/provider/nxtapay.go
internal/webhook/provider/nxtapay_test.go
test/integration/acquirer_sandbox_test.go
migrations/000018_init_acquirer_outbox.up.sql
migrations/000018_init_acquirer_outbox.down.sql
```

如果执行端认为需要调整路径，必须先在报告中说明原因，不能静默改名或绕过既有模块。

### 3.2 预计修改文件

Phase 0 预计修改：

```text
go.mod
go.sum
internal/acquirer/domain/domain.go
internal/acquirer/registry/registry.go
internal/acquirer/repository/repository.go
internal/checkout/acquirer_gateway.go
internal/checkout/handler.go
internal/webhook/handler/handler.go
internal/webhook/consumer/consumer.go
cmd/api/main.go
cmd/worker/main.go
README.md
```

允许的修改范围：

- 增加真实 provider factory。
- 增加 outbox enqueue / worker。
- 增加 provider webhook verifier。
- 增加错误映射。
- 增加 integration test。
- 增加配置项。

禁止的修改范围：

- 删除或绕过 `ChannelRegistry`。
- 删除或绕过 `riskcarding.Gateway`。
- 在 real/sandbox 分支静默回退 mock。
- 在 PG 事务中执行 Stripe/Nxtapay HTTP 请求。
- 删除现有 mock/local demo 的三态测试。
- 删除 dispute / ledger 的事务一致性测试。

### 3.3 结构体变更建议

建议扩展 `internal/acquirer/domain`：

```text
AuthorizeRequest
  MerchantID
  OrderID
  Amount
  Currency
  PaymentMethodToken
  ReturnURL
  Metadata
  IdempotencyKey

AuthorizeResponse
  ProviderReference
  Status
  RequiresAction
  RedirectURL
  RawStatus

CaptureRequest
  ProviderReference
  Amount
  Currency
  IdempotencyKey

RefundRequest
  ProviderReference
  Amount
  Currency
  Reason
  IdempotencyKey

ProviderWebhookEvent
  Provider
  EventID
  EventType
  ProviderReference
  OrderID
  Amount
  Currency
  OccurredAt
  RawPayloadHash
```

建议新增统一错误：

```text
ErrPaymentDeclined
ErrPaymentRequiresAction
ErrProviderTimeout
ErrProviderRateLimited
ErrProviderAuthFailed
ErrProviderInvalidRequest
ErrUnknownExternalState
ErrWebhookSignatureInvalid
ErrWebhookReplay
```

### 3.4 Outbox 表建议

新增 `acquirer_outbox`：

| 字段 | 说明 |
| --- | --- |
| `id` | UUID |
| `tenant_id` | 租户 |
| `merchant_id` | 商户 |
| `channel_id` | 通道 |
| `operation` | AUTHORIZE / CAPTURE / REFUND / QUERY |
| `idempotency_key` | 外部请求幂等键 |
| `payload` | JSONB |
| `status` | PENDING / SENDING / SENT / SUCCEEDED / FAILED / UNKNOWN_EXTERNAL_STATE |
| `external_ref` | provider reference |
| `attempt_count` | 重试次数 |
| `next_attempt_at` | 下次重试 |
| `last_error_code` | 最后错误码 |
| `last_error_message` | 最后错误 |
| `created_at / updated_at` | 时间 |

核心约束：

- `(operation, idempotency_key)` 唯一。
- 外部 HTTP 写请求必须由 worker 消费 outbox 后执行。
- API handler 只能创建本地意图和 outbox，不能在 DB transaction 内调用外部 provider。

---

## 4. 当前生产安全红线

1. Real/Sandbox 分支不允许 silent mock。
2. Mock 只能存在于 `mock/local/test` 模式。
3. Real mode 不允许 raw PAN/CVV。
4. ChannelRegistry 是唯一通道客户端入口。
5. Anti-Carding 是 public/checkout 入口前置硬边界。
6. 任何外部状态未知必须进入 `UnknownExternalState`，不得重复提交写请求。
7. 所有 provider webhook 必须验签、幂等、可回放测试。
8. 账本分录不平衡时，后续资金流转必须 fail-closed。

---

## 5. Phase 0 完成判定

Phase 0 不能用文字报告判定完成，必须满足：

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

并额外提供：

- 所有新增 public/provider 状态映射表。
- 所有 webhook fixture。
- 所有 outbox 状态流转测试。
- 明确说明哪些测试使用 sandbox credentials，哪些测试使用 `httptest.Server` 模拟外部 provider。
- 明确说明未提供真实 sandbox secret 时，integration test 如何 skip，而不是 mock success。
