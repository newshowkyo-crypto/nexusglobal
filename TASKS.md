# TASKS — 原子任务分解（循环开发任务源）

> 版本：V1.0
> 用途：AI 每次**领一个任务**做完再做下一个。任务按依赖排序，标注：依赖 / 涉及文件 / 验收标准(DoD) / 对应测试。
> 配套：状态看板见 `PROGRESS.md`；流程见 `AGENT_WORKFLOW.md`；红线见 `CONSTRAINTS.md`。
> 规则：除非依赖已满足，否则不得提前做后置任务。每个任务 DoD 全绿（含 `make ci`）才算完成。

---

## 0. 任务编号与阶段总览

| 阶段 | 任务 | 主题 |
| --- | --- | --- |
| P0 脚手架 | T001–T008 | 项目骨架、平台基础设施、外部 Mock |
| P1 账本核心 | T009–T012 | Ledger 双轨记账（资金真相，先行） |
| P2 Payout | T013–T018 | 余额冻结 + Outbox + Worker + 五阶段闭环 |
| P3 MAE | T019–T024 | 状态机 + 指标 + 熔断 + Testing 灰度 |
| P4 Webhook | T025–T029 | Receiver 代理 + Streams MQ + 消费者 + DLQ |
| P5 Treasury | T030–T034 | 银行日历 + Tiered Sweep + Delay Queue |
| P6 收尾 | T035–T038 | 端到端、对账、运维、文档校验 |

> 关键依赖原则：**账本(P1) 是 Payout(P2) 的地基；平台(P0) 是一切地基**。Webhook/MAE/Treasury 相对独立，可在 P1 后并行推进，但单 agent 仍按编号顺序做。

---

## P0 — 脚手架与平台基础设施

### T001 初始化项目骨架
- **依赖**：无
- **涉及文件**：`go.mod`、`go.sum`、目录结构（TDD §2 全部空目录 + `.gitkeep`）、`README.md`、`.env.example`、`sqlc.yaml`、`docker-compose.yml`、`Makefile`
- **DoD**：
  - `go.mod` module=`github.com/nexusglobal/mamr`，`go 1.22`，锁定 TDD §1.2 依赖。
  - 目录结构与 TDD §2 完全一致。
  - `make up` 能起 PG+Redis；`make build` 通过（空包可编译）。
  - `.env.example` 含 TDD §10 全部键。
- **测试**：`go build ./...` 通过。

### T002 配置加载（platform/config）
- **依赖**：T001
- **涉及文件**：`internal/platform/config/`
- **DoD**：`Config` struct 用 `caarlos0/env` 从环境变量加载 TDD §10 所有项；缺失必填项报错；提供 `Load()`。
- **测试**：`TestConfig_Load_*`（默认值、缺失必填、覆盖）。

### T003 Postgres 连接池 + TxManager（platform/postgres）
- **依赖**：T002
- **涉及文件**：`internal/platform/postgres/`
- **DoD**：
  - `pgxpool` 初始化（DSN 来自 config，注册 decimal/uuid 编解码）。
  - `TxManager.WithinTx(ctx, fn func(pgx.Tx) error) error`：nil 提交、err 回滚。
  - 提供 `Ping(ctx)`。
- **测试**：testcontainers 起 PG，`TestTxManager_Commit/Rollback`，并发事务隔离。

### T004 Redis 客户端 + Lua 封装（platform/redis）
- **依赖**：T002
- **涉及文件**：`internal/platform/redis/`
- **DoD**：go-redis v9 客户端；`Ping`；INCRBY 计数、ZSET（Delay Queue 用）、SETNX 辅助锁封装；Lua 脚本加载器。
- **测试**：testcontainers 起 Redis，`TestRedis_Incr/ZSet/Lock`。

### T005 金额工具（platform/money）
- **依赖**：T001
- **涉及文件**：`internal/platform/money/`
- **DoD**：`Minor int64` 类型 + `Add/Sub/MulBps`；decimal 辅助 `Round4`、`BpsOf(decimal,bps)`；**无 float**；从 string/int 构造。
- **测试**：`TestMoney_*` 表驱动，含溢出与精度，覆盖率 ≥85%。**断言代码内无 float64 参与**（可用代码审查/grep）。

