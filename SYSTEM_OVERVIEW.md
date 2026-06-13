# NexusGlobal MAMR-System 系统总览与上线 SOP

> 一份文档讲清：系统有哪些功能、核心逻辑怎么跑、开发到什么程度、怎么上线。
> 配套文档：PRD.md（需求真相）、AUDIT_LOG.md（进度审计）、GO_LIVE.md（切换 checklist）、ONBOARDING_DATA.md（上线物料）、PROJECT_STATUS.md（逐项状态）。
> 最后更新：R13（托管收银台接线完成），真实完成度 ≈98%。

---

## 1. 系统是什么

NexusGlobal MAMR-System 是面向多租户跨境 SaaS 的**全球财资调度与多轨收单均衡系统**，覆盖交易流与资金流全链路：多收单实体动态路由、托管收银台、Webhook 回调解耦、多轨银行清算归集、商户双轨总账、终值代付清算。

**技术栈**：Go 1.22 + PostgreSQL 15 + Redis 7（Streams 做 MQ）+ chi + pgx + sqlc + shopspring/decimal。
**架构形态**：模块化单体（Modular Monolith），两个可执行体：`cmd/api`（HTTP 服务）、`cmd/worker`（异步处理）。
**部署**：Dockerfile 多阶段构建 + docker-compose 一键起全栈（PG/Redis/api/worker/migrate）。

**核心设计原则**：
- 所有外部依赖（LP/Bank/MAE/KMS）通过接口 + 工厂按 `*_MODE` 切换 mock/real，**mock 全功能开发完，换真实凭证一键切换，不改业务代码**。
- 资金安全边界 = DB 本地事务 + 行级锁 + 唯一约束 + 幂等键（Redis 不作为资金边界）。
- DB 本地事务内禁止任何外部网络 I/O；外部调用一律走 Transactional Outbox + Worker。

---

## 2. 功能清单（6 大模块）

### 模块一：MAE 多收单实体池化与动态路由
- **状态机**：Draft → Active → Cooldown → Testing → Suspended → Disabled（Cooldown 后只能进 Testing，禁止直接回 Active）。
- **额度三口径**：`daily_captured`、`daily_settled`、`daily_gross_exposure`（只增不减，熔断主指标）；refund/dispute 独立计数，不回扣 exposure。
- **熔断**：exposure 达 `daily_vmax * 0.8` → Cooldown + 写 `business_day_lock_until`（当日不回 Active）；异常率/测卡/API 超时也触发。
- **Testing 灰度恢复**：冷却结束 + 健康检查 + 当日锁过期 → Testing，按 `testing_traffic_bps`（默认 5%）承接灰度流量，达标转 Active，连续失败 3 次 → Suspended。
- **路由引擎**：按 `routing_weight` 加权 + 可用性（Active/Testing）+ 币种 + 租户范围 + 辖区过滤选 MAE。
- **测卡防御**：60s 滑动窗口（invalid_card / unique_card_per_ip / decline_per_tenant），触发标记 RiskRejected + MAE 进 Cooldown。

### 模块二：Webhook Receiver 解耦代理 + 托管收银台
- **Webhook 代理**：MAE/Bank/LP 三类回调先进边缘接收 → 验签（HMAC_SHA256 / RSA_SHA256）→ 加密原始 Payload → 幂等去重 → 投 Redis Streams → 核心消费者异步入账。核心账务不直连外部回调。
- **幂等键**：`hash(provider_type + provider_id + provider_event_id + event_type)`，无 event_id 时退化为 payload_hash + 业务日。
- **DLQ**：验签失败/投递失败/消费失败进死信队列；XAutoClaim 回收滞留消息。
- **资产台账**：proxy_code、证书指纹、证书到期、secret_version、IP allowlist、rate_limit。
- **Single-Use Token**：TTL、使用次数限制、来源域/CORS/CSP 控制、订单绑定。
- **托管收银台**：PCI-scoped 页面（`web/checkout/`）+ merchant descriptor，token 兑换后渲染，单次使用。

### 模块三：多轨银行清算与阶梯式财资归集
- **银行日历**：工作日、节假日、临时关闭日、各 Rail 的 Cut-off 矩阵。
- **Sweep 策略**：Threshold Accumulation / Tiered Scheduled / Emergency / Manual；非窗口期只累计或进 Redis Delay Queue，不调银行 API。
- **自动化**：worker ticker 周期 Evaluate + 消费 Delay Queue。
- **受益人规则**：SAME_NAME(verified 自动) / THIRD_PARTY(大额四眼复核) / CROSS_BORDER(合规+四眼) / UNKNOWN(拒绝)。
- **审批工作流**：APPROVAL_REQUIRED 持久化为待审批记录。
- **银行 API 超时 → UnknownExternalState**，禁止盲重试。

