# NexusGlobal MAMR-System 项目进度、功能完成度与业务逻辑总览

> 更新时间：2026-06-14
>
> 本文件是当前项目的一页式总览，用于回答三个问题：
>
> 1. 这个项目到底在做什么；
> 2. 已经完成了哪些功能，完成到什么程度；
> 3. mock / 本地演示、真实生产上线分别处于什么阶段。
>
> 本文件基于当前代码、`PRD.md`、`TDD.md`、`STATUS.md`、`README.md`、历史审计记录，以及最近一轮本地复核结果整理。

---

## 1. 一句话结论

NexusGlobal 是一个面向跨境 SaaS 商户的资金调度、收单路由、清算归集、双轨账本和代付出金系统。

当前状态：

| 维度 | 当前完成度 | 结论 |
| --- | ---: | --- |
| mock / 本地演示模式 | 100% | 已具备完整本地演示闭环 |
| 核心资金安全逻辑 | 95%+ | 已完成冻结、出金、核销、释放、对账、负余额防线 |
| 生产真实接入 | 0% | 尚未填写真实 LP / Bank / MAE / KMS / 支付通道凭证 |
| 生产上线准备度 | 约 85%-90% | 开发骨架扎实，但仍需真实联调、AML、手续费与灰度验证 |

换句话说：**本项目已经可以在本地 mock 模式完整演示，不等于已经可以直接生产上线。**

---

## 2. 项目业务逻辑

系统围绕一条完整资金链路设计：

```text
商户订单
  -> Single-Use Token
  -> 托管收银台 Checkout
  -> MAE 多收单实体路由
  -> Webhook Receiver 接收外部事件
  -> Redis Streams 进入核心消费链路
  -> 双轨账本 Ledger 入账
  -> 商户余额沉淀
  -> Payout 本地冻结
  -> Transactional Outbox
  -> FX Lock Worker
  -> LP Payout Worker
  -> LP Confirm / Failed / Unknown
  -> Confirm 核销 frozen 或 Release 释放 frozen
  -> Reconcile 对账修复 UnknownExternalState
```

资金原则：

1. Ledger 是资金真相来源。
2. 商户余额快照只用于查询和并发控制。
3. 出金前必须先在本地事务中冻结余额。
4. LP / Bank / MAE 外部调用不能发生在 DB 本地事务内。
5. Timeout 不等于 Failed，必须进入 UnknownExternalState 并等待对账。
6. Redis 不能作为最终资金安全边界，最终边界是 DB 事务、唯一约束、行锁和幂等键。

---

## 3. 核心模块逻辑

### 3.1 Checkout / Single-Use Token

用途：把商户订单安全引导到托管收银台。

已完成：

- Single-Use Token 创建、保存、兑换、过期、使用次数控制。
- MemoryStore 与 RedisStore 两种 token 存储。
- RedisStore 使用 Lua 保证使用次数递增原子性。
- Checkout 页面可根据 token 展示订单。
- 本地 mock 模式新增 `/api/checkout/token`，用于快速创建演示 token。
- MockOrderProvider 已接入，订单展示不再回退到 `Merchant / 0.00 / USD`。
- MockPaymentGateway 支持三态：
  - `order_success` / `order_123`：成功；
  - `order_fail`：返回 402；
  - `order_timeout`：返回 504。

注意：

- `/api/checkout/token` 是本地演示入口，生产前必须加鉴权或环境门禁。
- 卡号和 CVV 只传给支付网关，不落库，不写日志。

完成度：mock 模式 100%，生产真实支付接入未开始。

### 3.2 MAE 多收单实体与路由

用途：管理多个外部收单实体，根据状态、风险、额度、币种、租户和灰度策略做路由。

已完成：

