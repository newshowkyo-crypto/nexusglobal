# CONSTRAINTS — 硬约束红线清单（资金安全最高优先级）

> 版本：V1.0（对应 PRD V1.5 第 15 章 + 第 10 章）
> 性质：**AI 绝对不能违反的红线**。任何代码、任何任务、任何"优化"都不得突破。
> 优先级：本文档 > TDD.md > 其他文档。当任意文档与本文件冲突，**以本文件为准**。
> 用法：每轮开发**前**必读本文件；每次写完 service / 事务 / 外部调用相关代码后，用 §17 自查表逐条核对再提交。

---

## 0. 使用方式（给 AI）

1. 本文件每条红线含：**规则 / 原因 / ❌错误写法 / ✅正确写法 / 自查命令**。
2. 写 `service` 层、事务、外部调用、金额、状态机时，必须先定位相关红线再动手。
3. 提交前运行 §17 的 grep 自查命令，**有任一命中即视为违规，必须修复后才能提交**。
4. 若某需求看似要求违反红线，**停止编码**，在 `MEMORY.md` 的"未决问题"登记并标注冲突，不得擅自绕过。

---

## RED-01：DB 本地事务内禁止任何外部网络 I/O 【最高红线】

**规则**：数据库本地事务（`BEGIN...COMMIT` / `pgx.Tx` 作用域）内，**禁止**出现任何 LP / Bank / MAE API 调用、HTTP、gRPC、外部 Redis 阻塞调用、消息发送等不可控耗时 I/O。

**原因**：外部请求可能阻塞数秒至数十秒 → 长事务持有行锁/连接/快照 → 高并发下连接池耗尽 → 死锁范围扩大 → 核心账务级联阻塞。