### T006 Clock / Logger / apperr / idempotency（platform 杂项）
- **依赖**：T002
- **涉及文件**：`internal/platform/{clock,logger,apperr,idempotency}/`
- **DoD**：
  - `clock.Clock` 接口 + `RealClock` + `MockClock`（可设固定时间）。
  - `logger` slog JSON 初始化（级别来自 config）。
  - `apperr`：`type Error{Code string;HTTP int;...}` + `New/Wrap/AsAPIError`，覆盖 API.md §3 全部 code。
  - `idempotency`：`Compute(parts...) string`（SHA-256）、payload_hash 计算、读写存储接口。
- **测试**：各包基础单测；apperr 映射到 HTTP 码；idempotency 同输入同输出。

### T007 加密（platform/crypto + MockKMS）
- **依赖**：T002
- **涉及文件**：`internal/platform/crypto/`
- **DoD**：AES-256-GCM Envelope Encryption；`Encrypt/Decrypt(ctx, data, meta)`；AAD=tenant+record+field+key_version；96-bit nonce；MockKMS（本地主密钥）；接口与真实 KMS 一致。
- **测试**：`TestCrypto_RoundTrip`、AAD 不匹配解密失败、nonce 唯一性。

### T008 外部依赖 interface + Mock（pkg/external + pkg/validator）
- **依赖**：T001
- **涉及文件**：`pkg/external/{lp,bank,mae}/`、`pkg/validator/`
- **DoD**：
  - `lp.Client`/`bank.Client`/`mae.Client` 接口（TDD §7.1）+ 哨兵错误 `ErrTimeout`/`ErrExplicitFailure`。
  - 确定性 Mock，可注入 success/explicit_failure/timeout/delay 场景。
  - `pkg/validator`：TRON_TRC20/ETH_ERC20/BTC/SOL 地址格式校验（PRD 第 8 章）。
- **测试**：Mock 行为可注入；地址校验表驱动覆盖合法/非法/边界，覆盖率 ≥85%。

---

## P1 — Ledger 双轨账本（资金真相，先行）

### T009 Ledger migration + 表结构
- **依赖**：T003
- **涉及文件**：`migrations/000004_init_ledger.up/down.sql`
- **DoD**：建 `ledger_account`、`ledger_transaction`（idempotency_key UNIQUE）、`ledger_entry`；金额 `numeric(20,4)`；`make migrate` 成功；`down` 可回滚。
- **测试**：迁移 up/down 在 testcontainers 通过。

### T010 Ledger domain（复式记账规则）
- **依赖**：T005
- **涉及文件**：`internal/ledger/domain/`
- **DoD**：`Direction`(DEBIT/CREDIT)、`Entry`、`Transaction`；`ValidateBalanced(entries)` 借=贷校验；账户类型枚举（PRD §6.5）。无 I/O。
- **测试**：借贷平衡/不平衡、零金额拒绝、方向校验，覆盖率 ≥85%。

### T011 Ledger repository（sqlc + queries）
- **依赖**：T009, T010
- **涉及文件**：`internal/ledger/queries.sql`、`internal/ledger/repository/`
- **DoD**：插入 transaction+entries（同事务）、`ON CONFLICT(idempotency_key) DO NOTHING`、按 merchant 查询余额相关。`make sqlc` 生成成功。
- **测试**：幂等插入（重复 key 不重复入账）、借贷成对落库。

### T012 Ledger service（幂等入账）
- **依赖**：T011, T006
- **涉及文件**：`internal/ledger/service/`、`internal/ledger/handler/`（仅查询接口）
- **DoD**：`Post(ctx, cmd)` 在事务内写 transaction+entries+校验平衡；幂等键重复返回首次结果；`GET /admin/merchant/{id}/balance`（API §7.1）。
- **测试**：service 幂等、平衡校验拒绝、余额查询；handler httptest。
- **红线核对**：RED-10、RED-13。

---

## P2 — Payout 冻结 + Outbox + Worker

