# PRELAUNCH_FIX_ORDER —— 预上线修复指令（NexusGlobal MAMR-System）

> 本文件是 mimo 的唯一行动依据。目标：把当前"功能原型"推进到"预上线级别"。
> 适用对象：自驱动 AI 开发代理（codex/mimo 等）。
> 配套文件：PRD.md（需求真相）、BUILD_COMPLETION_ORDER.md（上一轮资金核心修复，已完成，勿回退）。

---

## 0. 工作纪律（强制，违反即视为失败）

1. **分批执行**：本文件分 P0 / P1 / P2 三批。**只做当前批次，做完停下等人工验收，禁止跨批次。** 默认从 P0 开始。
2. **禁止 stub 糊弄**：禁止写死返回值（如 `return {"status":"Cooldown"}`）、禁止返回空数组冒充查询、禁止用 `t.Skip` 跳过失败测试、禁止 `_ = cmd` 丢弃参数后假装完成。
3. **禁止谎报**：完成度必须如实写入 PROJECT_STATUS.md，三种状态严格区分：`已实现并测试` / `接口就绪未接真实后端` / `未实现`。
4. **失败循环纪律**：同一问题失败 2 次必须停止打补丁，定位根因、换方案，并在 MEMORY.md 记录。
5. **资金红线不可破**（沿用 BUILD_COMPLETION_ORDER §4）：DB 事务内禁止外部 I/O；外部调用走 Outbox/Worker；超时≠失败（UnknownExternalState）；借贷必须平衡；余额分项守恒；幂等唯一约束；敏感字段加密。
6. **金额**：禁用 float；用 int64 minor 或 shopspring/decimal，禁止混用导致精度问题。
7. **每批验收门禁**（见 §4），不过不许声称完成。

---

## 1. 当前真实状态基线（审计结论，勿重复犯）

### 已做扎实（保留，勿回退）
- 资金账务闭环 Freeze→FXLock→Payout→Confirm/Release：真实动账、借贷守恒、`FOR UPDATE`、幂等、事务内零外呼。
- AES-256-GCM 加密原语正确。
- 双轨账本 append-only、幂等唯一约束、银行窗口判断（WithinBankingWindow）真实。
- Webhook 消费者四类事件真实动账（但见下方链路断裂问题）。
- 出金地址清洗+加密+落库真实。

### 已确认的假实现 / 缺失（本文件要修的对象）
- 认证授权全假（只检查 Bearer 前缀；merchant_id 从请求体读）。
- Outbox worker 生命周期坏死（不 Ack、不 retry、不 DeadLetter，永远停在 Processing）。
- KMS 是 MockKMS + 主密钥已随 .env.example 泄露。
- config 不校验生产安全（production 可用 mock+公开密钥静默启动）。
- 外部连接器全是 mock，FX 汇率写死 1.05，无真实 HTTP 实现。
- MAE 熔断/恢复是死代码 + 假端点；`business_day_lock_until` 熔断时根本没写入。
- 无路由引擎（routing_weight/tenant/currency 是死列）。
- 无测卡防御/risk-engine。
- Sweep 无自动化（无 ticker 驱动 Evaluate，Delay Queue 入队后无人消费）。
- Single-Use Token + 托管收银台完全没做。
- Rolling Reserve 是永远为 0 的被动字段。
- Webhook 生产链路断裂（handler 发 `event_type="webhook"` 到 `stream:webhook:mae`，消费者只订阅 `stream:webhook:lp` 且不处理 "webhook"）。
- Reconcile/Replay/ListEvents 是 stub。
- 无 CI、无 Dockerfile、compose 无 app；无 metrics/tracing；`/healthz` 静态 stub；关键包零测试；无负载测试；集成测试绕过 golang-migrate。

---

## 2. 批次 P0 —— 上线绝对阻断项（资金安全 / 合规红线）

> 这批不做完，系统上线 = 资金事故。每项必须有真实测试证据。

### P0-1 真实认证授权
**文件**：`internal/platform/httpx/httpx.go`、`internal/payout/handler/*`、`internal/platform/httpx/dashboard.go`、`cmd/api/main.go`
**规格**：
- `AdminAuth`/`MerchantAuth`：实现真实 JWT 校验（验签名 + 校过期 exp + 校签发方 iss）。解析出主体身份放入 `context`。
- `MerchantAuth`：认证出的 `merchant_id` 放入 context；所有 payout/merchant handler 的 `merchant_id` **必须从 context 取，禁止从请求体读**。
- `InternalAuth`：与配置共享密钥做 `subtle.ConstantTimeCompare`，非空检查不算数。
- `/admin/dashboard` 与 `/admin/dashboard/data` 移入鉴权保护组（当前在 auth group 外，公开暴露）。
- `dashboard.go` 中硬编码的 `'Bearer admin-token'` 移除，改为登录后注入。
**验收证据**：测试覆盖——无 token 拒绝、过期 token 拒绝、错误签名拒绝、合法 token 放行且 context 带身份；越权测试（A 商户 token 不能操作 B 商户资金）。