**❌ 错误写法**：

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
repo.UpdateLedger(ctx, tx, ...)          // DB
lpClient.FXLock(ctx, req)                // ❌ 事务内外部 I/O
lpClient.Payout(ctx, req)                // ❌
tx.Commit(ctx)
```

**✅ 正确写法**（本地事务只写库 + 写 Outbox，提交后由 Worker 外呼）：

```go
// service: 本地事务内只做 DB 操作 + 写 Outbox
err := txm.WithinTx(ctx, func(tx pgx.Tx) error {
    if err := repo.FreezeBalance(ctx, tx, ...); err != nil { return err }
    if err := repo.InsertLedger(ctx, tx, ...); err != nil { return err }
    return repo.InsertOutbox(ctx, tx, OutboxEvent{Type: "FX_LOCK_REQUESTED"})
})
// COMMIT 之后，Worker 才在事务外调用 LP
```

**自查命令**：

```bash
# service 事务回调内不得出现外部 client 调用
rg -n "WithinTx|Begin\(ctx\)" internal --type go    # 定位事务块，人工核对块内无 lpClient./bankClient./http.
rg -n "lpClient\.|bankClient\.|maeClient\.|http\.(Get|Post|Do)" internal/*/service
```

---

## RED-02：外部调用必须经 Outbox / Worker / Saga

**规则**：所有 LP / Bank / MAE 外部 API 调用，必须由异步 Worker 在事务外执行，触发源是 `transactional_outbox` 或 Redis Streams 事件，**禁止**在同步请求处理链（handler→service→事务）里直接外呼。

**原因**：保证 RED-01；提供重试、幂等、可观测、可恢复。

**❌**：handler 里直接 `lpClient.Payout(...)`。
**✅**：handler→service 写 Outbox(Pending) 并返回；worker 抢占 Outbox 后调用 LP。

**自查**：`rg -n "lpClient\.|bankClient\." internal/*/handler internal/*/service` 应为空（仅 `internal/worker` 与 mock 测试允许）。

---

## RED-03：外部 API 超时 ≠ 失败，禁止盲重试资金提交

**规则**：LP / Bank / MAE 调用超时一律映射为 `UnknownExternalState`，**不得**当作 Failed，**不得**盲目重复提交资金类请求。必须通过查询接口 / 等待 Webhook / 人工对账确认最终状态。

**❌**：

```go
res, err := lpClient.Payout(ctx, req)
if err != nil {                  // ❌ 把 timeout 也当失败
    releaseFrozen()              // ❌ 可能 LP 已成功，导致重复出金/超卖
}
```

**✅**：

```go
res, err := lpClient.Payout(ctx, req)
switch {
case errors.Is(err, lp.ErrTimeout):
    setStatus(payout, "UnknownExternalState")   // frozen 保持不动
case errors.Is(err, lp.ErrExplicitFailure):
    setStatus(payout, "Failed")                 // 触发释放
case err == nil:
    setStatus(payout, "LPProcessing")
}
```

**自查**：`rg -n "ErrTimeout|UnknownExternalState" internal/worker` 必须存在；release/retry 分支不得在 timeout 路径触发。

---

## RED-04：Payout 必须先本地事务冻结，再异步锁价，再提交 LP

**规则**：Payout 第一步必须 `SELECT merchant_treasury_ledger ... FOR UPDATE` 并在本地事务内完成冻结（`settled -= amount`，`frozen += amount`）+ 双轨分录 + 写 Outbox；冻结失败**禁止**调用 LP。顺序不可颠倒。

**原因**：先冻结后外呼是防双花与防超卖的根本。

**✅** 见 TDD §5.3 Phase1。
**自查**：`payout/service` 冻结函数必须含 `FOR UPDATE`；`rg -n "FOR UPDATE" internal/payout/queries.sql` 必须命中冻结查询。

---

## RED-05：余额校验与扣减必须用行级锁，禁止"读-改-写"竞态

**规则**：余额变更必须 `SELECT ... FOR UPDATE` 锁定该商户币种余额行后再校验、再更新；**禁止**先普通 SELECT 读出、应用层判断、再 UPDATE 的非原子模式。并发 5 笔相同余额请求只允许余额覆盖范围内成功，**绝不超卖、绝不负余额**。

**❌**：

```go
bal := repo.GetBalance(ctx, merchantID)        // 普通读
if bal.Available.GreaterThanOrEqual(amount) {  // ❌ 此刻其他事务可能已扣
    repo.UpdateBalance(ctx, merchantID, ...)    // ❌ 竞态超卖
}
```

**✅**：单事务内 `FOR UPDATE` → 校验 → 更新，配合 `CHECK (settled_balance >= 0)`、`CHECK (frozen_payout_balance >= 0)` 兜底。

**自查**：余额相关查询必须带 `FOR UPDATE`；migration 必须有非负 CHECK 约束。

---

## RED-06：MAE Exposure 只增不减，退款/争议不得回扣

**规则**：`daily_gross_exposure_amount_minor` 仅允许 `INCRBY`（Captured 时累加），**禁止**因 Refund / Chargeback / Void 调用 `DECRBY` 或以任何方式减少；自然日结束才归档重置。Refund/Dispute 独立计数，不触碰 exposure。

**❌**：

```go
rdb.IncrBy(ctx, exposureKey, captured)
rdb.DecrBy(ctx, exposureKey, refund)   // ❌ 绝对禁止
```

**✅**：

```go
rdb.IncrBy(ctx, exposureKey, captured)            // 只增
rdb.IncrBy(ctx, refundKey, refund)                // 独立统计，不动 exposure
```

**自查**：`rg -n "DecrBy|DECRBY" internal/mae` 中**不得**出现作用于 exposure 的递减。

---

## RED-07：因 Exposure 出池的 MAE 当日不得自动回 Active

**规则**：MAE 因 `gross_exposure >= vmax*0.8` 进入 Cooldown 时，必须设置 `business_day_lock_until = 当日业务日末`；该 MAE 当日**禁止**自动恢复 Active，只能在**下一业务日**进入 Testing。

**❌**：Cooldown 计时到期即转 Active（跳过 Testing、忽略当日锁）。
**✅**：恢复前校验 `now.businessDate > business_day_lock_until` 且必须经 `Cooldown→Testing→Active`。

**自查**：恢复逻辑必须检查 `business_day_lock_until`；`Cooldown→Active` 直接迁移不得存在于状态机迁移表。

---

## RED-08：Cooldown 后必须进入 Testing，禁止直接 Active

**规则**：状态机中**不存在** `Cooldown→Active` 边。Cooldown 只能到 Testing；Testing 达标才到 Active。Testing 仅承接 5%–10% 灰度流量（`testing_traffic_bps` 默认 500）。

**自查**：`mae/domain` 迁移表断言测试必须包含"`Cooldown→Active` 非法"用例。

---

## RED-09：Webhook 必须经 Receiver 代理，核心账务不得直连外部回调

**规则**：所有 MAE / Bank / LP 异步回调必须先进 Webhook Receiver Proxy；Receiver 只做 TLS/IP Allowlist/限流/验签/幂等/Raw Payload 加密/MQ 投递。**严禁** Webhook 直达 Core Payment API / Ledger Service / Database。核心系统只从 MQ（Redis Streams）消费。

**❌**：`webhook/handler` 里 import 并调用 `ledger/repository` 直接写账。
**✅**：`webhook/handler` 只调用 `stream.Producer.Publish`；入账由 `webhook/consumer`（或 ledger consumer）从 Streams 消费完成。

**自查**：`rg -n "ledger/repository|payout/repository" internal/webhook/handler` 必须为空。

---

## RED-10：Webhook 可重复，账务不可重复（强幂等）

**规则**：Webhook 至少一次投递，必然有重复。每条事件 `idempotency_key` UNIQUE；入账前先幂等检查；`ledger_transaction.idempotency_key` UNIQUE。重复 Webhook **禁止**产生第二笔账。状态机单调流转。

**✅**：入账事务第一步 `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING` 或先查重；冲突即跳过并 ACK。

**自查**：`webhook_ingress_event.idempotency_key`、`ledger_transaction.idempotency_key`、`transactional_outbox.idempotency_key`、`payout_request.idempotency_key`、`sweep_transfer.idempotency_key` 在 migration 中必须 UNIQUE。

---

## RED-11：Sweep 受银行窗口/Cut-off/节假日约束，禁止满额即扫

**规则**：Sweep **禁止**仅按金额触发。必须同时满足金额阈值 ∧ 银行工作日 ∧ 未过 Cut-off ∧ `time_window_matrix` 命中。非窗口期**禁止**调用银行 API，只能 Accumulating / 进 Redis Delay Queue。默认策略为 `TIERED_SCHEDULED`，受 `max_daily_sweep_count` 限制。

**❌**：`if accumulated >= threshold { submitBankAPI() }`（缺窗口判断）。
**✅**：见 TDD §5.2，窗口判断不过 → 入 Delay Queue，不触银行 API。

**自查**：Sweep 提交路径前必须有 `withinBankingWindow(...)` 判断；非窗口分支不得调用 bankClient。

---

## RED-12：非同名 / 跨境划转必须进入审批与合规复核

**规则**：
- `beneficiary_type == UNKNOWN` → reject（禁止执行）。
- `SAME_NAME` 未验证 → ApprovalRequired。
- `THIRD_PARTY` 且金额 ≥ `approval_threshold` → ApprovalRequired（四眼）。
- `THIRD_PARTY_CROSS_BORDER` → 合规复核 + 四眼复核。

**自查**：`treasury/domain` 必须有受益人规则纯函数 + 表驱动测试覆盖全部分支。

---

## RED-13：Ledger 是资金真相，余额快照仅查询/并发控制

**规则**：`merchant_treasury_ledger` 是快照，**不替代** Dual-Entry Ledger。**禁止**手工直接改余额脱离分录；所有资金修正必须通过冲正（REVERSAL）分录。每笔 `ledger_transaction` 的借贷分录金额必须相等（借=贷）。

**❌**：直接 `UPDATE merchant_treasury_ledger SET settled_balance = X`（无对应 ledger_transaction/entry）。
**✅**：先 `INSERT ledger_transaction + ledger_entry(Dr/Cr)`，余额快照随之更新，二者同事务。

**自查**：余额更新的 service 必须同时写 `ledger_transaction` 与成对 `ledger_entry`；domain 有"借贷平衡"断言。

---

## RED-14：Redis 不是资金安全边界

**规则**：Redis Lock / 计数器**禁止**作为最终资金安全边界。资金边界必须是 **DB 本地事务 + 行级锁 + 唯一约束 + 幂等键**。Redis 仅用于：指标计数、Delay Queue、辅助锁、限流。

**❌**：用 `SETNX` 分布式锁作为防双花的唯一手段，DB 无 `FOR UPDATE`/唯一约束。
**✅**：DB `FOR UPDATE` + UNIQUE 为准，Redis 锁仅减少无效争抢。

**自查**：防双花/资金扣减处必须能在"Redis 不可用"假设下仍由 DB 约束兜底。

---

## RED-15：敏感字段必须加密，资金动作必须可审计可冲正

**规则**：API Key、Webhook Secret、银行账号、ABA、IBAN、链上地址、LP 凭证、商户敏感识别信息必须 AES-256-GCM（Envelope Encryption）加密入库。密文字段 struct tag 必须 `json:"-"`，**禁止**出现在日志、响应、错误信息中。所有状态变更与资金动作写审计日志。

**❌**：`log.Info("apiKey", key)`；响应体回传明文地址；密文字段无 `json:"-"`。
**✅**：日志只记 masked / hash；入库前 `crypto.Encrypt`；展示用 `target_address_masked`。

**自查**：`rg -n "ciphertext|api_key|webhook_secret|Plaintext" internal | rg -i "log|json:\""` 人工核对无明文外泄；密文字段均带 `json:"-"`。

---

## 16. 非功能红线（PRD 第 10 章）

| 编号 | 约束 | 要求 |
| --- | --- | --- |
| NF-01 | 幂等保存周期 | 支付/账务/出金类幂等记录 ≥365 天 |
| NF-02 | 幂等冲突 | 同 Key 不同 Payload → `409 IDEMPOTENCY_CONFLICT` |
| NF-03 | 金额类型 | minor=int64，账本=decimal(20,4)；**全局禁用 float 参与金额** |
| NF-04 | 加密算法 | AES-256-GCM，96-bit IV，128-bit Tag，AAD 含 tenant/record/field/key_version |
| NF-05 | 性能 | MAE 熔断 ≤100ms、Payout 冻结事务 P95 ≤200ms、Webhook 验签入队 P95 ≤100ms（见 PRD §10.4） |
| NF-06 | 时间存储 | 一律 UTC `timestamptz`；业务日按司法辖区时区计算 |
| NF-07 | 错误不吞 | error 必须 wrap 上抛或显式处理，禁止 `_ = err` 静默丢弃资金路径错误 |

---

## 17. 提交前自查表（每次提交强制逐条核对）

```text
[ ] RED-01  service 事务回调块内无任何外部 I/O（lp./bank./mae./http.）
[ ] RED-02  外部调用只出现在 internal/worker（+mock 测试）
[ ] RED-03  timeout 走 UnknownExternalState，无盲重试/误释放
[ ] RED-04  Payout 先冻结（FOR UPDATE）后外呼，冻结失败不调 LP
[ ] RED-05  余额变更全程 FOR UPDATE + 非负 CHECK，并发无超卖
[ ] RED-06  exposure 只 INCRBY，无任何 DECRBY；refund/dispute 独立
[ ] RED-07  Exposure 出池设 business_day_lock_until，当日不回 Active
[ ] RED-08  状态机无 Cooldown→Active 边
[ ] RED-09  webhook/handler 不 import ledger/payout repository，只发 Streams
[ ] RED-10  所有幂等键 UNIQUE，重复事件不重复入账
[ ] RED-11  Sweep 有银行窗口判断，非窗口不触银行 API
[ ] RED-12  受益人规则四分支齐全（UNKNOWN reject / 三方四眼 等）
[ ] RED-13  余额变更必有成对借贷分录，借=贷，修正走冲正
[ ] RED-14  防双花由 DB 兜底，Redis 不可用也不破防
[ ] RED-15  敏感字段加密 + json:"-"，日志/响应无明文
[ ] NF-03   grep 无 float32/float64 参与金额计算
[ ] make ci 通过（build + lint + test-short）
```

**自查脚本（建议放 Makefile `redlines` 目标）**：

```bash
# 资金路径禁用 float
rg -n "float(32|64)" internal/ledger internal/payout internal/mae && echo "VIOLATION NF-03"
# 事务/handler 外呼检测
rg -n "lpClient\.|bankClient\.|maeClient\." internal/*/handler internal/*/service && echo "CHECK RED-01/02"
# exposure 递减检测
rg -n "DecrBy" internal/mae && echo "CHECK RED-06"
# Cooldown->Active 检测
rg -n "Cooldown.*Active" internal/mae/domain && echo "CHECK RED-08"
```

> 任一 VIOLATION/CHECK 命中且非误报，必须修复后才能提交。无法判断时在 MEMORY.md 登记并保守处理（按红线从严）。