- MAE 状态机：Draft、Active、Cooldown、Testing、Suspended、Disabled。
- 禁止 Cooldown 直接回 Active，必须先进入 Testing。
- Exposure 只增不减，refund / dispute 不回扣 exposure。
- 熔断后写入 `business_day_lock_until`。
- Testing 连续失败计数、超时回 Cooldown。
- 路由引擎支持权重、币种、租户、辖区、Testing 灰度过滤。
- 管理端点已补齐：
  - Create；
  - Update；
  - RotateSecret；
  - ChangeStatus；
  - Metrics。
- Update 与 RotateSecret 已检查 `RowsAffected`，版本冲突返回 `VERSION_CONFLICT`，不再把 0 行更新当成功。

完成度：mock / 本地管理闭环 100%，真实收单健康检查和生产流量灰度仍需 staging 验证。

### 3.3 Webhook Receiver

用途：隔离外部 webhook，避免外部系统直接打进核心账务逻辑。

已完成：

- MAE / Bank / LP 三类 webhook 入口。
- Provider 配置、验签、payload hash、幂等键。
- 原始 payload 加密落库。
- Redis Streams 投递。
- LP / MAE / Bank 三条 stream 均有消费者。
- DLQ、XAutoClaim、重投逻辑已接线。
- 后台 ListEvents / Replay 已从 stub 改为真实查询和真实重投。

完成度：mock 模式 100%，真实 webhook 证书、IP allowlist、provider 密钥需要生产配置。

### 3.4 Ledger 双轨账本

用途：作为资金真相来源。

已完成：

- Ledger account、transaction、entry 表结构。
- 借贷方向与复式记账校验。
- 所有活跃资金路径保持一借一贷等额。
- 幂等入账。
- 账本金额使用 decimal，不使用 float。
- 历史严重 BUG C1 已修：错误的 decimal -> numeric 指数取反逻辑已移除，统一使用 `money.DecimalToNumeric`。
- 已补真实 DB 非整数金额回归测试，覆盖 `100.50`、`0.01`、`100.1234`、`999.99` 等场景。

完成度：100%。

### 3.5 Payout 出金冻结与 LP 代付

用途：商户发起出金时，先冻结本地余额，再异步调用 LP。

已完成：

- 创建出金地址。
- 地址格式清洗、hash、masked。
- 出金请求校验 screening / whitelist 状态。
- 本地冻结事务：
  - `SELECT ... FOR UPDATE` 锁商户余额；
  - 校验可用余额；
  - settled 减少；
  - frozen 增加；
  - 写双轨分录；
  - 写 payout_request；
  - 写 transactional_outbox。
- Outbox Worker 抢占任务。
- FX Lock Worker 调 LP mock / real interface。
- LP Payout Worker 调 LP mock / real interface。
- Confirm 后核销 frozen。
- Failed 后释放 frozen 回 settled。
- UnknownExternalState 不释放 frozen，等待对账。
- 并发防双花。
- provider_ref / lock_id 已真实落库，对账路径可达。

完成度：mock 模式 100%，真实 LP 凭证和真实回调未接入生产环境。

### 3.6 Reconcile 对账

用途：处理 LP / Bank timeout 后的未知状态。

已完成：

- UnknownExternalState 查询 LP 状态。
- confirmed -> Confirm；
- failed -> Release；
- still unknown -> 增加 reconcile attempts；
- 超过次数或时间阈值 -> manual_review；
- manual_review 期间禁止自动释放 frozen。
- Redis 分布式锁，缺锁时 fail-fast，不静默无锁运行。

完成度：mock 模式 100%，真实 LP QueryStatus 需生产验证。

### 3.7 Treasury / Sweep 银行归集

用途：根据银行工作日、cut-off、受益人规则和金额阈值做资金归集。

已完成：

- banking_calendar；
- sweep_rule；
- sweep_transfer；
- bank_account_balance；
- 工作日、节假日、cut-off 判断；
- same-name / third-party / cross-border / unknown 受益人规则；
- ApprovalRequired 流程；
- Delay Queue；
- 归集确认后扣减源账户余额，避免重复归集。

注意：

