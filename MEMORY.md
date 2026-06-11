# MEMORY — 上下文记忆文件（每轮必读必写）

> 版本：V1.0
> 用途：防止 context 压缩 / 换会话后丢上下文。AI 每轮**开始必读**、**结束必写**。
> 与 PROGRESS.md 配合：PROGRESS 是任务看板（权威），MEMORY 是"为什么"与"怎么避坑"的记忆库 + 进度指针。
> 写作原则：记**决策理由、坑的根因与解法、约定、指针**，不流水账。

---

## 0. 进度指针（最重要，先看这里）

- **下一个要做的任务**：`T016`（Payout service 冻结事务 + Outbox）
- **当前阶段**：P2 Payout（T013-T015 完成，T016-T018 待做）
- **上一轮做了什么**：T015 完成——queries.sql（LockBalanceForUpdate FOR UPDATE、UpdateBalance、InsertPayoutRequest、GetPayoutRequest、UpdatePayoutRequestStatus、InsertOutboxEvent、ClaimOutboxBatch SKIP LOCKED、AckOutboxEvent、FailOutboxEvent）；repository.go（Balance struct、LockBalanceForUpdate、UpdateBalance、InsertPayoutRequest、GetPayoutRequest、InsertOutboxEvent、ClaimOutboxBatch）；sqlc.yaml 更新支持多模块。
- **恢复方式**：读本节 → 读 PROGRESS 看板 → 按 AGENT_WORKFLOW STEP 1 选最低编号「待办且依赖已完成」任务。

---

## 1. 文档地图（先读哪份）

| 想知道 | 看哪份 |
| --- | --- |
| 技术栈/目录/表/流程伪代码/MQ/Mock | `TDD.md` |
| 不可违反的红线 + 正反例 | `CONSTRAINTS.md`（最高优先级） |
| 接口契约/错误码/幂等 | `API.md` |
| 代码风格/金额/错误/事务/DI | `CODING_STANDARDS.md` |
| 做什么、什么顺序、DoD | `TASKS.md` |
| 怎么循环开发/卡住怎么办/命令 | `AGENT_WORKFLOW.md` |
| 任务进度看板 | `PROGRESS.md` |
| 本记忆 | `MEMORY.md` |

冲突优先级：**CONSTRAINTS > TDD > API/CODING_STANDARDS > TASKS**。PROGRESS 看板 > MEMORY 指针（不一致时以 PROGRESS 为准并修正本文件）。

---

## 2. 已锁定的架构决策（不要推翻；要改先在 §4 登记）

1. **架构**：Go 模块化单体，单仓多二进制（`cmd/api`、`cmd/worker`、`cmd/migrate`）。
2. **技术栈**：Go 1.22 / PostgreSQL 15 / Redis 7。
3. **库选型**：chi（HTTP）、pgx/v5 + sqlc（DB，显式 SQL，**不用 ORM**）、golang-migrate、go-redis/v9、shopspring/decimal、google/uuid、caarlos0/env、testify、testcontainers-go、httprate、标准库 slog。
4. **MQ = Redis Streams**（消费组 + XAck + XAutoClaim 重投 + DLQ stream），**不引入 Kafka/RabbitMQ**。
5. **金额双轨**：MAE 指标/计数用 `int64` minor unit（对齐 Redis INCRBY）；账本余额用 `decimal.Decimal`（PG numeric(20,4)）。**全局禁用 float**。
6. **外部依赖全 Mock**：LP/Bank/MAE 抽象为 `pkg/external/*` 接口，MVP 只交付确定性 Mock，本地/CI 零触网。KMS 也用 MockKMS。
7. **依赖注入**：手工构造函数注入，无 DI 框架；装配集中在 `cmd/*/main.go`。
8. **时间**：业务代码不直接 `time.Now()`，统一走 `clock.Clock` 接口（测试可注入）。
9. **事务边界**：只在 service 层用 `TxManager.WithinTx`；回调内纯 DB，**严禁外部 I/O**（RED-01）。
10. **资金安全边界**：DB 本地事务 + 行级锁(FOR UPDATE) + 唯一约束 + 幂等键；Redis 非资金边界。
11. **开发顺序取舍**：先 P0 平台 → P1 Ledger（资金真相地基）→ P2 Payout → P3 MAE → P4 Webhook → P5 Treasury → P6 收尾。Ledger 必须早于 Payout。
12. **module 名**：`github.com/nexusglobal/mamr`。

---

## 3. 已踩坑与解法（持续追加）

> 格式：现象 → 根因 → 解法 → 关联任务/红线

（暂无——开始编码后在此累积。示例占位：）
- _示例：sqlc 对 numeric 生成 string 而非 decimal → 根因 sqlc 默认映射 → 解法 sqlc.yaml overrides 指定 shopspring/decimal，必要时 repository 手写 pgx 包裹 → T011。_

---

## 4. 未决问题 / 需要决策（受阻或拿不准时写这里）

> 格式：编号 | 问题 | 背景 | 已试方案 | 倾向方案 | 是否需要用户决策

（暂无）

> 规则：若某需求看似要求违反 CONSTRAINTS 红线，**停止编码**在此登记，按红线从严处理，不擅自绕过。

---

## 5. 关键约定速记（高频易错点）

- **金额**：JSON 进出用字符串；内部 int64/decimal；`decimal.NewFromFloat` 在金额路径禁用。
- **幂等键 UNIQUE 必建**：webhook_ingress_event、ledger_transaction、transactional_outbox、payout_request、sweep_transfer。
- **exposure 只 INCRBY**，refund/dispute 独立计数，**永不 DECRBY exposure**（RED-06）。
- **状态机无 `Cooldown→Active` 边**（RED-08）；Exposure 出池设 `business_day_lock_until`，当日不回 Active（RED-07）。
- **超时 = UnknownExternalState ≠ Failed**，不盲重试，frozen 不释放（RED-03）。
- **Payout 顺序**：先本地事务冻结(FOR UPDATE)→COMMIT→Worker 事务外调 LP（RED-04）。
- **Webhook handler 只投 Streams**，不 import ledger/payout repository（RED-09）。
- **余额变更必有成对借贷分录，借=贷**；修正走冲正（RED-13）。
- **敏感字段**：AES-256-GCM 加密入库 + struct tag `json:"-"`；日志/响应只用 masked/hash（RED-15）。
- **改 queries.sql → make sqlc**；**改 migrations → make migrate**。
- **提交前 make ci + make redlines 必过**。

---

## 6. 环境与命令备忘

- 起依赖：`make up`；迁移：`make migrate`；生成查询：`make sqlc`。
- 开发循环默认：`make build` + `make test-short`；集成：`make up` 后 `make test`。
- 红线自查：`make redlines`；提交总检：`make ci`。
- `.env`：`cp .env.example .env`（键见 TDD §10）。
- 卡住重置环境：`make down && make up && make migrate`。

---

## 7. 本轮工作区（临时草稿，可随时覆盖）

> 当前任务的子步骤清单、临时笔记放这里，做完即清。

- [ ] （T001 开始时在此列子步骤）

---

## 8. 完成状态

- 项目状态：T001 完成，T002 待开始。
- 完成标志：PROGRESS 中 T001–T037 全 ✅ 且 T038 收尾通过 → 在此写 `PROJECT COMPLETE` 并输出总结。