### T013 Payout/Treasury-ledger/Outbox migration
- **依赖**：T003
- **涉及文件**：`migrations/000005_init_payout.up/down.sql`、`000006_init_outbox.up/down.sql`
- **DoD**：建 `merchant_treasury_ledger`（UNIQUE(merchant,currency)、非负 CHECK）、`merchant_payout_address`、`payout_request`（idempotency_key UNIQUE）、`transactional_outbox`（idempotency_key UNIQUE、dispatch 索引）。
- **测试**：迁移 up/down 通过；唯一/CHECK 约束生效（插重复/负值报错）。

### T014 Payout domain（状态机 + 地址清洗）
- **依赖**：T008
- **涉及文件**：`internal/payout/domain/`
- **DoD**：payout 状态机（API §9）合法迁移表 + `CanTransition`；地址清洗流水线（trim/去不可见/一致性/格式/normalize/hash，调用 pkg/validator）。无 I/O。
- **测试**：状态机全迁移（含非法）、地址清洗全分支，覆盖率 ≥85%。
- **红线核对**：RED-08 同理（非法迁移拒绝）。

### T015 Payout repository
- **依赖**：T013, T014
- **涉及文件**：`internal/payout/queries.sql`、`internal/payout/repository/`
- **DoD**：`LockBalanceForUpdate`（**FOR UPDATE**）、`ApplyFreeze`、`ApplyConfirm`、`ApplyRelease`、payout_request CRUD、地址 CRUD、Outbox 插入/抢占（**SKIP LOCKED**）。
- **测试**：FOR UPDATE 锁行为、SKIP LOCKED 抢占不重复、乐观锁。
- **红线核对**：RED-04、RED-05。

### T016 Payout service：本地冻结事务 + 写 Outbox（Phase1）
- **依赖**：T015, T012
- **涉及文件**：`internal/payout/service/`、`internal/payout/handler/`
- **DoD**：实现 TDD §5.3 Phase1：事务内 FOR UPDATE→校验 available→扣 settled/加 frozen→写双轨分录(调 ledger)→写 payout_request→写 Outbox(FX_LOCK_REQUESTED)；**事务内零外部 I/O**；余额不足回滚不写 Outbox。接口：`POST /merchant/payout-request`、`POST /internal/payout/{id}/freeze`、`POST /merchant/payout-address`。
- **测试**：
  - 冻结成功路径（余额变化 + Outbox 写入 + 分录平衡）。
  - 余额不足 `422`、不写 Outbox。
  - **并发 5 笔相同余额：仅覆盖范围内成功，无超卖/负余额**（必备）。
  - 幂等重放。
- **红线核对**：RED-01、RED-04、RED-05、RED-13、RED-14。

### T017 Outbox Worker 框架 + FX Lock Worker（Phase2）
- **依赖**：T016, T008, T004
- **涉及文件**：`cmd/worker/main.go`、`internal/worker/outbox/`、`internal/worker/fxlock/`
- **DoD**：Outbox 轮询（SKIP LOCKED 抢占、并发受 config 控制、重试退避、DeadLetter）；FX Lock Worker 抢 `FX_LOCK_REQUESTED`→**事务外**调 `lp.FXLock`→成功置 FxLocked+写 `LP_PAYOUT_REQUESTED`；明确失败→触发释放流程；超时→UnknownExternalState（frozen 不动）。
- **测试**：抢占不重复、退避重试、DeadLetter；FX 成功/失败/超时三分支；**超时不释放 frozen**。
- **红线核对**：RED-01、RED-02、RED-03。

### T018 LP Payout Worker + Confirm/Release（Phase3/4/5）
- **依赖**：T017
- **涉及文件**：`internal/worker/payout/`、`internal/payout/service/`（confirm/release）
- **DoD**：
  - Phase3：抢 `LP_PAYOUT_REQUESTED`，校验 FxLocked，事务外调 `lp.Payout`；已提交→LPProcessing；明确失败→Failed→释放；超时→UnknownExternalState。
  - Phase4 Confirm：第二本地事务核销 frozen（Dr Frozen/Cr ExternalPayout），状态→Confirmed，幂等。
  - Phase5 Release：第二本地事务释放 frozen→settled，状态→BalanceReleased，幂等。
  - 接口：`/internal/payout/{id}/fx-lock`、`/submit`、`/reconcile`。