### 模块四：商户双轨总账与强冻结代付
- **账户口径**：pending / settled / reserve / frozen_payout / dispute_hold / available_payout。
  `available = settled - reserve - frozen_payout - dispute_hold`
- **双轨账本**：Dual-Entry Ledger 为资金唯一真相，append-only，借贷必平衡。余额快照仅用于查询和并发控制。
- **Rolling Reserve**：按比例从 settled 计提到 reserve（RESERVE_HOLD 分录）+ 到期释放调度（RESERVE_RELEASE 分录）。

### 模块五：Payout 本地冻结 + 异步 LP 代付（资金核心）
五阶段 Saga：
1. **Freeze**（本地事务）：`SELECT FOR UPDATE` 锁余额 → 校验 available → settled 减、frozen 增 → 写 PAYOUT_FREEZE 双轨分录 → 写 payout_request → 写 Outbox(FX_LOCK_REQUESTED)。事务内零外部 I/O。
2. **FX Lock**（Worker）：拉 Outbox → 调 LP 锁价 → 成功写 Outbox(LP_PAYOUT_REQUESTED)，超时 → UnknownExternalState。
3. **LP Payout**（Worker）：调 LP 代付 → LPProcessing / Failed / UnknownExternalState。
4. **Confirm**：幂等校验 → FOR UPDATE → PAYOUT_CONFIRM 分录(Dr Frozen / Cr External) → frozen 减 → Confirmed。
5. **Release**（失败时）：PAYOUT_RELEASE 分录(Dr Frozen / Cr Settled) → frozen 减、settled 加 → BalanceReleased。
- **UnknownExternalState 期间 frozen 不释放**，靠 LP 查询/Webhook/对账确认终值。

### 模块六：对账（Reconcile）
- UnknownExternalState 的 payout/sweep：拉外部状态 + bank_ref/provider_ref 匹配 + 金额核对 → Confirmed/Failed/仍 Unknown 三分支。

### 横切能力
- **认证**：JWT 签名校验（防 alg=none）+ merchant_id 从 claims 取；InternalAuth 常量时间比对。
- **加密**：AES-256-GCM 信封加密，Vault KMS（KeyVersion 进 AAD）。
- **可观测**：Prometheus `/metrics` + OpenTelemetry tracing + `/healthz`/`/readyz`。
- **可视化后台**：`/admin/dashboard` 中文管理界面（10 个标签页，目前只读）。

---

## 3. 核心逻辑红线（不可破）

1. MAE Exposure 只增不减；因额度出池当日不回 Active。
2. Cooldown 后必须经 Testing 灰度，不直接 Active。
3. Webhook 必须代理化，核心账务不暴露给外部回调。
4. Sweep 受银行工作窗口/Cut-off/节假日约束；非窗口不调银行 API。
5. 非同名/跨境划转必须审批 + 合规复核。
6. Payout 先本地冻结，再异步锁价，再提交 LP。
7. **DB 本地事务内严禁外部网络 I/O**；外部调用走 Outbox/Worker/Saga。
8. 外部 API 超时 ≠ 失败，进 UnknownExternalState，不盲重试。
9. Ledger 是资金真相，借贷必平衡，余额分项守恒，所有修正走冲正分录。
10. Redis 不作为资金边界；资金边界 = DB 事务 + 行锁 + 唯一约束 + 幂等键。
11. 敏感字段全加密，所有资金动作可审计、可追溯、可冲正。

---

## 4. 开发进度（≈98%，预上线就绪）

### 已完成并测试通过（勿回退）
| 领域 | 状态 |
|------|------|
| 平台层（crypto/money/idempotency/postgres/redis/stream） | ✅ |
| 资金闭环 Freeze→FXLock→Payout→Confirm/Release→Reconcile | ✅ 借贷守恒+幂等+事务内零外呼 |
| 认证授权（JWT + InternalAuth 常量时间比对） | ✅ |
| Outbox 生命周期（Ack/Fail/DeadLetter） | ✅ |
| config 生产门禁 + real 模式 URL 校验 | ✅ |
| 外部连接器 lp/bank/mae 真 HTTP + 错误分类 | ✅ |
| MAE 路由引擎 / 熔断 tick / 测卡防御 | ✅ |
| Sweep 自动化 tick + Delay Queue 消费 + 审批表 | ✅ |
| Rolling Reserve 计提/释放 | ✅ |
| Single-Use Token + 托管收银台页面 + 路由接入 | ✅ |
| Mock/Real 一键切换架构（双层防误切） | ✅ |
| 迁移 000001~000011 | ✅ |
| CI / Dockerfile / compose 全栈 | ✅ |
| Prometheus metrics / OpenTelemetry tracing | ✅ |
| 负载测试脚本(k6) / golang-migrate 集成测试 / 单测补全 | ✅ |
| `go build/vet/gofmt/test ./... -count=1` 全绿（含 testcontainers 集成测试） | ✅ |