### P0-2 Outbox 生命周期修复
**文件**：`internal/worker/outbox/outbox.go`、`internal/payout/repository/*`
**规格**：
- 处理成功 → 调用 `AckOutboxEvent` 置 `Completed`。
- 处理失败 → 调用 `FailOutboxEvent`，递增 `retry_count`，按指数退避推进 `next_retry_at`。
- `retry_count >= MaxRetries` → 写 `DeadLetter` 状态（不许只打日志 continue）。
- 确认 claim 过滤条件能重新捞起 `Failed` 且 `next_retry_at <= now()` 的事件。
**验收证据**：testcontainers 真实 PG 测试——成功→Completed；失败→retry_count+1 且 next_retry_at 推进；超限→DeadLetter；DeadLetter 不再被 claim。

### P0-3 真实 KMS + 密钥安全
**文件**：`internal/platform/crypto/*`、`internal/platform/config/config.go`、`.env.example`
**规格**：
- 实现真实 KMS 接口（AWS KMS 或 HashiCorp Vault 二选一），按 `cfg.KMSMode` 分支；`MockKMS` 仅 `APP_ENV=local` 可用。
- `KeyVersion` 真实写入并参与 AAD；支持密钥轮换的版本标记。
- `.env.example` 的公开主密钥替换为占位说明（如 `<BASE64_32BYTE_KEY_REQUIRED>`），禁止真实可用密钥进仓库。
**验收证据**：加密/解密往返测试（含 KeyVersion）；mock 与 real 分支选择测试。无真实 KMS 凭证时，real 实现需完整且能在有凭证时工作，并在 STATUS 标注"未接真实后端"。

### P0-4 config 启动安全校验
**文件**：`internal/platform/config/config.go`
**规格**：`APP_ENV=production` 时，若 `KMSMode=mock` 或 `ExternalMode=mock` 或主密钥等于示例密钥 → 启动失败并报明确错误。
**验收证据**：测试覆盖 production+mock 必须报错、production+real 通过、local+mock 通过。

### P0-5 外部连接器接口化 + 真实实现
**文件**：`pkg/external/lp/*`、`pkg/external/bank/*`、`pkg/external/mae/*`、`cmd/api/main.go`、`cmd/worker/main.go`
**规格**：
- 为每个 `Client` 增加真实 HTTP 实现，工厂按 `cfg.ExternalMode`（mock/real）切换。
- 真实实现含：可配置 base_url、超时、错误分类（Timeout vs ExplicitFailure，对接已有 UnknownExternalState）、请求签名/鉴权、FX 汇率来自响应而非写死。
- real 实现必须是真正能发 HTTP 请求的客户端，**禁止内部返回假数据冒充**。
**验收证据**：用本地 httptest server 模拟端点的测试——正常响应解析、超时→Timeout、5xx→ExplicitFailure 分类正确。

---

## 3. 批次 P1（PRD 功能缺失）/ 批次 P2（工程化）

> P0 验收通过后才开始 P1。P1 验收通过后才开始 P2。

### 批次 P1 —— PRD 要求但未实现的核心能力

**P1-1 MAE 路由引擎**（PRD §1.1、§3）
- 实现按 `routing_weight` + 可用性（仅 Active/Testing）+ `supported_currencies` + `tenant_scope` + `jurisdiction` 选择 MAE 的引擎。
- Testing 状态只承接 5%-10% 灰度流量（按 `testing_traffic_bps`）。
- 证据：选路单测覆盖权重分布、币种/租户过滤、Suspended/Cooldown 排除、Testing 灰度比例。

**P1-2 MAE 熔断/恢复接线 + business_day_lock_until 写入**（PRD §3.7、§3.8、§9.1）
- 新增 scheduler/ticker 周期调用 `EvaluateCircuitBreaker` 与 `EvaluateTesting`。
- 熔断（EXPOSURE_THRESHOLD）时**必须写入 `business_day_lock_until`**，保证"当日不回 Active"。
- 删除 `CircuitBreak`/`TestingEvaluate`/`GetMetrics` 的写死返回，改为调真实 service + 读真实 Redis 指标。
- 实例化 metric `Aggregator`，让 exposure 计数真实递增。
- 证据：集成测试——exposure 达 80% 阈值自动 Cooldown 且写入 lock，当日不恢复，次日可进 Testing。

**P1-3 测卡防御 / risk-engine**（PRD §9.1）
- 实现 60s 滑动窗口：`invalid_card_rate_60s`、`unique_card_count_per_ip_60s`、`decline_count_per_tenant_60s`、`card_testing_count_per_mae_60s`。
- 触发→标记 `RiskRejected` + MAE 进 Cooldown + 100ms 内剔除路由。
- 证据：滑动窗口计数单测 + 触发动作测试。

