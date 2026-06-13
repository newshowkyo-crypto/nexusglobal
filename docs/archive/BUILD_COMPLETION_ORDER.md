# NexusGlobal MAMR-System 收尾完工执行令（BUILD COMPLETION ORDER）

> 给负责开发的 AI（mimo / codex）的唯一行动文件。
> 本文件优先级高于 PROJECT_STATUS.md。PROJECT_STATUS.md 曾谎报“全部完成”，已作废，以本文件为准。
> 目标：把资金闭环后半程的空壳实现补成真实可用、可验收的代码。

---

## 0. 工作纪律（必须严格遵守）

1. **做完所有任务之前，禁止向人类提问。** 遇到歧义，按本文件的规格和 PRD.md 的硬约束自行决策，并在 `PROGRESS.md` 记录决策理由。
2. **自驱动循环：** 选任务 → 写代码 → 写测试 → 构建 → 自测 → 失败则定位根因修复 → 通过 → 更新进度 → 下一个任务。循环直到 §6 的全部验收门禁通过。
3. **同一问题失败 2 次后，停止打补丁，分析根因，换方案。** 把根因和新方案写入 `PROGRESS.md`。
4. **禁止用“改状态字段”冒充“资金动账”。** 任何资金动作必须写真实的 `ledger_transaction` + 双轨 `ledger_entry`，并同步 `merchant_treasury_ledger` 余额快照。
5. **禁止删除/弱化测试断言来让测试变绿。** 测试必须真实反映 PRD §6/§7 的资金规则。
6. **每完成一个任务，运行该任务的“验收命令”，全绿后才能标记完成。**
7. 所有金额计算禁止使用 `float32/float64`。展示层可转字符串，计算层用 `shopspring/decimal`（法币/账户）或 `int64` minor（额度/sweep）。
8. 不得在数据库本地事务内执行任何外部网络 I/O（HTTP/gRPC/LP/Bank/MAE）。外部调用一律走 Outbox + Worker。
9. 不得让 webhook 接收层直连账务库；账务只在核心消费者侧发生。

---

## 1. 当前真实状态基线（核查结论）

可信、已完成、质量合格的部分（不要重写，只在其上扩展）：

- 平台层：`internal/platform/*`（config/postgres/redis/crypto/money/clock/logger/apperr/idempotency/stream/httpx）。
- 全部迁移：`migrations/000001..000006`，表结构与 PRD 基本一致。
- `ledger.Service.Post`：借贷平衡校验 + 幂等 + 本地事务，健全，可直接复用。
- `payout.Service.Freeze`：Phase 1 本地冻结事务（FOR UPDATE + 校验 + 余额更新 + 双轨分录 + Outbox），正确，已被集成测试覆盖。
- `fxlock` worker：Phase 2 FX Lock，事务外调 LP、超时→UnknownExternalState、失败→Failed，逻辑正确。
- MAE：`mae.Service.ChangeStatus` 真接入 `CanTransition`；熔断/Testing 评估存在。状态机本体可用。
- `payout/domain.CleanAddress`：地址清洗（trim/去不可见字符/校验/hash/mask）已实现，可直接调用。

空壳 / 造假 / 缺失的部分（本文件要修复的全部对象）：见 §2 任务清单。

---

## 2. 任务清单（按依赖顺序执行，逐个完成）

### FIX-01　Payout Confirm 真实动账（最高优先级）
**文件：** `internal/worker/payout/payout.go`、`internal/payout/repository/repository.go`、`internal/payout/repository/queries.sql`（如需新查询）

**现状：** `Worker.Confirm()` 只调 `UpdatePayoutRequestStatus` 改状态，不动账，导致 `frozen_payout_balance` 永不归零（资金黑洞）。

