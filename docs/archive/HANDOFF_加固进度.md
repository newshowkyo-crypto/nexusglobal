# 加固进度与待修复 HANDOFF

> 本轮独立核查 + 修复的交接记录。核查原则照旧：**不信报告，只信代码 + 强制 `go test ./... -count=1`（含 docker 集成测试，禁缓存/禁 -short/禁挑包/禁 t.Skip）**。
> Docker 环境可用（postgres:15 + redis:7 已 healthy，集成测试能真跑，约 45s）。

---

## 一、本轮已完成并验证通过的修复（勿回退）

### 1. 包三 Sweep 审批闭环（上一轮报告夸大，已补全）
- **死代码修复**：`ApproveSweep` 之前全仓无调用方。已在 `cmd/api/main.go` 的 `/internal` 路由组加 `POST /sweep/{id}/approve`，`internal/treasury/handler/handler.go` 加 `ApproveSweep` 方法 + 接口声明。
- **坏 transfer 修复**：`ApproveSweep` 审批满额后建 transfer 时 source/target 账户原为 `uuid.Nil`（注释自承「需要从 rule 获取」）。已改为先 `GetSweepRule(approval.SweepRuleID)` 取真实 `SourceBankAccountID`/`TargetBankAccountID`。文件 `internal/treasury/service/service.go`。
- **无效绿测试修复**：`TestApproveSweep_Success` 原用 `InsertTransfer(mock.Anything)` 盖住 nil bug，已改 `mock.MatchedBy` 断言账户非 nil + `AssertExpectations`。文件 `internal/treasury/service/service_test.go`。

### 2. 包五 T5-1 reconcile 分布式锁静默降级修复
- 原 `ReconcilePayout` 在 `w.locker == nil` 时静默无锁跑（资金路径隐患）。已改为 locker 缺失直接报错拒绝对账。文件 `internal/worker/reconcile/reconcile.go`。
- `cmd/worker/main.go`：Redis ping 失败时改为 `os.Exit(1)`（worker 依赖 Redis 分布式锁，不允许无 Redis 启动）；locker 直接用 `NewRedisLockProvider(rdb)`，rdb 保证非 nil。

### 3. 包一 T1-1 MAE 探测退出机制（原为死代码，已真正接线）
- **失败计数器**：`internal/mae/metric/metric.go` 加 `MAE_TESTING_FAIL:{mae_id}` 的 Get/Incr(带7天TTL)/Reset 三方法。
- **service 接线**：`internal/mae/service/service.go` 加 `TestingFailCounter` 接口 + `WithFailCounter()` 注入；`EvaluateTesting` 现在读真实 failCount 传入纯函数（原恒传 0，永不触发 `>=3` Suspend），判定后 HOLD→Incr、Promote/Suspend→Reset。
- **探测窗口超时**：新增 `EvaluateTestingTimeout`，Testing 态停留超 `cooldown_minutes` 仍未晋级 → 回 Cooldown（用 `UpdatedAt` 作进入 Testing 时间基准）。
- **worker 接线**：`cmd/worker/main.go` 注入 `maeSvc.WithFailCounter(maeMetrics)` + 新增 Testing 超时巡检 tick（1分钟）。
- **测试**：新增 `internal/mae/service/testing_exit_test.go`，覆盖连续失败→Suspend、Promote 清零、超时→Cooldown 等 6 例，全过。

> 上述 1/2/3 已全部通过 `go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1`（单元 + 真 docker 集成测试全绿，**除下方第二节那条新测试**）。

---

## 二、⚠️ 进行中：未修完的关键 BUG（下次第一优先级）

### 🔴 BUG：payout_request 的 manual_review / reconcile_attempts 字段从未被 SQL 查询 SELECT/Scan

**根因**：迁移 `000013_reconcile_enhancements.up.sql` 给 `payout_request` 加了 4 个字段（`reconcile_attempts`、`last_reconcile_at`、`manual_review`、`manual_review_reason`），模型 `internal/payout/repository/db/models.go:133-152` 也有这些字段，**但 sqlc 生成的查询在加迁移后没重新生成**，所以以下查询的 SELECT 列和 Scan 都不含这 4 个字段：
- `getPayoutRequest`（queries.sql.go:117-142）
- `getPayoutRequestByIdempotencyKey`（queries.sql.go:144-169）
- `lockPayoutRequestForUpdate`（queries.sql.go:537-563）

