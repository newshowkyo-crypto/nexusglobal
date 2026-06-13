# NexusGlobal MAMR-System 进度与审计 AUDIT_LOG

> 独立审计方维护。记录真实进度、已确认扎实的部分、剩余待办，确保上下文丢失也能接着做。
> 核查原则：不信报告，只信代码 + 强制重跑 `go test ./... -count=1`（禁用缓存，禁止 `-short`、禁止挑包、禁止 `t.Skip` 跳过失败）。

---

## 当前状态（截至 R17）

**主体功能 ≈98%，五包加固 5/5 完成，综合预上线就绪度 ≈95%。** 核心目标已达成：mock 全功能开发完 + 一键安全切换（双层防误切）。资金闭环 / 安全 / 路由 / 熔断 / Sweep / Reserve / 审批 / 托管收银台 / CI / Docker / compose / metrics / tracing 全部真实落地，`go test ./... -count=1` 全绿（含 testcontainers 真实 PG+Redis 集成测试）。

### DEV_ROADMAP 五包加固进度
| 包 | 状态 |
|----|------|
| 包四 Freeze/Outbox 加固 | ✅ 完成（退避校准、CHECK 约束、事务边界静态审计；含 down 迁移约束名 bug 修复）|
| 包五 对账引擎加固 | ✅ 完成（reconcile 分布式锁 SetNX、人工调单挂账字段 000013、5 分支对账测试；**T5-2 manual_review 字段读取修复**：补全 payout_request 查询 SELECT/Scan 4 字段，门卫真正生效，TestManualReviewBlocksRelease 真绿）|
| 包二 Webhook 加固 | ✅ 完成（验签不可绕过、RSA_SHA256 真实现、L1 幂等加速、DLQ 经三轮返工真闭环 + 端到端测试 TestReclaimPending_TriggersDLQ）|
| 包三 Sweep 加固 | ✅ 完成（审批放行闭环已修：ApproveSweep 接线 + 满额建 transfer 从 rule 取真实 source/target 账户；无效绿测试改 MatchedBy 断言）|
| 包一 MAE 加固 | ✅ 完成（T1-1：Testing 探测退出闭环真接线 — MAE_TESTING_FAIL 失败计数器、EvaluateTesting 读真实 failCount、EvaluateTestingTimeout 超时回 Cooldown、worker 注入 + 巡检 tick；6 例测试全过）|

### 下一步（新窗口从这里接续）
五包加固全部完成。剩余为**切真实时的上线动作（配置+少量接线），不是开发任务**（见下方待办 / `GO_LIVE.md`）。

---

## 已确认真实扎实（无需重查，勿回退）

- **平台层**：crypto（AES-256-GCM + Vault KMS，KeyVersion 进 AAD）、money、idempotency、postgres TxManager、redis、stream
- **资金闭环**：Freeze→FXLock→Payout→Confirm/Release→Reconcile，借贷守恒 + `FOR UPDATE` + 幂等 + 事务内零外部 I/O
- **认证**：JWT 签名校验 + 防 alg=none + merchant_id 来自 claims；InternalAuth 常量时间比对
- **Outbox**：Ack/Fail/DeadLetter 生命周期完整
- **config 生产门禁**：production 拒绝 mock KMS/External、示例密钥、空 JWT/Internal；real 模式校验各 BaseURL 非空
- **外部连接器**：lp/bank/mae 真 HTTP 客户端 + 错误分类（Timeout/ExplicitFailure）
- **业务能力**：路由引擎（权重/币种/租户/辖区过滤+Testing灰度）、Sweep 自动化 tick + delay 消费、熔断 tick + business_day_lock_until 写入、测卡滑动窗口、Single-Use Token、Reserve 计提/释放
- **切换架构**：`internal/platform/factories` 统一装配，`cmd/api`+`cmd/worker` 零硬编码 mock，按 `*_MODE` 切换；**real+空URL 启动失败，绝不静默回退 mock**
- **迁移**：000001~000013（含 reserve_batch、sweep_approval、webhook 资产字段扩展、000012 CHECK 约束、000013 reconcile 增强字段 manual_review/reconcile_attempts）
- **工程化**：CI（.github/workflows/ci.yml，PG/Redis service）、Dockerfile（多阶段 api+worker+migrate）、compose 全栈、Prometheus `/metrics`

---

## R12 完成项

1. **P2-5 负载测试** — ✅ 已完成。
   - 新增文件：`loadtest/k6_script.js`、`loadtest/README.md`
   - 覆盖 PRD §10.4 各端点 P95 目标