**要做：** 严格按 PRD §7.4，在**一个本地事务**内完成：
1. 幂等校验：用 LP 事件标识构造 ledger 幂等键 `payout_confirm:{payout_request_id}`，若 `ledger_transaction` 已存在该 key，直接返回（幂等重放，不重复动账）。
2. `SELECT payout_request FOR UPDATE`，校验状态 ∈ {LPProcessing, UnknownExternalState}，否则按幂等/非法转换处理。
3. `SELECT merchant_treasury_ledger FOR UPDATE`（merchant_id+currency）。
4. 写 `ledger_transaction`（source_event_type/transaction_type = `PAYOUT_CONFIRM`）+ 双轨分录：
   - `Dr FROZEN_PAYOUT`，`Cr EXTERNAL_PAYOUT`（金额 = payout amount）。
5. 更新余额快照：`frozen_payout_balance -= amount`，重算 `available_payout_balance = settled - reserve - frozen - dispute`，`version += 1`。
6. `payout_request.status = Confirmed`（经 `CanTransition` 校验）。
7. 全程零外部 I/O。

**注意：** `payout_request` 必须能取到 `merchant_id / amount / fiat_currency` 用于动账；确认 `GetPayoutRequest` 返回这些字段，缺则补查询与 model。

---

### FIX-02　Payout Release 真实动账 + 触发链路闭合
**文件：** `internal/worker/payout/payout.go`、`cmd/worker/main.go`

**现状：** `releaseFrozen` 只把状态置为 `Failed` 就停下；`Release()` 要求状态为 `Failed` 才执行，但没有任何代码在 Failed 后调用它 → 冻结资金永远不退回。

**要做：**
1. `Release()` 严格按 PRD §7.5，在一个本地事务内：
   - 幂等键 `payout_release:{payout_request_id}`，已存在则幂等返回。
   - `SELECT payout_request FOR UPDATE`，校验状态允许释放（Failed）。
   - `SELECT merchant_treasury_ledger FOR UPDATE`。
   - 写 `ledger_transaction`（`PAYOUT_RELEASE`）+ 双轨分录 `Dr FROZEN_PAYOUT` / `Cr SETTLED`。
   - `frozen_payout_balance -= amount`，`settled_balance += amount`，重算 available，`version += 1`。
   - `payout_request.status = BalanceReleased`。
2. **链路闭合：** LP 明确失败时（fxlock 阶段或 payout 阶段判定 Failed），必须在同一处理链上紧接着调用 `Release()` 完成资金退回，不允许停在 Failed 死等。
3. `cmd/worker/main.go`：删除 `log.Info("reconcile tick (placeholder)")` 占位，注册 Confirm / Release / Reconcile 的真实触发入口（通过 LP webhook 消费事件或 outbox 事件驱动，见 FIX-03 与 FIX-06）。

---

### FIX-03　Webhook 核心消费者真实动账 + 修复 nil 注入
**文件：** `internal/webhook/consumer/consumer.go`、`cmd/worker/main.go`

**现状：**
- `handleFinancialEvent` 构造的 `LedgerPostCmd` 没有 `Entries`、没有金额、没有 account → 只会写空交易。
- `cmd/worker/main.go` 第 ~79 行 `consumer.New(rdb, nil, log)` 传了 `nil` ledger service，真实处理会 panic。

**要做：**
1. 消费者从消息（或据 ingress_id 回查 `webhook_ingress_event` 解密 payload）解析出 `merchant_id / amount / currency / provider_event_id`。
2. 按事件类型映射真实双轨分录（遵循 PRD §6 资金口径），至少实现：
   - `captured`：商户 `Dr EXTERNAL/Clearing` `Cr PENDING`（资金入账到 pending），`pending_balance += amount`。
   - `settled`：`Dr PENDING` `Cr SETTLED`，`pending_balance -= amount; settled_balance += amount`。
   - `refund` / `dispute`：按 PRD 口径生成对应分录与 dispute_hold（refund 冲减、dispute 进 `dispute_hold_balance`）。
   - `payout_confirmed` / `lp_callback`：转交给 FIX-01 的 Confirm 逻辑（按 provider_ref 找到 payout_request）。
