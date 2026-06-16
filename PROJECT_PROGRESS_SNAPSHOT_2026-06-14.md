# NexusGlobal 项目进度快照

更新时间：2026-06-14 15:20  
快照目的：固化当前项目总体框架、功能完成度、mock / 本地演示状态、真实通道切换边界与后续待办，避免后续迭代遗忘当前进度点。

---

## 1. 项目定位

NexusGlobal 当前是一个 Go 语言实现的跨境支付、收单、账本、清结算与管理后台系统。当前代码形态为模块化单体，核心依赖 PostgreSQL、Redis、pgx/v5、sqlc，并以本地 mock 模式作为开发、演示和测试闭环。

当前项目已经从早期 MAE、账本、资金归集、payout、webhook 等基础模块，扩展到以下关键能力：

- 动态收单通道注册表 `ChannelRegistry`
- 本地 mock acquirer 三态演示
- checkout 收银台通过通道注册表发起支付
- 管理后台可视化通道管理
- Redis 滑动窗口前置风控
- 拒付、退款、逆向账本与风险准备金
- real mode fail-closed 边界

当前必须明确：项目已经具备完整 mock / local demo 闭环，但真实 Stripe / Nxtapay adapter 仍未完成，不能把当前状态误判为真实生产收单 100%。

---

## 2. 总体架构

### 2.1 运行入口

- `cmd/api/main.go`：API 服务入口，负责装配 checkout、ledger、treasury、payout、MAE、webhook、risk、dispute、acquirer 等模块。
- `cmd/worker`：异步 worker 入口，承载 outbox、reconcile、payout、fxlock 等后台任务。
- `cmd/migrate`：数据库 migration 执行入口。
- `cmd/seed/main.go`：本地 mock acquirer 种子数据入口，用于创建 mock provider、mock channel 与 credential vault 数据。

### 2.2 核心模块

| 模块 | 路径 | 当前职责 |
| --- | --- | --- |
| Checkout | `internal/checkout` | checkout token、托管收银台、charge API、支付网关适配 |
| Acquirer | `internal/acquirer` | provider/channel/credential 管理、客户端注册表、mock acquirer |
| Risk/Carding | `internal/risk/carding` | Redis 滑动窗口测卡拦截、失败率限制、cooldown |
| Dispute | `internal/dispute` | refund、chargeback、chargeback reversed、风险准备金与负余额 |
| Ledger | `internal/ledger` | 双轨账本、账户余额、交易分录、账务校验 |
| MAE | `internal/mae` | MAE 配置、状态切换、密钥轮换、指标 |
| Treasury | `internal/treasury` | 资金归集、清算、reconcile 辅助能力 |
| Payout | `internal/payout` | 出金地址、出金批次、余额冻结与释放 |
| Webhook | `internal/webhook` | webhook proxy、consumer、幂等处理 |
| Platform | `internal/platform` | config、crypto、logger、postgres、redis、idempotency、dashboard |

### 2.3 数据库迁移

当前 migration 已包含：

- `000001` - MAE 初始化
- `000002` - Webhook 初始化
- `000003` - Treasury 初始化
- `000004` - Ledger 初始化
- `000005` - Payout 初始化
- `000006` - Outbox 初始化
- `000007` - Bank balance
- `000008` - Webhook proxy 扩展字段
- `000009` - Reserve batch
- `000010` - Webhook proxy 额外字段
- `000011` - Sweep approval
- `000012` - Check constraints
- `000013` - Reconcile enhancements
- `000014` - Idempotency payload hash
- `000015` - Dispute / reverse ledger
- `000016` - Risk event / carding
- `000017` - Acquirer registry

其中 `000017_init_acquirer_registry` 是当前 mock / 通道管理闭环的关键 migration，包含：

- `payment_provider`
- `tenant_acquirer_channel`
- `provider_credential_vault`

---

## 3. 当前已完成功能

### 3.1 Mock / 本地演示闭环

完成度：100%

当前本地 mock 模式已经不是“测试假通过”，而是通过真实 migration、真实 seed、真实 ChannelRegistry、真实 AcquirerGateway 完成闭环。

已完成能力：

- `cmd/seed/main.go` 可创建 mock provider 与 mock channel。
- seed 使用真实表名：
  - `payment_provider`
  - `tenant_acquirer_channel`
  - `provider_credential_vault`
- seed 与 migration 字段已对齐。
- mock channel 的 `channel_config` 包含三态场景：
  - `order_success` -> success
  - `order_fail` -> failure
  - `order_timeout` -> timeout