**连锁影响（两处资金逻辑实际失效）**：
1. **T5-2 我加的 Release 门卫读不到 manual_review**：`internal/worker/payout/payout.go` 的 `Release` 里 `if payoutReq.ManualReview` 永远读到零值 false → 人工调单期间 frozen 仍会被释放。门卫逻辑对，但数据读不到，等于没生效。
2. **reconcile 的 `shouldTransferToManualReview` 读 `ReconcileAttempts` 也读到 0**（`internal/worker/reconcile/reconcile.go`）→ 转人工调单的阈值判定可能不准。

**这正是新测试 `TestManualReviewBlocksRelease`（test/integration/integration_test.go:575-656）抓出来的**：
报错 `"release: cannot transition from UnknownExternalState to BalanceReleased" does not contain "manual review"`——门卫没拦住（因为读到 ManualReview=false），落到了后面的状态机检查。

**待办修复步骤（下次执行）**：
1. 把这 3 个查询（`getPayoutRequest`、`getPayoutRequestByIdempotencyKey`、`lockPayoutRequestForUpdate`）的 SELECT 列补上 `reconcile_attempts, last_reconcile_at, manual_review, manual_review_reason`，Scan 按模型字段顺序（见 models.go:148-151）补 4 个 `&i.ReconcileAttempts, &i.LastReconcileAt, &i.ManualReview, &i.ManualReviewReason`。
   - 注意 SELECT 列顺序要和 Scan 顺序一致；模型里这 4 个字段排在 `UpdatedAt` 之后。
   - 文件：`internal/payout/repository/db/queries.sql.go`（若有 sqlc 源 `queries.sql` 也同步改，最好直接重跑 sqlc generate）。
2. **修测试初始状态的状态机问题**：`TestManualReviewBlocksRelease` 第一段用 `UnknownExternalState` 起步，但状态机 `legalTransitions`（internal/payout/domain/domain.go:27-36）里 `UnknownExternalState` 只能 →Confirmed/Failed，**不能直接 →BalanceReleased**。门卫应该在 CanTransition 之前就拦住，所以只要字段能读到、门卫生效，第一段 `assert.Contains(err, "manual review")` 就会过。但测试末尾「清除标记后 Release 成功」那段把 status 改成了 `Failed`（Failed→BalanceReleased 合法），这段是对的。确认门卫修好后整条测试应能过。
3. 重跑 `go test ./test/integration/ -run TestManualReviewBlocksRelease -count=1 -v` 确认绿，再跑全量。

---

## 三、其余已核查为「真落地、质量好」的项（无需重做）
- 包二 Webhook 全部加固（T2-1 验签不可绕过 / T2-2 RSA_SHA256 / T2-3 L1+L2 幂等 / T2-4 DLQ XAutoClaim）✅ 真实
- 包四 T4-1 指数退避、T4-2 CHECK 约束、T4-3 Freeze 事务边界 AST 测试 ✅ 真实
- 包五 T5-3 对账四分支测试 ✅ 真实

---

## 四、属于「上线动作」非开发缺口（照 GO_LIVE.md，暂不动）
- `HandleCharge` 仍是占位（未接真实支付网关），且收银台收明文卡号——切真实时再接 MAE 实扣款。
- token store 仍是 `token.NewMemoryStore()` 内存版，多副本部署需换 Redis 实现。
- `/metrics` 公网无鉴权、`/api/checkout/charge` 无 rate limit（占位阶段）。
- 填真实凭证 + 切 `*_MODE=real`。

---

## 五、收尾时记得做
- 修完第二节 BUG 后，更新 `AUDIT_LOG.md`：包三标「完成（审批放行闭环已修）」、包一标「T1-1 完成」、包五 T5-2 标「完成（含 manual_review 字段读取修复）」。
- 同步更新 `PROJECT_STATUS.md`。