- 当前 Bank Submit 是在 service 中调用 bank client，虽然不在 DB 事务里，但与 PRD 中“统一 Outbox / Worker / Saga 外呼”的理想形态相比仍可继续工程化。

完成度：mock 演示 95%-100%，生产真实银行联调未开始。

### 3.8 Admin / Dashboard / 数据录入

用途：本地演示和运营后台基础录入。

已完成：

- merchant 创建；
- bank account 创建；
- bank calendar 创建；
- MAE config 创建；
- webhook proxy 创建；
- sweep rule 创建；
- payout address 创建；
- stats、MAE list/detail、payout list/detail、outbox list、webhook events、ledger transactions、merchant balances 等查询。
- 敏感字段加密落库，缺 encryptor 时 fail-closed，不退化明文。

完成度：mock / 本地演示 100%。

---

## 4. Mock / 本地演示模式完成度

当前 mock / 本地演示模式可以视为 100% 完成，包含：

| 演示能力 | 状态 |
| --- | --- |
| 一键启动 PG / Redis | 已完成 |
| migration 建表 | 已完成 |
| API / worker 启动 | 已完成 |
| 后台录入演示数据 | 已完成 |
| 创建 checkout token | 已完成 |
| checkout 页面展示 mock 订单 | 已完成 |
| checkout 成功支付 | 已完成 |
| checkout 失败支付 402 | 已完成 |
| checkout 超时支付 504 | 已完成 |
| MAE 创建 / 更新 / 轮密钥 / 改状态 / 查指标 | 已完成 |
| Payout 冻结 / FX Lock / LP Payout / Confirm / Release | 已完成 |
| Webhook 接收 / 投递 / 消费 / DLQ / Replay | 已完成 |
| Reconcile UnknownExternalState | 已完成 |
| Sweep 规则 / 审批 / 归集 / 扣减源余额 | 已完成 |
| build / vet / fmt / short tests | 已通过 |

最近一次复核通过：

```text
go build ./...
go vet ./...
gofmt -l .
go test ./... -short -count=1
```

其中 `go test ./... -short -count=1` 本轮实际包含 `test/integration`，并通过。

---

## 5. 真实生产上线状态

真实生产上线尚未开始，主要原因不是代码骨架缺失，而是外部真实系统尚未接入和验证。

生产上线前必须完成：

1. 填写真实环境变量：
   - `APP_ENV=production`
   - `EXTERNAL_MODE=real`
   - `KMS_MODE=real`
   - `TOKEN_STORE=redis`
   - `LP_BASE_URL`
   - `LP_API_KEY`
   - `BANK_BASE_URL`
   - `BANK_API_KEY`
   - `MAE_BASE_URL`
   - `MAE_API_KEY`
   - `VAULT_BASE_URL`
   - `VAULT_TOKEN`
   - `JWT_SECRET`
   - `INTERNAL_TOKEN`
2. 在 staging 环境跑完整链路：
   - healthz / readyz；
   - 创建出金地址并确认加密落库；
   - Freeze -> FXLock -> LP Payout -> Confirm；
   - webhook captured -> ledger 入账；
   - sweep evaluate -> bank transfer；
   - MAE 熔断 -> business_day_lock_until 写入；
   - token -> checkout -> charge；
   - OTel span 上报。
3. 跑 k6 压测并产出 P95 报告。
4. 灰度放量。
5. 配置监控、告警、日志、备份和回滚。

生产上线完成度：0%，因为真实凭证和真实外部联调还没有执行。

---

## 6. 已修复的关键问题