### 切真实前需处理的占位项（非阻断，已知）
1. **收银台 `HandleCharge` 是占位**：目前只返回成功，未接真实扣款，需接 MAE 路由支付链路。
2. **收银台 Token 用内存储**：多副本部署需换 Redis 共享存储。
3. **后台为只读**：`/admin/dashboard` 10 个标签页目前只能看，无新增/编辑表单（业务数据录入需补表单，见 ONBOARDING_DATA.md §4）。

---

## 5. 上线操作 SOP

> 原则：先在 staging 用 mock 跑通全链路 → 切 real 验证 → 生产上线。切换只改配置，不改代码。

### 阶段 0：物料准备（业务/运营）
按 `ONBOARDING_DATA.md` 准备：
- **A 类系统密钥**：生产 PG DSN、Redis 地址、JWT_SECRET、INTERNAL_TOKEN、Vault 配置、LP/Bank/MAE 的 BASE_URL+API_KEY。
- **B 类业务数据**（7 张表，按序）：银行账户 → 银行日历 → MAE → 归集规则 → Webhook 代理 → 商户 → 出金地址。
- **C 类收银台物料**：每商户 descriptor.json（名称/Logo/条款/退款政策/客服）。

### 阶段 1：staging 验证（mock 模式）
1. `cp .env.example .env`，保持 `APP_ENV=local`、`EXTERNAL_MODE=mock`、`KMS_MODE=mock`。
2. `docker compose up -d`（起 PG/Redis/api/worker）。
3. `go run ./cmd/migrate up` 执行迁移。
4. 录入 B 类业务数据（后台或 API）。
5. 跑通 6 条核心链路（逐条确认账目守恒）：
   - 出金地址创建 → 加密落库
   - Freeze → FXLock → LP Payout → Confirm 全链路
   - Webhook captured → ledger 入账
   - Sweep Evaluate → 银行转账
   - MAE 熔断 → business_day_lock_until 写入
   - Token 创建 → 收银台兑换

### 阶段 2：切真实（按 GO_LIVE.md）
1. 填真实凭证到 `.env`（A 类）。
2. 切开关：`APP_ENV=production`、`EXTERNAL_MODE=real`、`KMS_MODE=real`。
   - ⚠️ production 启动会强校验：mock 模式 / 示例密钥 / 空 JWT / real 模式空 URL 一律启动失败。
3. 逐项验证：LP fx-lock 返回真实 lock_id、Bank 转账发出、MAE HealthCheck、KMS 加密落库、OTel span 上报。
4. 完成占位项收尾：HandleCharge 接支付链路、Token 换 Redis 存储。

### 阶段 3：生产上线
1. 生产 PG/Redis/Vault 就位，`POSTGRES_DSN` 用 `sslmode=require`。
2. 部署 api + worker 容器，配 OTel Collector。
3. 跑 GO_LIVE.md §9 切换后验证 checklist（9 项全过）。
4. 灰度放量，监控 metrics/tracing。

### 回滚
`EXTERNAL_MODE`/`KMS_MODE` 改回 `mock` → 重启 api+worker → 确认 `/healthz`。

---

## 6. 关键文件索引

| 文件 | 用途 |
|------|------|
| `PRD.md` | 需求真相 |
| `SYSTEM_OVERVIEW.md` | 本文档：功能/逻辑/进度/SOP 总览 |
| `PROJECT_STATUS.md` | 逐项实现状态 |
| `AUDIT_LOG.md` | 进度审计与盯防清单 |
| `GO_LIVE.md` | Mock→Real 切换 checklist |
| `ONBOARDING_DATA.md` | 上线物料准备清单 + 格式 |
| `.env.example` | 全部配置项模板 |
| `RUN_TO_PRELAUNCH.md` | 切换架构与收尾任务规格 |

---

## 7. 一句话总结

资金核心、安全、路由、清算、代付、对账、切换架构全部真实落地且测试通过，已达预上线 ≈98%。剩余仅 3 个「切真实时」的收尾动作（收银台扣款接线、Token 换 Redis、后台录入表单），按本文 SOP 与 GO_LIVE.md 即可上线。
