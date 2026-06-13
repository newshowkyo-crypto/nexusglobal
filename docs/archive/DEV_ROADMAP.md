# NexusGlobal MAMR-System 研发推进与模块拆解路线图 DEV_ROADMAP

> 工程总指挥蓝图。五个研发冲刺（Sprint）任务包，锚定真实代码现状。
> **定位**：系统已 ≈98% 闭环落地，本路线图是「架构基线对照 + 加固/补全蓝图」，**在既有代码上增量推进，禁止重写已完成部分**。
> 配套：PRD.md（需求真相）、SYSTEM_OVERVIEW.md（总览）、AUDIT_LOG.md（进度真实性）、PROJECT_STATUS.md（逐项状态）。

---

## 0. 事实基线校准（执行体必读，错了前提就全错）

1. **存储引擎是 PostgreSQL，不是 MySQL**。用 `pgx` + `FOR UPDATE` / `SKIP LOCKED`。隔离级别 **READ COMMITTED + 显式行锁**（不用 SERIALIZABLE，避免序列化重试风暴）。
2. **MQ 是 Redis Streams**（消费组 + XACK + DLQ），不是抽象 MQ。
3. **幂等最终边界 = DB 唯一约束 + `idempotency_key`**。布隆过滤器只能作 L1 快速否决（有假阳性），**不能**作资金幂等真相来源。
4. **资金真相来源 = Dual-Entry Ledger 分录**；余额快照表仅查询/并发控制，改值只能走冲正分录。
5. **事务铁律**：DB 本地事务内禁止任何外部网络 I/O；外部调用走 Outbox → Worker → 二次确认事务。
6. **超时语义**：外部 API 超时 ≠ 失败 → `UnknownExternalState`，禁止盲重试资金提交。
7. **金额**：int64 minor 或 decimal，全链路禁用 float。
8. **Mock/Real 一键切换**：外部依赖经工厂按 `*_MODE` 注入；real 缺 URL 必须启动失败，不得静默回退 mock。

> 每个任务标注 `[已落地]`（勿动/勿重写）与 `[加固缺口]`（本轮要做）。

---

## 任务包一：MAE 四状态机控制器（PRD 第 3 章）

### 现状
- `[已落地]` 六态状态机（含禁止 Cooldown→Active 直连边）、`CanTransition`/`Transition`、`EvaluateCircuitBreaker`、`EvaluateTesting`、路由引擎 `router.go`、测卡滑动窗口 `risk.go`、熔断 tick、`business_day_lock_until` 写入。

### 加固缺口（本轮做）
- **T1-1 探测退出机制**：Testing 态补「探测窗口超时」与「连续灰度失败计数器」。
  - Redis key `MAE_TESTING_FAIL:{mae_id}` 计连续失败次数，`≥3 → Suspended`。
  - 探测窗口超时（超过 `cooldown_minutes` 仍未达 Testing→Active 条件）→ 回 Cooldown。
  - 文件：`internal/mae/domain/domain.go`（纯函数判定）+ `internal/mae/service/service.go`（持久化）。
- **T1-2 探测退出测试**：单测覆盖「连续失败3次→Suspended」「窗口超时→Cooldown」「达标→Active」三分支。

### 设计要点
- 触发判定为纯函数（输入指标快照+阈值，输出 目标态+原因+lockUntil），与持久化解耦。
- Exposure 自然日只增不减，退款/争议独立计数不回扣。
- 状态迁移：DB 乐观锁 `version` 为主，Redis Lock 仅防抖，非资金边界。
- 隔离级别：单行乐观锁 UPDATE，READ COMMITTED 足够，无双花面。

---

## 任务包二：去特征化 Webhook 边缘代理网关（PRD 第 4 章）

### 现状
- `[已落地]` HMAC 验签、幂等键双分支公式、Raw Payload AES-256-GCM 加密、`webhook_ingress_event` 落库、Streams 投递、消费端四类事件真实动账、Ack-after-commit。

### 加固缺口（本轮做）
- **T2-1 验签不可绕过**：无匹配 proxy 行必须**拒绝**（当前会跳过验签）。文件：`internal/webhook/handler/handler.go`。
- **T2-2 RSA_SHA256 验签**：实现并按 `signature_algorithm` 列分支（当前只有 HMAC）。文件：`internal/webhook/domain/domain.go`。
- **T2-3 幂等 L1 加速层**：Redis 对 `provider_event_id` 做 `SET`/布隆快速否决，命中才查库；**DB 唯一约束仍为 L2 最终真相**，假阳性回落查库。文件：`internal/webhook/consumer/consumer.go`。
- **T2-4 DLQ 可触发**：producer 写 `delivery_count`（当前恒为 1，DLQ 永不触发）；补 `XAutoClaim` 回收 pending 的 ReclaimPending ticker。文件：`internal/platform/stream/stream.go` + worker 接线。

### 设计要点
- 代理层禁止直连 Ledger/DB 写。
- 消费端单事务：幂等检查 → Ledger 分录 → 余额快照 → commit → 再 ack。

---

## 任务包三：自适应阶梯式定时归集引擎（PRD 第 5 章）

### 现状
- `[已落地]` 银行窗口判断 `WithinBankingWindow`、受益人四分支、Sweep tick、Delay Queue 入队/消费、`sweep_approval` 迁移表。