**P1-4 Sweep 自动化**（PRD §5）
- 新增 ticker 驱动 `Evaluate`；实现 Delay Queue 消费者周期调用 `DequeueReady` 重新评估。
- 修复阈值语义（gross vs net 一致性）。
- 实现审批工作流：`APPROVAL_REQUIRED` 持久化为待审批记录（四眼复核），不许 detect 后丢弃。
- 证据：延迟项入队后被消费、窗口内自动提交、非同名大额进审批表。

**P1-5 Reconcile 真实对账**（PRD §5、§7）
- Sweep/Payout 对账：拉取外部状态/bank_ref 匹配 + 金额核对 + 必要时 ledger 冲正分录，不许纯翻状态。
- 证据：confirmed/failed/仍 unknown 三分支测试，含金额不符告警。

**P1-6 Webhook 生产链路打通**（PRD §4）
- 统一 producer/consumer 的 event_type 与 stream 名称（handler 发的类型与消费者订阅一致）。
- 验签不可绕过：无 proxy 行 → 拒绝；实现 RSA_SHA256；按 `signature_algorithm` 列分支。
- 补全 `webhook_receiver_proxy` 资产台账字段（proxy_code、certificate_fingerprint、certificate_expires_at、secret_version、mq_topic 等），实现 IP allowlist + rate_limit 强制执行 + 证书到期监控。
- 实现 ReclaimPending（XAutoClaim）调度 + producer 写 delivery_count，让 DLQ 真能触发。
- Replay/ListEvents 接真实 repo（加载原事件、复用真实幂等键/payload；列表真实查库）。
- 证据：端到端 HTTP webhook → ledger 入账测试；无效签名拒绝；DLQ 触发测试；replay 真实重投。

**P1-7 Rolling Reserve**（PRD §6）
- 实现滚动保证金计提（按比例从 settled 拨入 reserve）+ 到期释放调度 + `RESERVE_HOLD`/释放分录。
- 证据：计提→reserve 增加、available 减少；到期→释放回 settled，分录守恒。

**P1-8 Single-Use Token + 托管收银台**（PRD §1.2、模块二）
- 实现 Single-Use Token 服务：TTL、使用次数限制、来源域/CORS/CSP/回跳地址控制、与订单绑定。
- 托管收银台最小可用：PCI-scoped 页面 + descriptor。
- 证据：token 单次使用、过期失效、重复兑换拒绝测试。

### 批次 P2 —— 工程化与上线必备

**P2-1 CI**：新增 CI（GitHub Actions 或等价），跑 build + vet + gofmt + test。
**P2-2 容器化**：编写 Dockerfile（api + worker），compose 加入 app 服务，可一键起全栈。
**P2-3 可观测**：接入 Prometheus `/metrics`（关键资金/路由/延迟指标）；接入 OpenTelemetry tracing；`/healthz` 改为真实存活检查（或明确为 liveness）。
**P2-4 测试补全**：为所有 service、worker、auth 中间件补单测；关键资金路径覆盖率达标。
**P2-5 负载测试**：用 k6/vegeta 对 PRD §10.4 的 P95 目标做压测脚本并出报告。
**P2-6 迁移机制验证**：集成测试改用 golang-migrate 真实迁移路径（不再 raw-exec SQL）；评估 down 迁移的数据安全性。

---

## 4. 验收门禁（每批结束必须全过）

```
go build ./...            # 必须 BUILD_OK
go vet ./...              # 必须 VET_OK
gofmt -l .                # 必须无输出
go test ./... -count=1    # 必须全绿；禁止 -short、禁止挑包、禁止 t.Skip 跳过失败
```

附加要求：
1. 贴**完整** `go test ./... -count=1` 输出，证明零 FAIL、零 panic、零 SKIP（环境限制的 skip 须显式说明原因）。
2. 资金相关测试必须断言"借方合计=贷方合计"与"余额分项加总守恒"。
3. 每个修复项必须有对应的新增/更新测试，证明**真实行为**而非状态字符串。
4. 重写 PROJECT_STATUS.md：逐项标注 `已实现并测试` / `接口就绪未接真实后端` / `未实现`，禁止"全部完成/可上线"这类未经证明的措辞。
5. 在 MEMORY.md 记录本批次的关键决策、踩坑与未决项。

---

## 5. 完工定义（DoD）

- **P0 完工**：5 项全部真实现 + 测试证据齐全 + 门禁全过 + STATUS 如实更新。资金不再可被越权操作，密钥不再公开，Outbox 可靠，外部调用可切真实后端。
- **P1 完工**：8 项 PRD 核心能力真实落地 + 测试 + 门禁全过。
- **P2 完工**：CI/容器/可观测/测试/压测/迁移验证齐备，具备预上线交付条件。

> 每批做完即停，输出门禁结果与证据，等待人工验收。验收通过后再进入下一批。