| 编号 | 问题 | 当前状态 |
| --- | --- | --- |
| C1 | decimal -> numeric 指数取反，导致小数金额放大 | 已修复 |
| G1 | merchant_id 从请求体读取导致 IDOR | 已修复 |
| G2 | provider_ref / lock_id 不落库，对账不可达 | 已修复 |
| G3 | `/admin/outbox` 路由重复注册 | 已修复 |
| G4 | webhook 验签使用密文当密钥 | 已修复 |
| G5 | MAE / Bank stream 无消费者 | 已修复 |
| G6 | 多个活跃端点是 stub | 已修复 |
| G7 | 出金不校验 screening / whitelist | 已修复 |
| G8 | Idempotency-Key 不校验 payload hash | 已修复 |
| G9 | metrics 无鉴权、token 回显、JWT 密钥弱校验等 | 已修复 |
| G10 | sweep 后源余额不扣减 | 已修复 |
| M1 | MAE Update / RotateSecret 假成功 | 已修复 |
| M2 | checkout OrderProvider 为 nil | 已修复 |
| M3 | mock payment 三态运行时未生效 | 已修复 |
| M4 | MAE Update / RotateSecret 未检查 affected rows | 已修复 |
| M5 | README demo 命令与真实 API 不匹配 | 已修复 |

---

## 7. 残留事项

### 7.1 不影响 mock 演示，但影响生产

| 项 | 说明 | 建议 |
| --- | --- | --- |
| `/api/checkout/token` 公开 | 为本地演示新增，生产环境不能裸露 | 加鉴权或只允许 local/mock 环境注册 |
| AML / 制裁筛查 | 当前没有真实第三方 screener | 接 Chainalysis / TRM 或同类服务 |
| 周/月手续费分录 | 目前未实现费用计提和对账 | 明确费率口径后补 `FEE` 双轨分录 |
| 真实支付通道 | Checkout 仍是 mock gateway | 接真实 MAE / PSP 支付链路 |
| 真实 LP / Bank / MAE | 只有 real client 骨架，未做真实环境联调 | staging 验证 |
| Sweep 外呼形态 | 当前 submit 不在事务内，但仍是 service 直接调 bank client | 可继续改为 Outbox / Worker / Saga |

### 7.2 文档注意事项

- `STATUS.md` 是历史权威核查文件，但它反映的是前一轮口径。
- 本文件反映的是最近 mock/local demo 补齐后的当前口径。
- `README.md` 现在提供本地演示步骤。
- `GO_LIVE.md` 仍是生产切换 checklist。

---

## 8. 项目当前完成度总结

| 层级 | 完成度 | 说明 |
| --- | ---: | --- |
| 平台基础设施 | 100% | config、PG、Redis、crypto、logger、otel、factories |
| 账本 Ledger | 100% | 双轨、幂等、借贷平衡 |
| Payout | 100% | mock 闭环完整 |
| Outbox / Worker | 100% | 抢占、退避、DeadLetter、FX/Payout worker |
| Webhook | 100% | mock 链路完整 |
| MAE | 100% | mock 管理和路由完整 |
| Treasury / Sweep | 95%-100% | mock 演示完整，工程形态可继续优化 |
| Checkout / Token | 100% | mock 演示完整 |
| Admin 本地录入 | 100% | mock 演示完整 |
| 测试与工程化 | 95%+ | build/vet/fmt/short test 通过，全量 test 依赖 Docker 环境 |
| 真实生产接入 | 0% | 尚未配置真实凭证和联调 |

最终口径：

```text
mock / 本地演示模式：100%
生产代码骨架：约 85%-90%
真实生产上线：0%（待真实凭证、staging 联调、压测、灰度）
```

---

## 9. 建议下一步

1. 保留当前状态作为 `mock-local-demo-complete` 基线。
2. 在进入生产前，先给 `/api/checkout/token` 增加环境门禁或鉴权。
3. 按 `GO_LIVE.md` 建 staging 环境，填真实 LP / Bank / MAE / Vault KMS。
4. 补 AML / 制裁筛查。
5. 补手续费分录和周期对账。
6. 对 Sweep submit 做 Outbox / Worker 化，进一步贴合 PRD 的外呼一致性。
7. 跑全量 `go test ./... -count=1` 和 k6 压测，留存报告。

