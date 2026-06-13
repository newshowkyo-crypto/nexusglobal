# PROJECT_STATUS.md — NexusGlobal MAMR-System

> 最后更新：2026-06-13
> 依据：mimo提示词.txt 收尾任务审计

---

## Mock/Real 一键切换架构

| 项目 | 状态 | 说明 |
|------|------|------|
| 工厂模式 | ✅ 已实现 | `internal/platform/factories/factories.go` 统一创建 LP/Bank/MAE/KMS |
| 安全校验 | ✅ 已实现 | real+空URL 启动失败，不静默回退 mock |
| cmd/api | ✅ 已改造 | 使用 `factories.NewExternalDeps(cfg)`，无硬编码 mock |
| cmd/worker | ✅ 已改造 | 使用 `factories.NewExternalDeps(cfg)`，无硬编码 mock |
| 配置清单 | ✅ 已补全 | `.env.example` 包含所有切真实所需配置项 |
| 切换文档 | ✅ 已交付 | `GO_LIVE.md` 完整 checklist |

---

## P0 — 上线绝对阻断项

| 项目 | 状态 | 说明 |
|------|------|------|
| P0-1 真实认证授权 | ✅ 已实现并测试 | JWT 校验，merchant_id 从 context 取 |
| P0-2 Outbox 生命周期 | ✅ 已实现并测试 | Ack/Fail/DeadLetter 完整 |
| P0-3 真实 KMS | 接口就绪未接真实后端 | VaultKMS 实现存在，切换 `KMS_MODE=real` 即可 |
| P0-4 config 生产校验 | ✅ 已实现并测试 | production+mock 均拒绝；real+空URL 均拒绝 |
| P0-5 外部连接器 | 接口就绪未接真实后端 | HTTPClient 实现存在，切换 `EXTERNAL_MODE=real` 即可 |

---

## P1 — PRD 核心能力

| 项目 | 状态 | 已知缺口 |
|------|------|----------|
| P1-1 MAE 路由引擎 | ✅ 已实现并测试 | 无 |
| P1-1b MAE Testing 退出闭环 (T1-1) | ✅ 已实现并测试 | 失败计数器 ≥3→Suspend、Testing 超时回 Cooldown，worker 已注入 + 巡检 tick（包一完成） |
| P1-2 MAE 熔断接线 | ✅ 已实现并测试 | 无 |
| P1-3 测卡防御 | ✅ 已实现并测试 | 无 |
| P1-4 Sweep 自动化 | ✅ 已实现并测试 | 审批闭环已接线：ApproveSweep 满额建 transfer 从 rule 取真实账户（包三完成） |
| P1-5 Reconcile | ✅ 已实现 | 金额不符告警未实现 |
| P1-6 Webhook 链路 | ✅ 已实现 | IP allowlist/rate_limit 强制执行未实现；证书到期监控未实现 |
| P1-7 Rolling Reserve | ✅ 已实现并测试 | 无 |
| P1-8 Single-Use Token | ✅ 已实现并测试 | 无 |
| P1-8 托管收银台 | ✅ 已实现 | PCI-scoped checkout 页面 + descriptor + 路由已接线；HandleCharge 为占位，未接支付链路 |

---

## P2 — 工程化