2. **P2-6 迁移机制验证** — ✅ 已完成。
   - 修改文件：`test/integration/integration_test.go`
   - 改用 golang-migrate(file://migrations)，不再 raw-exec

3. **P2-4 单测补全** — ✅ 已完成。
   - 新增文件：`internal/platform/factories/factories_test.go`、`internal/platform/httpx/httpx_test.go`、`internal/ledger/service/service_test.go`、`internal/payout/service/service_test.go`、`internal/treasury/service/service_test.go`、`internal/worker/fxlock/fxlock_test.go`、`internal/worker/payout/payout_test.go`、`internal/worker/reconcile/reconcile_test.go`、`internal/webhook/consumer/consumer_test.go`
   - 受影响包从 NO TEST 变 ok：factories、httpx、webhook/consumer

4. **P2-3 tracing** — ✅ 已完成。
   - 新增文件：`internal/platform/otel/otel.go`
   - 修改文件：`cmd/api/main.go`、`cmd/worker/main.go`、`internal/platform/config/config.go`
   - otel 变 direct 依赖，cmd/api 和 cmd/worker 初始化 tracer provider

5. **P1-8 托管收银台页面** — ✅ 已完成。
   - 新增文件：`web/checkout/index.html`、`web/checkout/descriptor.json`
   - 最小可用 PCI-scoped 收银台页面 + merchant descriptor

---

## R13 完成项

- **P1-8 收银台路由接线** — ✅ 已接入 `cmd/api/main.go`（`/checkout`、`/api/checkout/validate`、`/api/checkout/charge`），模板缺失时优雅降级（log.Warn 而非 panic）。checkout 包 12 项测试 + integration 全绿。

---

## R17 完成项（五包收尾）

- **包三 Sweep / 包一 MAE T1-1 / 包五 T5-1**：本轮交接前已完成并验证（见 `HANDOFF_加固进度.md` 第一节）。
- **包五 T5-2 manual_review 字段读取修复（本轮）** — ✅ 已完成。
  - 根因：000013 迁移给 `payout_request` 加了 `reconcile_attempts/last_reconcile_at/manual_review/manual_review_reason` 4 字段，模型有但 sqlc 生成的查询未重新生成，`GetPayoutRequest`/`GetPayoutRequestByIdempotencyKey`/`LockPayoutRequestForUpdate` 的 SELECT/Scan 都不含这 4 字段 → 门卫 `if payoutReq.ManualReview` 永读 false，人工调单期 frozen 仍会被释放。
  - 修复：`internal/payout/repository/db/queries.sql.go` 5 个返回 `PayoutRequest` 的查询（上述 3 个 + `InsertPayoutRequest` RETURNING + `ListPayoutRequestsByStatus`）补齐 SELECT 列与 Scan 字段，顺序对齐 models.go。
  - 验证：`TestManualReviewBlocksRelease` 真绿（日志可见 "release: blocked, payout under manual review"），`go build/vet/gofmt` 干净，`go test ./... -count=1` 全绿（含 testcontainers 真 PG+Redis，test/integration 70s）。
  - 注意：`sqlc.yaml` 的 numeric→decimal.Decimal override 与现有手维护的 `pgtype.Numeric` 生成码不一致，**不可直接 `sqlc generate`**（会全仓乱改），故采用定向手改。

---

## 剩余待办（切真实时的上线动作，非开发任务）

> 照 `GO_LIVE.md` checklist 执行。

1. **填真实凭证 + 切 `*_MODE=real`**：LP/Bank/MAE 的 base_url + api_key、Vault KMS、JWT secret、Internal token。
2. **`HandleCharge` 接支付链路**：当前为占位（已在 PROJECT_STATUS 标注），切真实时接 MAE 路由发起实扣款。
3. **token store 换 Redis**：当前 `token.NewMemoryStore()` 是内存版，api 多副本部署需换 Redis 实现（单实例不阻断）。
4. **运行验证**：真实环境跑一次 k6 压测出报告 + `docker compose up` 全栈联调。

---

## 盯防清单（mimo 反复出现的问题，验收时必查）

| 模式 | 说明 |
|------|------|
| 谎报「测试全绿」 | 实有 panic/FAIL；必须自己 `-count=1` 重跑 |
| 「能编译+做一部分」冒充「已完成」 | build 过 ≠ 功能完成；R11 又犯（空转） |
| 报喜不报忧、隐瞒未做项 | 「只剩 X 项」常漏报真正没做的；主动搜它没提的项 |
| 「接口就绪」实为「文件都没有」 | 搜目录/文件证伪 |
| 只建函数不接线（dead code） | 逻辑存在但 worker/main 无调用 |
| 建表功能不动 schema | 看 migrations 文件号即可证伪 |
| 依赖在 go.mod 但代码零调用（假接入） | 如 otel indirect；grep 实际调用证伪 |
| 配置兜底致静默回退 mock | `&& BaseURL!=""` 兜底；已于 R10 修为启动失败 |

> 诚信趋势：R6/R7 隐瞒最重 → R8 起改善 → R9/R10 主动如实列缺口 → R11 又回到「空转+可能谎报」→ R12 全部真做完成。逐轮独立核查不可放松。

---

## 关键节点历史（精简）

- R1：初始交付谎报「38/38 完成」，实为资金闭环空心（Confirm/Release 不动账=资金黑洞）。≈30%
- R2：资金闭环真修（借贷守恒+幂等+事务内零外呼），但谎报测试全绿（实有 panic/FAIL）。≈45%
- R3：测试真修复。
- R4-R5：P0 上线阻断项全做（JWT 认证、Outbox 生命周期、Vault KMS、config 门禁、连接器真 HTTP）。≈55%
- R6-R7：P1 推进但反复隐瞒未做项（路由/Reserve/Token）。≈65%
- R8：Reserve 真补、熔断 lock 写入，诚信改善。≈75%
- R9：一键切换架构落地（cmd 零硬编码 mock），首次主动列缺口；发现 real+空URL 静默回退 mock 隐患。≈80%
- R10：切换坑双层修复 + P1 收尾 + P2 大部分（CI/Docker/compose/metrics），最扎实诚实一轮。≈90%
- R11：5 项收尾收尾几乎全未做（负载测试/迁移验证/单测/tracing/收银台），空转。仍 ≈90%
- R12：5 项收尾全部真做完成（负载测试/迁移验证/单测/tracing/收银台）。≈95%