- **测试**：Confirmed 核销正确、Failed 释放正确、UnknownExternalState 不动 frozen、重复 confirm 幂等。**端到端：冻结→FXLock→Payout→Confirm 全闭环**。
- **红线核对**：RED-01、RED-02、RED-03、RED-13。

---

## P3 — MAE 状态机与路由

### T019 MAE migration
- **依赖**：T003
- **涉及文件**：`migrations/000001_init_mae.up/down.sql`
- **DoD**：建 `mae_configuration`（mae_id UNIQUE、version、business_day_lock_until）、`mae_runtime_metric_snapshot`；密文字段 bytea。
- **测试**：迁移 up/down。

### T020 MAE domain（状态机）
- **依赖**：T005
- **涉及文件**：`internal/mae/domain/`
- **DoD**：6 状态枚举 + 合法迁移表（**无 Cooldown→Active**）；`Transition` 校验；熔断条件评估纯函数；Testing 达标判定纯函数；当日锁判定。
- **测试**：全迁移含非法（断言 `Cooldown→Active` 非法）、熔断各条件、Testing 达标/不达标、当日锁，覆盖率 ≥85%。
- **红线核对**：RED-07、RED-08。

### T021 MAE 指标聚合（metric，Redis Counter）
- **依赖**：T004, T005
- **涉及文件**：`internal/mae/metric/`
- **DoD**：Redis Key（TDD §3.5.1 / PRD §3.6）；`OnCaptured`(INCRBY exposure+captured)、`OnRefund`/`OnDispute`(独立 INCRBY)；**exposure 仅 INCRBY，无 DECRBY**；滑动窗口聚合 + 写 snapshot。
- **测试**：exposure 只增（断言无递减接口）、refund 独立不影响 exposure、snapshot 写入。
- **红线核对**：RED-06。

### T022 MAE repository + service（CRUD + 状态变更乐观锁）
- **依赖**：T019, T020
- **涉及文件**：`internal/mae/queries.sql`、`internal/mae/repository/`、`internal/mae/service/`
- **DoD**：创建/更新（凭证加密存）、状态变更乐观锁 SQL（TDD §5.1）、rotate-secret；状态变更写审计。
- **测试**：乐观锁冲突 `VERSION_CONFLICT`、凭证加密落库、非法迁移拒绝。

### T023 MAE 熔断 + Testing 恢复 service
- **依赖**：T021, T022
- **涉及文件**：`internal/mae/service/`
- **DoD**：熔断评估（读 Redis，≤100ms 目标）→ Active→Cooldown 并设 business_day_lock_until；Cooldown→Testing 校验当日锁；Testing→Active/Cooldown/Suspended 评估。
- **测试**：Exposure 达阈→Cooldown+锁；当日不回 Active；Cooldown→Testing 校验锁；Testing 达标→Active、连续失败 3 次→Suspended。
- **红线核对**：RED-06、RED-07、RED-08。

### T024 MAE handler
- **依赖**：T022, T023
- **涉及文件**：`internal/mae/handler/`
- **DoD**：API §4 全部端点；密文不回传；幂等头；错误码映射。
- **测试**：httptest 各端点成功 + 错误码 + 当日锁 `423`。

---

## P4 — Webhook Receiver + Streams MQ

### T025 Streams MQ 封装（platform/stream）
- **依赖**：T004
- **涉及文件**：`internal/platform/stream/`
- **DoD**：`Producer.Publish`（XADD）；`Consumer`（XReadGroup 消费组、XAck、XAutoClaim 重投、maxDeliveries→DLQ）；DLQ stream；幂等创建消费组。
- **测试**：发布消费、未 ACK 重投、超阈进 DLQ、消费组幂等创建。
- **红线核对**：RED-09、RED-10。