| 项目 | 状态 | 说明 |
|------|------|------|
| P2-1 CI | ✅ 已实现 | `.github/workflows/ci.yml`：build+vet+gofmt+test |
| P2-2 容器化 | ✅ 已实现 | `Dockerfile`(多阶段构建) + `docker-compose.yml`(api+worker+pg+redis) |
| P2-3 可观测 | ✅ 已实现 | OpenTelemetry tracing 已接入，`internal/platform/otel/otel.go` 初始化 tracer provider |
| P2-4 测试补全 | ✅ 已完成 | 9个包已补真实单测：factories、httpx(auth)、ledger/payout/treasury service、fxlock/payout/reconcile worker |
| P2-5 负载测试 | ✅ 已完成 | `loadtest/k6_script.js` 覆盖 PRD §10.4 各端点 P95 目标 |
| P2-6 迁移验证 | ✅ 已完成 | `test/integration/integration_test.go` 改用 golang-migrate(file://migrations) |

---

## 资金账务闭环

| 流程 | 状态 |
|------|------|
| Freeze (SETTLED→FROZEN_PAYOUT) | ✅ 已实现并测试 |
| FX Lock | ✅ 已实现 |
| Payout | ✅ 已实现 |
| Confirm (FROZEN_PAYOUT→EXTERNAL) | ✅ 已实现并测试 |
| Release (FROZEN_PAYOUT→SETTLED) | ✅ 已实现并测试 |
| Reserve Hold (SETTLED→RESERVE) | ✅ 已实现并测试 |
| Reserve Release (RESERVE→SETTLED) | ✅ 已实现并测试 |
| Webhook captured/settled/refund/dispute | ✅ 已实现 |

---

## 迁移文件

| 文件 | 说明 |
|------|------|
| 000001 | MAE 配置表 |
| 000002 | Webhook receiver_proxy + ingress_event |
| 000003 | Treasury 日历/规则/转账 |
| 000004 | Ledger 账户/交易/分录 |
| 000005 | Payout 请求/地址/余额 |
| 000006 | Outbox 事件表 |
| 000007 | Bank account balance 表 |
| 000008 | Webhook proxy 资产字段 |
| 000009 | Reserve batch 计提批次表 |
| 000010 | Webhook proxy 扩展字段 |
| 000011 | Sweep approval 审批表 |
| 000012 | CHECK 约束补全 (reserve_balance >= 0, dispute_hold_balance >= 0) |
| 000013 | Reconcile 增强：payout_request 加 reconcile_attempts / last_reconcile_at / manual_review / manual_review_reason |

---

## DEV_ROADMAP 任务包四（资金红线加固）

| 任务 | 状态 | 说明 |
|------|------|------|
| T4-1 退避策略校准 | ✅ 已落地 | `FailOutboxEvent` 已实现指数退避 `1 << LEAST(retry_count, 6)` |
| T4-2 CHECK 约束补全 | ✅ 已完成 | 新增迁移 `000012_check_constraints.up.sql`，补 `reserve_balance >= 0` 和 `dispute_hold_balance >= 0` |
| T4-3 事务边界静态审计 | ✅ 已完成 | `transaction_boundary_test.go` 断言 Freeze 事务内无外部 HTTP 调用 |

---

## DEV_ROADMAP 任务包五（异步出金二次确认与对账引擎）

| 任务 | 状态 | 说明 |
|------|------|------|
| T5-1 对账分布式锁 | ✅ 已完成 | `ReconcilePayout` 持分布式锁 `LOCK:reconcile:{payout_id}`，防多 Worker 并发 |
| T5-2 人工调单挂账 | ✅ 已完成 | 新增迁移 `000013_reconcile_enhancements`，超阈值转 `manual_review`；**已修字段读取**：payout_request 查询补齐 SELECT/Scan 4 字段，Release 门卫 `if payoutReq.ManualReview` 真正生效（TestManualReviewBlocksRelease 真绿） |
| T5-3 对账测试 | ✅ 已完成 | 10 个测试覆盖 confirmed/failed/仍 unknown/超阈值转人工/锁未获取 五分支 |

---

## DEV_ROADMAP 任务包二（Webhook 加固）

| 任务 | 状态 | 说明 |
|------|------|------|
| T2-1 验签不可绕过 | ✅ 已完成 | repo 为 nil 时返回 500，不再跳过验签 |
| T2-2 RSA_SHA256 验签 | ✅ 已落地 | `domain.VerifyRSA256` 已实现，按 `signature_algorithm` 分支 |
| T2-3 幂等 L1 加速层 | ✅ 已完成 | Redis `SET` 快速否决，DB 唯一约束仍为 L2 最终真相 |
| T2-4 DLQ 可触发 | ✅ 已完成 | 使用 XPendingExt 获取真实 RetryCount；handler 失败不 XAck 留在 PEL；ReclaimPending ticker 已接线；4 个 DLQ 测试通过 |

---

## DEV_ROADMAP 任务包三（Sweep 加固）

| 任务 | 状态 | 说明 |
|------|------|------|
| T3-1 阈值语义统一 | ✅ 已完成 | `EvaluateSweepTrigger` 改用 `NetSweepAmount`（净额），净额为 0 不触发 SUBMIT |
| T3-2 审批工单闭环 | ✅ 已完成 | `SweepApproval` 决策持久化到 `sweep_approval` 表；`ApproveSweep` 审批通过后创建 transfer |
| T3-3 审批测试 | ✅ 已完成 | 净额为 0 不 SUBMIT、非同名大额进审批表、审批通过后续做、审批未满不创建 transfer |

---

## 新增文件清单

### P2-5 负载测试
- `loadtest/k6_script.js` — k6 脚本覆盖 PRD §10.4 各端点 P95 目标
- `loadtest/README.md` — 运行说明

### P2-6 迁移验证
- `test/integration/integration_test.go` — 改用 golang-migrate

### P2-4 测试补全
- `internal/platform/factories/factories_test.go`
- `internal/platform/httpx/httpx_test.go`
- `internal/ledger/service/service_test.go`
- `internal/payout/service/service_test.go`
- `internal/treasury/service/service_test.go`
- `internal/worker/fxlock/fxlock_test.go`
- `internal/worker/payout/payout_test.go`
- `internal/worker/reconcile/reconcile_test.go`
- `internal/webhook/consumer/consumer_test.go`

### P2-3 OpenTelemetry
- `internal/platform/otel/otel.go` — Tracer provider 初始化
- `cmd/api/main.go` — 集成 OTel
- `internal/platform/config/config.go` — 添加 OTel 配置项

### P1-8 托管收银台
- `web/checkout/index.html` — PCI-scoped checkout 页面
- `web/checkout/descriptor.json` — 商户 descriptor