- `ChannelRegistry.LoadAll()` 可从数据库预热 active channel。
- checkout charge 主路径使用 `NewAcquirerGateway(acqReg, isRealMode)`。
- mock 支付不再依赖 checkout 旧 fallback 分支。
- `MockAcquirer` 从 `channel_config.scenarios` 注入场景。
- 本地集成测试验证三态：
  - `order_success` -> `CAPTURED`
  - `order_fail` -> `ErrPaymentFailed`
  - `order_timeout` -> `ErrPaymentTimeout`

### 3.2 Checkout 托管收银台

完成度：mock 模式 100%，real mode 边界已完成，真实 provider 调用未完成。

已完成能力：

- `/api/checkout/token` 可生成 checkout token。
- `/checkout?token=...` 可打开托管收银台页面。
- `/api/checkout/validate` 可校验 token 并返回订单信息。
- `/api/checkout/charge` 可发起支付。
- 本地 mock order provider 根据 `order_id` 返回确定性订单数据。
- charge 请求支持 `payment_method_token` 字段。
- mock 模式允许 raw card 字段用于本地演示。
- real mode 禁止 raw PAN / CVV，必须使用 `payment_method_token`。
- real mode 缺少真实 provider/channel/credential 时 fail-closed，不静默回退 mock。

### 3.3 动态通道管理

完成度：mock / 管理闭环 100%，真实 adapter 待实现。

已完成能力：

- `ChannelRegistry` 支持本地内存对象池。
- 热路径 `Get(channelID)` 只读内存，不访问 PG/KMS。
- `LoadAll(ctx)` 启动时预热通道。
- `Reload(ctx, channelID)` 支持单通道热重载。
- `Enable/Disable` 支持状态切换。
- Redis Pub/Sub 支持多实例配置更新通知。
- 配置事件格式支持 `channel_id|version`。
- 旧版本事件会跳过，新版本事件触发 reload。
- 非法 payload 会被忽略并记录。
- 支持 fail-closed 与 stale-but-serving 模式。
- credential vault 使用 AES-256-GCM 加密存储敏感信息。

### 3.4 Acquirer 管理 API

完成度：mock/local 可用。

已完成端点：

- `GET /admin/acquirer/providers`
- `POST /admin/acquirer/providers`
- `GET /admin/acquirer/channels`
- `POST /admin/acquirer/channels`
- `PATCH /admin/acquirer/channels/{id}`
- `POST /admin/acquirer/channels/{id}/enable`
- `POST /admin/acquirer/channels/{id}/disable`
- `POST /admin/acquirer/channels/{id}/rotate-secret`
- `POST /admin/acquirer/channels/{id}/reload`

安全边界：

- API 返回不包含明文 secret。
- credential 返回摘要字段，例如 `has_api_key`、`has_secret`、`key_version`、`expires_at`。
- 更新后发布 Redis Pub/Sub，使其他实例 reload。

### 3.5 Dashboard 可视化管理

完成度：代码对齐 95%-100%，仍建议浏览器手工点一遍。

已完成能力：

- `internal/platform/httpx/dashboard.go` 已增加“通道管理”Tab。
- 可展示 provider 列表。
- 可展示 channel 列表。
- 展示字段包含状态、provider code、channel code、version、active、updated time。
- 支持启用、禁用、reload 操作。
- 支持最小化创建 provider/channel 表单。
- JWT 从 `localStorage.admin_jwt` 读取。
- 所有 dashboard fetch 请求会发送 `Authorization: Bearer <token>`。
- 无 token 时显示轻量输入框。
- channel 创建表单中 `currencies` 已使用 `JSON.parse` 转成数组提交，不再把数组错误当字符串传给后端。

建议保留一个人工验收点：

- 启动 API。
- 打开 `/admin/dashboard`。
- 设置 admin JWT。
- 进入通道管理。
- 手动点击 enable / disable / reload。
- 确认列表状态变化与 API 返回一致。

### 3.6 前置风控 / 测卡拦截

完成度：mock/local 已接入。

已完成能力：

- checkout `HandleCharge` 前置接入 `riskcarding.Gateway`。
- 风控维度：
  - buyer IP
  - merchant ID / app ID
  - card BIN
- 命中阈值后直接返回风险错误，不调用后方 acquirer。
- Redis 滑动窗口用于失败计数与冷却判断。
- 测试覆盖命中风控时 acquirer/mock 不会被调用。

当前 README 中记录的默认策略：