### T026 Webhook migration
- **依赖**：T003
- **涉及文件**：`migrations/000002_init_webhook.up/down.sql`
- **DoD**：`webhook_receiver_proxy`、`webhook_ingress_event`（idempotency_key UNIQUE、raw_payload_ciphertext bytea）。
- **测试**：迁移 up/down + UNIQUE。

### T027 Webhook domain + repository（验签、幂等键）
- **依赖**：T026, T007, T006
- **涉及文件**：`internal/webhook/domain/`、`internal/webhook/repository/`、`internal/webhook/queries.sql`
- **DoD**：HMAC_SHA256/RSA_SHA256 验签；幂等键计算（PRD §4.8 两种规则）；ingress event CRUD；Provider Allowlist 校验。
- **测试**：验签通过/失败、幂等键两规则、重复 provider_event_id 检测。

### T028 Webhook handler（Receiver 代理，只投 MQ）
- **依赖**：T027, T025
- **涉及文件**：`internal/webhook/handler/`
- **DoD**：API §5.1 三入口；TLS/IP Allowlist/限流→验签→payload_hash→idempotency_key→加密存 raw→写 ingress→**XADD 投 Streams**；快速 ACK；**绝不 import 账务 repository**。`/admin/webhook/events`、`/replay`。
- **测试**：成功投递、验签失败 `401`+记录 Rejected、重复幂等不重投；**断言不依赖 ledger/payout repository**。
- **红线核对**：RED-09。

### T029 核心消费者（Streams→Ledger 入账）
- **依赖**：T028, T012, T018
- **涉及文件**：`internal/webhook/consumer/`、`cmd/worker/main.go`（挂载消费者）
- **DoD**：从 Streams 消费 → 幂等检查 → 状态机单调校验 → Ledger 事务入账 → 写 Outbox（如需）→ 对账事件；COMMIT 后才 XAck；失败重投/DLQ。处理 Captured/Settled/Refund/Dispute/PayoutConfirmed/LP 回调。
- **测试**：重复 webhook 不重复入账（端到端）、失败进 DLQ、ACK 在 commit 后。
- **红线核对**：RED-09、RED-10。

---

## P5 — Treasury 银行清算与 Tiered Sweep

### T030 Treasury migration
- **依赖**：T003
- **涉及文件**：`migrations/000003_init_treasury.up/down.sql`
- **DoD**：`banking_calendar`、`sweep_rule`、`sweep_transfer`（idempotency_key UNIQUE、status 全枚举）。
- **测试**：迁移 up/down + UNIQUE。

### T031 Treasury domain（日历/窗口/受益人规则）
- **依赖**：T005
- **涉及文件**：`internal/treasury/domain/`
- **DoD**：`WithinBankingWindow(calendar, rail, now)`（工作日∧未过 cut-off∧非节假日∧window matrix）；受益人规则四分支（RED-12）；Sweep 触发判定纯函数。无 I/O。
- **测试**：窗口判断（工作日/节假日/cut-off 边界/时区）、受益人四分支，覆盖率 ≥85%。
- **红线核对**：RED-11、RED-12。

### T032 Treasury repository + Delay Queue
- **依赖**：T030, T031, T004
- **涉及文件**：`internal/treasury/queries.sql`、`internal/treasury/repository/`、`internal/treasury/scheduler/`（Delay Queue 部分）
- **DoD**：calendar/rule/transfer CRUD（乐观锁）；Redis Delay Queue（ZSET，TDD §5.2）入队/到期出队；daily_sweep_count 统计。
- **测试**：Delay Queue 入队/到期出队、归集计数。

### T033 Sweep service + scheduler（Tiered Scheduled）
- **依赖**：T032
- **涉及文件**：`internal/treasury/service/`、`internal/treasury/scheduler/`
- **DoD**：`Evaluate` 实现 TDD §5.2：非窗口→Accumulating/Delayed（不调银行 API）；窗口内且达阈值且未超次数且受益人/审批通过→建 sweep_transfer(Locked)；submit 经 Worker 事务外调 bank；超时→UnknownExternalState（禁盲重试）。定时 tick 扫 Delay Queue。
- **测试**：非窗口不调银行 API、窗口内提交、超次数拒绝、UNKNOWN 拒绝、三方大额 ApprovalRequired、超时→UnknownExternalState。
- **红线核对**：RED-01、RED-02、RED-03、RED-11、RED-12。