### 加固缺口（本轮做）
- **T3-1 阈值语义统一**：触发判定用毛额、转账额用净额（扣 ratio/residual）的 gross/net 不一致，需统一口径，避免净额为 0 仍判 SUBMIT。文件：`internal/treasury/domain/domain.go`。
- **T3-2 四眼复核闭环**：`ApprovalRequired` 必须**持久化为待审批工单**（用已建 `sweep_approval` 表），不可 detect 后丢弃；补审批通过 → 解挂续做的接口与状态流转。文件：`internal/treasury/service/service.go` + handler。
- **T3-3 审批测试**：非同名大额 → 进审批表挂起；审批通过 → 续做提交；拒绝 → 终止。

### 设计要点
- 延迟队列 `SWEEP_DELAY:{rail}:{account}:{currency}` ZSET，score = `next_execution_at`。
- Sweep 提交：分布式锁 + DB 行锁双重防并发重复；`idempotency_key = sweep:{rule}:{date}`。
- 建单（Locked）与提交银行 API 分离；银行超时 → `UnknownExternalState`，禁止盲重试。

---

## 任务包四：本地冻结事务 + Transactional Outbox 原子操作（PRD 第 6-7 章）

### 现状
- `[已落地]` Freeze 本地事务（FOR UPDATE→校验→扣减→分录→Outbox）、Outbox Worker 完整生命周期（Ack/Fail/DeadLetter）、并发双花测试（5笔抢1000，2-3成功、总额守恒）。

### 加固缺口（本轮做）
- **T4-1 退避策略校准**：`FailOutboxEvent` 的 `next_retry_at` 确认为**指数退避**（如 base * 2^retry_count），非固定间隔。文件：`internal/payout/repository/repository.go` + 查询。
- **T4-2 CHECK 约束补全**：迁移补 `reserve_balance ≥ 0`、`dispute_hold_balance ≥ 0` 的 CHECK（当前只有 settled/frozen）。新增迁移文件。
- **T4-3 事务边界静态审计**：写一个测试或 lint 规则，断言 Freeze 事务内无任何 `http`/`lpCli`/`bankCli` 调用（红线自动化守卫）。

### 设计要点（Phase 1 时序，勿改）
单一本地事务严格顺序：FOR UPDATE → 校验 available → 扣 settled/加 frozen → PAYOUT_FREEZE 分录（Dr Settled/Cr Frozen）→ payout_request(BalanceFrozen) → outbox(FX_LOCK_REQUESTED) → COMMIT。
- 外部 LP 调用完全剥离事务外，仅 Worker 消费 Outbox 触发。
- 行级锁 + CHECK 约束 = 双花最终防线。READ COMMITTED + 行锁即可。

---

## 任务包五：异步出金二次确认与对账引擎（PRD 第 7-8 章）

### 现状
- `[已落地]` FXLock/Payout Worker、Confirm/Release 真实动账（幂等+FOR UPDATE+冲正分录）、Reconcile tick。

### 加固缺口（本轮做）
- **T5-1 对账分布式锁**：对账 Worker 扫描 `UnknownExternalState` 时持分布式锁（`LOCK:reconcile:{payout_id}`），防多 Worker 并发查同一单。文件：`internal/worker/reconcile/reconcile.go`。
- **T5-2 人工调单挂账**：`UnknownExternalState` 超过阈值（如 N 次查询或 M 小时）→ 转人工调单工单状态（新增 `manual_review` 标记/表），期间 frozen 绝不释放。新增迁移 + service。
- **T5-3 对账测试**：confirmed/failed/仍 unknown/超阈值转人工 四分支，含金额不符告警。

### 设计要点
- 二次确认幂等：LP `provider_event_id` 派生幂等键，先查 `ledger_transaction` 是否已存在，存在即跳过。
- Confirm 与 Release 互斥，靠状态机 + 幂等键保证只发生一次。
- 每次状态转换 + 动账在独立本地事务内；`UnknownExternalState` 期间释放/核销被状态机前置拦截。

---

## 任务包间耦合契约（接缝，勿破坏）

| 上游 | 接缝（DB 字段/状态/事件） | 下游 |
|------|--------------------------|------|
| 包一 | `mae_configuration.status` + 路由选池 | 收单流量分配 |
| 包二 | `webhook_ingress_event` → Stream 事件 | 包四/五 账务消费 |
| 包三 | `sweep_transfer.status` + `UnknownExternalState` | 包五 对账 |
| 包四 | `transactional_outbox(FX_LOCK_REQUESTED)` + `payout_request.status` | 包五 Worker |
| 包五 | `ledger_transaction` 冲正分录 + 余额快照 | 包二对账事件回流 |

> 各包仅通过 **DB 字段 + 状态机 + Outbox 事件** 通信，不跨包直接函数调用资金逻辑。

---

## 执行纪律（强制）

1. **增量不重写**：标 `[已落地]` 的勿动；只做 `[加固缺口]` 的 T 编号任务。
2. **禁止 stub/谎报/只建函数不接线/假接入（依赖在 go.mod 但代码零调用）/建表不写迁移**。
3. 每个 T 任务补真实测试（断言真实行为，资金类断言借贷平衡 + 余额守恒）。
4. 交付前本地实跑：`go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1`，亲眼确认零 FAIL/零 panic/零 SKIP，贴完整输出。
5. 完成后更新 PROJECT_STATUS.md（逐项真实标注）与 MEMORY.md。

---

## 建议执行顺序

资金安全优先：**包四 → 包五 → 包二 → 包三 → 包一**
（先夯实冻结/对账/幂等的资金红线，再补 Webhook 韧性，最后做路由探测精度。）
