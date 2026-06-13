# FIX_ORDER_R2 — 返工工单（第 2 轮）

> 来源：2026-06-13 对上一轮 G1-G10 修复的独立复验（见 `STATUS.md` 复验结论）。
> G1/G2/G3/G4/G5/G7/G9/G10 复验通过，**勿动**。本工单只修以下 3 件未达标项。
> 纪律同 `FIX_ORDER.md` §0（禁 stub/禁谎报/资金红线/金额禁 float/每项真实测试/门禁全过贴完整输出）。
> 特别提醒：**禁止 `sqlc generate`**（本仓库生成码与 sqlc.yaml 的 numeric→decimal override 不一致，会全仓乱改）；需改生成码请定向手改，字段顺序对齐 `models.go`。

---

## R2-1 🔴 G8 幂等冲突（409）—— 上一轮为**空操作**，需真正实现

**复验结论**：`Freeze` handler 从不设置 `cmd.PayloadHash` → `service.Freeze` 里 `if cmd.PayloadHash != ""` 永假，409 永不触发；且比较写成 `existing.IdempotencyKey != cmd.PayloadHash`（字段用错）；迁移 000014 加的 `payout_request.payload_hash` 列从不写入/读取（死列）；无 409 测试。位置也错——幂等键真正生效在 **创建路径 `CreatePayoutRequest`**，不在 `Freeze`。

**要做**（把 G8 落到创建路径，用上已建的 `payload_hash` 列）：
1. **handler**：`internal/payout/handler/handler.go` `CreatePayoutRequest`——读取原始请求体字节，用 `idempotency.PayloadHash(body)` 算哈希，传入 service（注意：`json.NewDecoder(r.Body)` 会消费 body，需先 `io.ReadAll` 再 `json.Unmarshal`，把原始字节留给哈希）。
2. **service**：`internal/payout/service/service.go`
   - `CreateRequestCmd` 增 `PayloadHash string` 字段。
   - `CreatePayoutRequest`：查到 `existing` 时——
     - `existing.PayloadHash != "" && cmd.PayloadHash != "" && existing.PayloadHash != cmd.PayloadHash` → 返回 `apperr.ErrIdempotencyConflict`（409）。
     - 否则按原样幂等返回 existing。
   - 插入新请求时把 `cmd.PayloadHash` **写入** `payload_hash` 列。
3. **写库**：`InsertPayoutRequest` 的生成码（`internal/payout/repository/db/queries.sql.go`）+ `InsertPayoutRequestParams` 增加 `payload_hash` 列与参数（手改，勿跑 sqlc）；确认 `GetPayoutRequestByIdempotencyKey` 的 SELECT/Scan 已含 `payload_hash`（models 已有该字段，确认查询一致，避免 Scan 错位）。
4. **清理**：删除 `Freeze` 路径里失效的 G8 残留（`FreezeCmd.PayloadHash`、`service.Freeze` 里那段 `existing.IdempotencyKey != cmd.PayloadHash` 比较），避免误导。
5. `apperr.ErrIdempotencyConflict` 须映射为 HTTP 409（确认 `writeAppErr` 映射正确）。

**验收**：
- 单测/集成：同 Idempotency-Key + 同 body → 幂等返回同一 payout（200/201，非新建）；同 Idempotency-Key + **不同 body** → **409**；不同 key → 正常新建。
- 确认 `payout_request.payload_hash` 真有值落库（查库断言非空）。

---

## R2-2 🟠 G6 剩余活跃 stub —— 接真实逻辑或下线路由

上一轮只修了 webhook `ListEvents`。以下两个仍是**活跃路由的 stub**，二选一处理（接真实 / 删路由+handler），不允许留假成功：

### (a) webhook `Replay` — `internal/webhook/handler/handler.go:226`（路由 `cmd/api/main.go:199`）
现状：只发合成 `"replay"` 事件（消费者无对应处理）、恒 `{"replayed":true}`。
- **接真实**：按 `id` 从 repo 加载原始 `webhook_ingress_event`（含 provider_type、event_type、原始/解密 payload、idempotency_key），**重投到该 provider 对应的 stream**（lp/mae/bank），用能触发重新入账的真实事件结构（复用真实 event_type 与幂等键，保证消费者可处理且不重复动账）。`repo` 或 `producer` 为 nil 时返回 500。
- **或下线**：若预上线不需要 replay，删除路由 `:199` + `Replay` handler。

### (b) mae `TestingEvaluate` — `internal/mae/handler/handler.go`（路由 `cmd/api/main.go:220`）
现状：喂全 0 指标，永远只能 HOLD/Suspend。
- **接真实**：从 Redis 指标聚合器读真实指标（参考 `cmd/worker/main.go` 熔断 tick `EvaluateCircuitBreaker`/`EvaluateTesting` 的读法）后再评估。
- **或下线**：若该手动端点非必需，删路由 + handler（worker 的定时 tick 已覆盖真实评估）。

### (c) 死代码清理（非 stub，但建议做）
`internal/payout/handler/outbox.go` 的 `List`/`Replay` 已无路由（G3 摘除），建议删除该文件及 `NewOutboxHandler` 引用。

**验收**：保留的端点产生真实副作用/返回真实数据并有测试；下线的端点路由与 handler 已移除；`go build` 无未用符号告警。

---

## R2-3 🟠 C1 端到端守卫测试 —— DoD 漏项，必须补

**背景**：C1（小数金额转换溢出）已修，但只有单元级 `numeric_test.go`。FIX_ORDER DoD 要求一条**经 service → 真实 DB → 回读**的端到端守卫，防止任何写库路径再次引入精度问题。

**要做**：在 `test/integration/integration_test.go` 新增集成测试（testcontainers 真 PG）：
1. 建 merchant ledger（settled 充足，如 1000.00）。
2. 经 **service 层**（`payout.Service.Freeze`，或 CreatePayoutRequest→Freeze）以**非整数金额**（如 `100.50`、`0.01`、`100.1234`）发起冻结。
3. 回读 `payout_request.amount` 与 `merchant_treasury_ledger` 余额，断言：
   - `amount` 回读值 == 输入值（`decimal.Equal`，不是字符串近似）。
   - `settled` 减少、`frozen` 增加**恰好等于**该非整数金额。
   - ledger 分录借贷平衡。
4. 不允许用原生 SQL 直接 INSERT 金额绕过 service 路径（那正是上次漏掉 C1 的原因）。

**验收**：该测试在 `go test ./test/integration/ -count=1` 下真跑通过；故意把 `decimalToNumeric` 改回取反指数能让它 FAIL（自检一次再改回）。

---

## 完工定义（DoD）

- R2-1 G8 真实实现 + 409 测试；`payload_hash` 真落库。
- R2-2 两个活跃 stub 全部「接真实并测试」或「下线路由」；死代码清理。
- R2-3 C1 端到端守卫测试新增并通过。
- 门禁全过并贴**完整** `go test ./... -count=1` 输出（零 FAIL/panic/SKIP；若 `internal/platform/stream` 偶发 `rootless Docker ... failed to create Docker provider`，属 Windows testcontainers 抖动，**重跑确认通过**并在报告里说明，不得当作通过略过）。
- 如实更新 `STATUS.md` 对应条目（G6/G8 状态）。不得声称「全部完成/可上线」。