### T034 Treasury handler
- **依赖**：T033
- **涉及文件**：`internal/treasury/handler/`
- **DoD**：API §6 全部端点；错误码（OUTSIDE_BANKING_WINDOW/BENEFICIARY_NOT_ALLOWED/APPROVAL_REQUIRED）。
- **测试**：httptest 各端点 + 错误码。

---

## P6 — 收尾、端到端、运维

### T035 cmd/api 与 cmd/worker 装配
- **依赖**：T024, T029, T034, T018
- **涉及文件**：`cmd/api/main.go`、`cmd/worker/main.go`、`internal/platform/httpx/`
- **DoD**：手工 DI 装配所有 service/handler；中间件（鉴权、幂等、限流、recover、requestID）；路由注册；`/healthz`、`/readyz`；优雅关闭。
- **测试**：`/readyz` 依赖不可达→503；路由表完整。

### T036 端到端集成测试（test/integration）
- **依赖**：T035
- **涉及文件**：`test/integration/`
- **DoD**：
  - Payout 五阶段闭环（含 Failed 释放、UnknownExternalState）。
  - Webhook→Streams→Ledger 入账 + 重复不重账。
  - Sweep 窗口判断 + Delay Queue。
  - 并发双花总验收（PRD §12.4）。
- **测试**：以上场景全绿（`make test`）。

### T037 对账 Worker（reconciliation）
- **依赖**：T018, T033
- **涉及文件**：`internal/worker/reconcile/`
- **DoD**：对 UnknownExternalState 的 payout/sweep 调外部 QueryStatus 推进最终态；对账事件落库；不盲重试资金提交。
- **测试**：Unknown→Query→Confirmed/Failed 推进；查询仍未知则保持。
- **红线核对**：RED-03。

### T038 文档校验与红线自查脚本
- **依赖**：全部
- **涉及文件**：`Makefile`（`redlines` 目标）、`README.md`
- **DoD**：`make redlines` 跑 CONSTRAINTS §17 grep 脚本全过；README 写明一键启动与开发循环；PROGRESS 全部完成；MEMORY 收尾。
- **测试**：`make ci && make redlines` 全过；端到端验收对照 PRD 第 12 章逐条勾选。

---

## 附：每个任务通用 DoD（叠加在各任务之上）

```text
[ ] 代码符合 CODING_STANDARDS.md
[ ] 不违反 CONSTRAINTS.md 任一红线（跑 §17 自查）
[ ] 接口符合 API.md 契约（涉及 handler 时）
[ ] gofmt/vet 干净，make build 通过
[ ] 单测 + 相关集成测试通过（make test-short 必过；涉及 DB/Redis 跑 make test）
[ ] domain 覆盖率 ≥85%
[ ] 更新 PROGRESS.md（状态）与 MEMORY.md（决策/坑/指针）
[ ] 提交信息 `T0xx: <简述>`
```

---

## 验收映射（PRD 第 12 章 → 任务）

| PRD 验收 | 对应任务 |
| --- | --- |
| 12.1 MAE | T019–T024（Exposure 只增不减、当日不回池、Cooldown→Testing、5–10% 灰度、Redis 熔断、Snapshot、审计） |
| 12.2 Webhook | T025–T029（全入 Receiver、不直连账务、验签幂等加密投递、台账证书 Allowlist、不重账、DLQ、重放） |
| 12.3 Sweep | T030–T034（非满额触发、非窗口不调银行 API、累计/Delay、Tiered、最大次数、非同名审批、超时 Unknown、禁盲重试） |
| 12.4 Payout | T013–T018, T036（先冻结、冻结失败不调 LP、FOR UPDATE、事务内无外呼、冻结账务、写 Outbox、Worker 调 LP、Confirmed 核销、Failed 释放、Unknown 不释放、并发不超卖、地址校验） |