3. 幂等：以 webhook idempotency_key / provider_event_id 作为 ledger 幂等键，重复事件不重复动账。
4. 状态机单调校验后再入账。
5. **ACK 时机：** 必须在账务事务 commit 成功之后再对 Redis Stream 执行 XAck；失败则不 ACK，留待重试/进 DLQ（PRD §4.9）。
6. `cmd/worker/main.go`：把 `nil` 替换为真实构造的 ledger service 实例（用 ledger repository + txm + pool 装配），并把 payout Confirm 依赖也注入消费者。

---

### FIX-04　Treasury Sweep 读真实金额 + 真实计算 + 对账动账
**文件：** `internal/treasury/service/service.go`、`internal/treasury/repository/*`、必要时新增 cash position 查询

**现状：** `Evaluate` 写死 `AccumulatedMinor: 0`，转账额直接用 `rule.TriggerThresholdMinor`；`Reconcile` 只改状态。Sweep 是空壳。

**要做：**
1. 从来源银行账户的真实可归集累计金额读取 `AccumulatedMinor`（新增账户余额/现金头寸查询；若暂无落地的 cash position 表，新增 `bank_account_balance` 或等价查询并在迁移中补表，记录于 PROGRESS）。
2. 实际转账金额按规则计算：`amount = min( floor(accumulated * sweep_ratio_bps/10000), max_single_sweep_minor )`，且 `accumulated - amount >= min_residual_minor`，否则不扫或调整额度。禁止直接用 trigger_threshold 当转账额。
3. 触发判定继续遵守：银行工作窗口 + Cut-off + `time_window_matrix` + `max_daily_sweep_count` + 受益人规则（UNKNOWN reject、非同名大额 ApprovalRequired）。
4. `Reconcile` 补齐归集确认后的账务处理与状态推进（Submitted/UnknownExternalState → Confirmed/Reconciled），超时不盲重试。
5. 非窗口期：只累计或进 Redis Delay Queue，绝不调银行 API（已部分实现，保留并确保金额真实）。

---

### FIX-05　出金地址真正落库 + 清洗 + 加密 + 名单校验
**文件：** `internal/payout/service/service.go`（`CreateAddress`）、`internal/payout/repository/*`、`internal/payout/handler/handler.go`

**现状：** `CreateAddress` 只 `uuid.New()` 返回，`_ = cmd` 丢弃全部入参，不落库、不清洗、不加密。

**要做：**
1. `CreateAddressCmd` 增加 `MerchantID`（handler 从鉴权上下文/请求获取并传入）。
2. 调用已存在的 `domain.CleanAddress`：trim、去不可见字符、按 `target_network` 正则校验（TRON_TRC20 / ETH_ERC20 / BTC / SOL），生成 normalized address、`target_address_hash`、`target_address_masked`。校验失败返回 `400 INVALID_ARGUMENT`。
3. 校验 `address_format_type` 与 `target_network` 一致。
4. 地址明文用 `crypto.Encryptor`（AES-256-GCM）加密为 `target_address_ciphertext` 后写 `merchant_payout_address` 表；`screening_status=Pending`，`whitelist_status=Pending`。
5. 黑/白名单 hash 匹配：命中黑名单直接拒绝；提供白名单查询接口/字段。
6. 返回真实 `id / target_address_masked / screening_status / whitelist_status`。

---

### FIX-06　清理假端点与死代码、补齐 reconcile worker
**文件：** `internal/payout/handler/handler.go`、`internal/treasury/handler/handler.go`、`internal/webhook/service/service.go`、`internal/worker/reconcile/reconcile.go`、`test/integration/integration_test.go`、`internal/platform/money/*`

