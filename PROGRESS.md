# PROGRESS — 任务状态看板（断点续传）

> 版本：V1.0
> 用途：每完成一个任务即更新本表。AGENT_WORKFLOW STEP 1 从这里选下一个「待办且依赖已完成」的最低编号任务。
> 状态图例：⬜ 待办 / 🟦 进行中（同时仅一个）/ ✅ 已完成 / ⛔ 受阻
> 进度指针以 `MEMORY.md` 为准，本表为权威看板（冲突时以本表为准并修正 MEMORY）。

---

## 快照

- 当前进度指针：**T023**
- 已完成：22 / 38
- 进行中：无
- 受阻：无
- 最近更新：T022 MAE repo+service 完成

---

## 看板

### P0 脚手架与平台基础设施
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T001 项目骨架 | ✅ | — | 2026-06-12 | ccd8406 | |
| T002 config | ✅ | T001 | 2026-06-12 | 6c1589c | |
| T003 postgres+TxManager | ✅ | T002 | 2026-06-12 | a9aaf1e | |
| T004 redis | ✅ | T002 | 2026-06-12 | f1de7b6 | |
| T005 money | ✅ | T001 | 2026-06-12 | 03f74af | |
| T006 clock/logger/apperr/idempotency | ✅ | T002 | 2026-06-12 | | |
| T007 crypto+MockKMS | ✅ | T002 | 2026-06-12 | | |
| T008 external mock+validator | ✅ | T001 | 2026-06-12 | | |

### P1 Ledger 双轨账本
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T009 ledger migration | ✅ | T003 | 2026-06-12 | | |
| T010 ledger domain | ✅ | T005 | 2026-06-12 | | |
| T011 ledger repository | ✅ | T009,T010 | 2026-06-12 | | |
| T012 ledger service | ✅ | T011,T006 | 2026-06-12 | | |

### P2 Payout 冻结 + Outbox + Worker
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T013 payout/outbox migration | ✅ | T003 | 2026-06-12 | | |
| T014 payout domain | ✅ | T008 | 2026-06-12 | | |
| T015 payout repository | ✅ | T013,T014 | 2026-06-12 | | |
| T016 payout freeze service (Phase1) | ✅ | T015,T012 | 2026-06-12 | | 并发双花测试 |
| T017 outbox+fxlock worker (Phase2) | ✅ | T016,T008,T004 | 2026-06-12 | | |
| T018 lp payout worker (Phase3/4/5) | ✅ | T017 | 2026-06-12 | | 五阶段闭环 |

### P3 MAE 状态机与路由
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T019 mae migration | ✅ | T003 | 2026-06-12 | | |
| T020 mae domain (状态机) | ✅ | T005 | 2026-06-12 | | 无 Cooldown→Active |
| T021 mae metric (Redis) | ✅ | T004,T005 | 2026-06-12 | | exposure 只增 |
| T022 mae repo+service | ✅ | T019,T020 | 2026-06-12 | | |
| T023 mae 熔断+Testing | ⬜ | T021,T022 | | | 当日锁 |
| T024 mae handler | ⬜ | T022,T023 | | | |

### P4 Webhook Receiver + Streams MQ
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T025 stream MQ 封装 | ⬜ | T004 | | | |
| T026 webhook migration | ⬜ | T003 | | | |
| T027 webhook domain+repo (验签) | ⬜ | T026,T007,T006 | | | |
| T028 webhook handler (只投MQ) | ⬜ | T027,T025 | | | 不连账务 |
| T029 核心消费者 (入账) | ⬜ | T028,T012,T018 | | | 不重账 |

### P5 Treasury 银行清算 + Sweep
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T030 treasury migration | ⬜ | T003 | | | |
| T031 treasury domain (窗口/受益人) | ⬜ | T005 | | | |
| T032 treasury repo+DelayQueue | ⬜ | T030,T031,T004 | | | |
| T033 sweep service+scheduler | ⬜ | T032 | | | 非窗口不调银行 |
| T034 treasury handler | ⬜ | T033 | | | |

### P6 收尾 / 端到端 / 运维
| 任务 | 状态 | 依赖 | 完成时间 | 提交号 | 备注 |
| --- | --- | --- | --- | --- | --- |
| T035 cmd 装配+中间件 | ⬜ | T024,T029,T034,T018 | | | |
| T036 端到端集成测试 | ⬜ | T035 | | | PRD §12 验收 |
| T037 对账 worker | ⬜ | T018,T033 | | | |
| T038 文档校验+红线自查 | ⬜ | 全部 | | | 收尾 |

---

## 受阻清单（⛔）

> 格式：T0xx | 受阻时间 | 现象 | 已试方案 | 需要的决策（详情写 MEMORY 未决问题）

（暂无）

---

## 完成日志（按时间倒序）

> 格式：YYYY-MM-DD HH:MM | T0xx | 简述 | 提交号

（暂无）