- 同 IP 60 秒内失败超过阈值返回 429。
- 同 BIN 5 分钟内失败超过阈值返回 429。
- App 失败率超过阈值返回风险拦截。
- Merchant cooldown 返回 403。

### 3.7 Dispute / 逆向账本

完成度：核心逻辑与测试已完成。

已完成能力：

- refund confirmed。
- chargeback received。
- chargeback reversed。
- 平台 dispute reserve ledger。
- merchant negative balance。
- chargeback fee 独立分录。
- settled balance 不足时，差额进入 merchant negative balance。
- settled balance 为 0 时，不生成金额为 0 的 settled debit entry。
- repository 写入方法使用传入 tx，修复事务一致性。
- `UpdateDisputeStatus` 使用 tx，避免事务外更新。

关键状态与分录：

- `ORDER_REFUND`
- `CHARGEBACK_RECEIVED`
- `CHARGEBACK_REVERSED`
- `CHARGEBACK_FEE`
- `PLATFORM_DISPUTE_RESERVE`
- `MERCHANT_NEGATIVE_BALANCE`

### 3.8 MAE 管理

完成度：本地管理闭环已完成。

已完成能力：

- Create。
- Update。
- RotateSecret。
- ChangeStatus。
- Metrics。
- 乐观锁真实生效。
- repository 检查 affected rows，0 行更新返回版本冲突。
- 移除 active stub 和假成功响应。

### 3.9 Ledger / Treasury / Payout / Webhook 基础能力

完成度：基础能力已存在，仍需按真实业务继续加强。

已存在能力：

- 双轨账本分录。
- balance 校验。
- reserve batch。
- sweep approval。
- payout address。
- payout batch。
- reconcile enhancement。
- webhook proxy。
- webhook consumer。
- idempotency payload hash。
- outbox worker。

---

## 4. 当前完成度评估

| 范围 | 完成度 | 说明 |
| --- | ---: | --- |
| Mock / 本地演示模式 | 100% | 已通过真实 migration、seed、ChannelRegistry、AcquirerGateway 和 integration test 验证 |
| Checkout mock 三态 | 100% | success/failure/timeout 三态真实从 `channel_config.scenarios` 生效 |
| Admin Acquirer API | 100% | provider/channel/credential/reload/enable/disable 基本闭环完成 |
| Dashboard 通道管理 | 95%-100% | 代码已对齐，建议补一次浏览器人工点击验收 |
| 风控前置拦截 | 90%-95% | 已接入 checkout；后续可补更完整的压测和可视化风控面板 |
| Dispute / 逆向账本 | 90%-95% | 核心状态机与测试完成；后续可接 webhook 实际事件源 |
| Real mode fail-closed 边界 | 90% | 禁 raw PAN/CVV、必须 token、无真实配置失败关闭已完成 |
| 真实 Stripe adapter | 0%-20% | 当前仅 scaffold / 边界准备，未实现真实 API 调用 |
| 真实 Nxtapay adapter | 0%-20% | 当前仅 scaffold / 边界准备，未实现真实 API 调用 |
| 法币上链 / 交易所换汇 worker | 未进入生产实现 | 仅有规划方向，当前不应宣称完成 |

综合判断：

- 如果目标是“本地演示、mock 验收、管理后台通道配置演示”：当前可按 100% 归档。
- 如果目标是“一键切真实收单生产”：当前只能算完成基础边界和管理骨架，真实 adapter、沙箱验收、真实 webhook 对账仍未完成。
- 如果目标是“完整生产级清结算与自动化资金调度”：当前仍需继续实现真实外部 provider、银行/LP/交易所适配、异常审批和操作审计。

---

## 5. 最新验收结果