**要做：**
1. `payout/handler` 的 `FxLock/Submit/Reconcile` 与 `treasury/handler` 中返回写死 `{"status":"FxLocked"/"Confirmed"}` 的假端点：要么接真实 service 真正改库，要么标注为 worker-only 并从对外路由移除。不允许返回与数据库不一致的假 JSON。
2. `internal/webhook/service/service.go` 为 0 字节空文件：删除该文件（及其空包引用），或补成真实实现。
3. `reconcile.go` 的 confirmed/failed 分支当前只改状态：改为复用 FIX-01/FIX-02 的真实动账逻辑（Confirmed→走 Confirm 动账；failed→走 Release 动账）。在 worker 主循环注册定时对账（扫描 UnknownExternalState 的 payout/sweep）。
4. 删除 `integration_test.go` 中未使用的 `decimalToPgNumeric`（死代码）。
5. 把分散在 `payout/repository`、`worker/payout`、`worker/fxlock` 的重复 `numericToDecimal` 抽到 `internal/platform/money` 统一一份，全部改为引用它。

---

### FIX-07　补齐缺失的 PRD 能力（如时间允许，按此顺序）
> 这些在 PRD 有要求但当前完全缺失。按优先级实现；若某项确无法在本轮完成，必须在 PROJECT_STATUS.md 如实标注“未实现”，不得谎报。

1. **测卡攻击防御 / risk-engine（PRD §9.1）**：实现触发条件（invalid_card_rate_60s、unique_card_count_per_ip_60s、decline_count_per_tenant_60s、card_testing_count_per_mae_60s）的 Redis 滑窗计数与判定，命中则标记 RiskRejected 并触发 MAE Cooldown。
2. **Rolling Reserve / reserve-service（PRD §6）**：实现 reserve hold 的分录与 `reserve_balance` 维护。
3. **metric-aggregator（PRD §3.6）**：Redis Counter → 滑窗聚合 → `mae_runtime_metric_snapshot` 落库；exposure 只 INCRBY，refund/dispute 独立计数，不回扣 exposure。

---

### FIX-08　集成测试补全（验收的核心证据）
**文件：** `test/integration/integration_test.go`（或新增测试文件）

新增以下测试，全部使用 testcontainers 真实 PG + Redis，**必须包含真实断言**：

1. `TestPayoutConfirmSettlesFrozen`：Freeze → 推进到 LPProcessing → Confirm，断言：`frozen_payout_balance` 归零、`PAYOUT_CONFIRM` 交易存在且借=贷、`available_payout_balance` 正确、状态=Confirmed。
2. `TestPayoutReleaseRefundsFrozen`：Freeze → Failed → Release，断言：`settled_balance` 恢复到原值、`frozen_payout_balance` 归零、`PAYOUT_RELEASE` 借=贷、状态=BalanceReleased。
3. `TestPayoutConfirmIdempotent`：同一确认事件投递两次，断言动账只发生一次（交易数=1，余额只变一次）。
4. `TestWebhookCapturedPostsLedger`：captured/settled webhook 事件 → 断言对应 `ledger_transaction` + 2 条分录真实生成、`pending/settled` 余额按口径变化。
5. `TestSweepRealAmount`：给定真实累计金额与 ratio，断言 sweep_transfer 金额 = 按 ratio/上限/残留计算的真实值，而非 trigger_threshold。
6. `TestConcurrentDoubleSpend` 增强：在原断言上追加——所有成功 Freeze 的分录借=贷合计相等，且 `settled+frozen+reserve+dispute` 守恒。
7. 通用断言工具：实现 `assertLedgerBalanced(t, pool, idempotencyKey)`（借方合计=贷方合计）与 `assertTreasuryConserved(t, pool, merchantID, currency, expectedTotal)`，在以上测试中复用。

---

## 3. 资金口径与分录映射（实现时严格遵循 PRD §6.2 / §7）

```
available_payout_balance = settled_balance - reserve_balance - frozen_payout_balance - dispute_hold_balance

Freeze   : Dr SETTLED        / Cr FROZEN_PAYOUT     ; settled-=a; frozen+=a   (已实现, 勿改)
Confirm  : Dr FROZEN_PAYOUT  / Cr EXTERNAL_PAYOUT   ; frozen-=a               (FIX-01)
Release  : Dr FROZEN_PAYOUT  / Cr SETTLED           ; frozen-=a; settled+=a   (FIX-02)
Captured : Dr CLEARING/EXT   / Cr PENDING           ; pending+=a              (FIX-03)
Settled  : Dr PENDING        / Cr SETTLED           ; pending-=a; settled+=a  (FIX-03)
```
每笔动账：先 `SELECT merchant_treasury_ledger FOR UPDATE`，再写分录，再更新快照，最后 `version+=1`。重算 available。借贷必须平衡（复用 `ledger/domain.ValidateBalanced`）。

---

## 4. 红线自查（每个任务完成后逐条核对，禁止口头宣称）

| 红线 | 验证方式 |
| --- | --- |
| 事务内无外部 I/O | grep 资金事务函数体，确认无 lp./bank./mae./http 调用 |
| 外部调用经 Outbox/Worker | Confirm/Release/FXLock/Payout 均在 worker 侧、事务外 |
| 超时≠失败 | timeout→UnknownExternalState 且不释放 frozen |
| 先冻结后外呼 | Freeze 事务 commit 后才有 outbox 事件 |
| 余额行级锁 | 所有动账走 SELECT...FOR UPDATE |
| exposure 只增 | metric 仅 INCRBY，refund/dispute 独立 |
| webhook 不连账务 | webhook handler 不 import ledger/不写账务库 |
| 强幂等 | 每个资金动作有 ledger 幂等键 + DB 唯一约束 |
| 非窗口不调银行 | Sweep 非窗口期只累计/Delay |
| 借=贷 | ValidateBalanced 在每笔交易生效，且有测试断言 |
| 敏感字段加密 | 地址/凭证密文存储，日志无明文 |
| 无 float | grep 资金路径无 float32/float64 |

---

## 5. 自驱动开发循环（每个任务都跑一遍）

```
1. 读取本文件 + PROGRESS.md，选下一个未完成任务
2. 读相关源码与 PRD 对应章节
3. 写/改实现代码
4. 写/改该任务的集成与单元测试（含真实断言）
5. 运行验收命令（§6）
6. 失败：定位根因 → 修复 → 回到 5（同一问题失败 2 次则换方案并记录）
7. 通过：更新 PROGRESS.md（标记完成 + 关键决策）+ 更新 MEMORY.md（上下文指针）
8. 回到 1，直到所有任务完成且 §6 全门禁通过
```

---

## 6. 全局验收门禁（全部通过才算完工）

按顺序执行，任一失败则回到开发循环修复，**不得跳过、不得削弱**：

```powershell
go build ./...
go vet ./...
gofmt -l .            # 必须无输出
go test ./... -short  # 全部包通过
go test ./test/integration/...   # testcontainers 集成测试全部通过
```

外加人工等价核对（自查）：
1. FIX-01..FIX-06 全部完成且对应测试存在并通过。
2. §4 红线表逐条满足。
3. §8（FIX-08）的 7 项测试存在且断言真实（断言余额守恒与借贷平衡，而非仅断言状态字符串）。
4. 不存在返回与数据库不一致的假端点；无 0 字节空文件；无死代码 `decimalToPgNumeric`；`numericToDecimal` 已统一。
5. 重写 `PROJECT_STATUS.md`：逐项写真实完成度，明确列出 FIX-07 中未实现项为“未实现”。**禁止再出现“全部完成/可联调”等未经测试证明的措辞。**

---

## 7. 完工定义（Definition of Done）

当且仅当满足以下全部条件，方可停止并产出最终报告：

- §2 的 FIX-01 ~ FIX-06 全部真实实现并通过各自测试。
- FIX-08 的全部集成测试通过，且断言真实反映 PRD §6/§7 资金规则。
- §6 全局验收门禁全绿。
- FIX-07 各项：已实现的通过测试；未实现的在 PROJECT_STATUS.md 如实标注。
- PROGRESS.md 记录了全过程关键决策；PROJECT_STATUS.md 已据实重写。

完成后输出一份简短最终报告：列出每个 FIX 的真实状态、新增测试清单、验收命令输出摘要、以及 FIX-07 中仍未实现的项（如有）。报告中不得包含未经测试验证的结论。
```