本快照生成前已执行并通过：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./test/integration -run TestLocalDemo_FullFlow -count=1 -v
go test ./... -short -count=1
```

`TestLocalDemo_FullFlow` 关键输出：

```text
Migration applied successfully
Mock provider created
Mock channel created
Registry loaded with 1 channels
Found mock_default channel: Active=true
order_success -> CAPTURED (expected: CAPTURED)
order_fail -> ErrPaymentFailed (expected)
order_timeout -> ErrPaymentTimeout (expected)
All local demo tests passed
```

---

## 6. 当前保留的真实边界

### 6.1 不能误判为已完成真实收单

当前真实 provider adapter 尚未完成。虽然系统已经具备：

- real mode 配置开关；
- raw card 禁止；
- payment method token 要求；
- fail-closed；
- provider/channel/credential 管理；
- ChannelRegistry 热加载；

但真实 Stripe / Nxtapay API 调用、真实 webhook 验签、真实支付状态映射、沙箱验收仍未落地。

### 6.2 不能绕过合规与安全边界

后续真实收单接入必须保持：

- 不存完整卡号。
- 不存 CVV。
- real mode 只接受 tokenized payment method。
- secret 只进 credential vault。
- API 不返回明文 secret。
- 失败不回退 mock。
- 所有真实资金状态必须进入账本和对账流程。

---

## 7. 后续推荐任务顺序

### P0：真实 Provider Adapter 最小生产骨架

目标：实现真实 provider 的最小可测链路，但仍使用沙箱环境。

建议拆分：

1. Stripe adapter sandbox Authorize / Capture / Refund / QueryPayment。
2. Stripe webhook 验签与事件映射。
3. Nxtapay adapter sandbox Authorize / Capture / Refund / QueryPayment。
4. Nxtapay webhook 验签与事件映射。
5. provider-specific 错误码统一映射为内部 domain error。
6. adapter contract test 与 sandbox integration test。

### P1：Dashboard 人工验收与操作审计

目标：确保运营后台“点点鼠标切通道”可以真实使用。

建议拆分：

1. 浏览器手工验收 provider/channel 创建。
2. 手工验收 enable / disable / reload。
3. 管理操作写入 audit log。
4. dashboard 增加成功/失败 toast。
5. dashboard 增加 reload 后版本号刷新提示。

### P2：Dispute 接入真实事件源

目标：让拒付、退款不只停留在手动 API，而是接入 provider webhook。

建议拆分：

1. Stripe dispute webhook -> `ChargebackReceived`。
2. Stripe dispute closed/reversed -> `ChargebackReversed`。
3. Refund webhook -> `RefundConfirmed`。
4. 幂等键使用 provider event id。
5. 回放测试与重复事件测试。

### P3：风控可视化与压测

目标：把 Redis 滑动窗口风控从代码能力变成可运营能力。

建议拆分：

1. admin risk events list API。
2. dashboard 风控事件列表。
3. 阈值配置表。
4. 风控命中率指标。
5. 压测验证 high concurrency 下 Redis key 过期与计数准确性。

### P4：资金调度与外部清算

目标：进入真实资金调度前的工程准备。

建议拆分：

1. 银行/LP/交易所 adapter 仅做合规沙箱或模拟模式。
2. conversion worker 引入断点续传。
3. limit order / slippage guard。
4. approval required 状态。
5. 操作审计与人工复核 console。

---

## 8. 关键文件索引

### 新增或重点变更

- `cmd/api/main.go`
- `cmd/seed/main.go`
- `internal/acquirer/domain/domain.go`
- `internal/acquirer/adapter/mock.go`
- `internal/acquirer/repository/repository.go`
- `internal/acquirer/registry/registry.go`
- `internal/acquirer/handler/handler.go`
- `internal/checkout/acquirer_gateway.go`
- `internal/checkout/handler.go`
- `internal/checkout/mock_order.go`
- `internal/risk/carding/gateway.go`
- `internal/risk/carding/service.go`
- `internal/dispute/service/service.go`
- `internal/dispute/repository/repository.go`
- `internal/platform/httpx/dashboard.go`
- `migrations/000015_init_dispute.up.sql`
- `migrations/000016_init_risk_event.up.sql`
- `migrations/000017_init_acquirer_registry.up.sql`
- `test/integration/local_demo_test.go`
- `README.md`

### 规划与交接文档

- `PROJECT.md`
- `STATUS.md`
- `COMPLIANT_REFACTOR_BLUEPRINT.md`
- `COMPLIANT_REFACTOR_BLUEPRINT_V2.md`
- `mimo提示词.txt`
- `PROJECT_PROGRESS_SNAPSHOT_2026-06-14.md`

---

## 9. 当前结论

当前项目已经达到一个重要稳定点：

1. 本地 mock 演示链路 100% 可用。
2. checkout 已通过 ChannelRegistry，而不是硬编码 mock gateway。
3. mock channel 可由数据库 seed 和 channel config 驱动。
4. dashboard 与 admin acquirer API 已经基本对齐。
5. real mode 的安全边界已经建立。
6. Dispute / risk / acquirer registry 三个关键加固模块已落地并通过测试。

下一阶段不应继续纠缠 mock 是否完成，而应进入“真实 adapter 沙箱实现 + dashboard 人工验收 + webhook 对账闭环”的阶段。
