# RUN_TO_PRELAUNCH —— 一次性跑到预上线总指令（NexusGlobal MAMR-System）

> 本文件是 mimo 从现在起的**唯一总行动依据**，优先级高于 PRELAUNCH_FIX_ORDER.md 的"分批停下"约束。
> 目标：把系统在 **mock 模式下开发到功能齐全、逻辑自洽、测试完整的预上线级别**，使得切换到真实凭证只需改配置、改 `*_MODE=real`，**不改任何业务代码**。
> 配套真相文件：PRD.md（需求真相）、PRELAUNCH_FIX_ORDER.md（逐项规格）、AUDIT_LOG.md（历史诚信记录）。

---

## 0. 本轮工作模式（关键变更）

1. **连续自驱动，不再分批停下。** 一次性把"剩余 P1 收尾 + 全部 P2 + 一键切换架构"全部做完，做完前不要停下来等人工验收。
2. **失败循环纪律**：同一问题失败 2 次必须停止打补丁，定位根因、换方案，并在 MEMORY.md 记录。绝不允许卡死或无限循环。
3. **禁止 stub 糊弄**（历史惯犯，严查）：禁止写死返回值、禁止空数组冒充查询、禁止 `t.Skip` 跳过失败、禁止 `_ = cmd` 丢弃参数、禁止只建函数不接线、禁止建表功能却不写迁移。
4. **禁止谎报与隐瞒**：PROJECT_STATUS.md 必须逐项、逐功能如实标注，且**必须主动列出所有已知缺口**，禁止"报喜不报忧"。审计会逐条比对，谎报即视为本轮失败。
5. **资金红线不可破**：DB 事务内禁止外部 I/O；外部调用走 Outbox/Worker；超时≠失败（UnknownExternalState）；借贷必须平衡；余额分项守恒；幂等唯一约束；敏感字段加密；Redis 不作为资金边界。
6. **金额**：禁用 float，统一用 int64 minor 或 shopspring/decimal，禁止混用。

---

## 1. 核心架构要求：Mock / Real 一键切换（本轮最重要的目标）

这是本轮的灵魂。系统必须做到：**所有外部依赖都通过接口抽象 + 工厂按配置切换，mock 与 real 实现行为契约一致，业务代码对二者无感知。**

### 1.1 统一切换开关
所有外部依赖的 mock/real 选择，集中由配置驱动，禁止散落硬编码：

| 配置项 | 取值 | 控制对象 |
|--------|------|----------|
| `EXTERNAL_MODE` | `mock` / `real` | LP、Bank、MAE 连接器 |
| `KMS_MODE` | `mock` / `real`（vault/aws） | 加密密钥后端 |
| `APP_ENV` | `local` / `staging` / `production` | 全局安全门禁 |

### 1.2 工厂模式（强制）
每类外部依赖必须有一个工厂函数，例如：
```
func NewLPClient(cfg Config) lp.Client {
    if cfg.ExternalMode == "real" { return lp.NewHTTPClient(cfg.LPBaseURL, cfg.LPAPIKey, cfg.LPTimeout) }
    return lp.NewMockLP()
}
```
- `cmd/api` 和 `cmd/worker` **只调工厂**，禁止直接 `NewMockLP()` 硬编码（当前 worker/main.go 仍硬编码 `lp.NewMockLP()`，必须改）。
- Bank、MAE、KMS 同理。

### 1.3 行为契约一致性（防止切换后逻辑崩坏）
- mock 与 real 实现**必须实现同一接口、返回同一组错误类型**（`ErrTimeout` / `ErrExplicitFailure`），使上层 UnknownExternalState 逻辑在两种模式下都成立。
- mock 必须能模拟：正常成功、显式失败、超时三种路径（供测试和联调用），不能只会返回成功。
- real 必须是真正发 HTTP 的客户端，**禁止内部返回假数据**。
- 切换 real 后，凡 real 实现尚未接通真实第三方端点的，在 PROJECT_STATUS.md 标注"接口就绪未接真实后端"，但代码路径必须完整可发请求。

### 1.4 配置清单补全
`.env.example` 必须列出所有切真实所需的配置项（base_url、api_key、timeout、KMS 端点、JWT 密钥、内部令牌等），用占位符，并加注释说明"切真实时填什么"。禁止真实可用密钥进仓库。

### 1.5 一键切换文档
新增 `GO_LIVE.md`：列出从 mock 切到 real 的**完整 checklist**（要改哪些环境变量、要准备哪些真实凭证、切换后如何验证每条外部链路连通、回滚方法）。这是交付物之一。

---

## 2. 剩余功能开发（P1 收尾 + P2 全部，一次做完）

### 2.1 P1 收尾项（前几轮未完成）

**P1-7 Rolling Reserve 调度补全**
- 已有 `ReserveHold`/`ReserveRelease` 计提释放分录逻辑，保留。
- 缺口：新增 reserve 计提批次表迁移（记录 merchant、amount、currency、计提日、到期释放日、状态）。
- worker 新增 reserve ticker：扫描到期批次自动调用 `ReserveRelease`。
- 测试：计提批次到期 → 自动释放回 settled，分录守恒、available 正确。

**P1-6 Webhook 收尾**
- 补全 `webhook_receiver_proxy` 剩余资产字段迁移：`mq_topic`、`region`、`environment`、`health_status`、`tenant_scope`、`version`（前轮 000008 只补了 4 个）。
- 确认/补全：证书到期监控逻辑、IP allowlist 强制执行、rate_limit 强制执行、RSA_SHA256 验签、无 proxy 行拒绝、stream/event_type 端到端对齐、ReclaimPending(XAutoClaim) 调度、producer 写 delivery_count 使 DLQ 真能触发、Replay/ListEvents 接真实 repo。
- 测试：端到端 HTTP webhook→ledger 入账、无效签名拒绝、DLQ 触发、replay 真实重投。

**P1-8 托管收银台收尾**
- Token 服务已做（含 TTL/MaxUses/Origin/订单绑定），保留。
- 缺口：补最小可用 PCI-scoped checkout 页面 + merchant descriptor 展示。
- 测试：token 单次使用、过期失效、重复兑换拒绝、来源域不符拒绝。

**逐项复核 P1-1~P1-5**：确认路由引擎、熔断 tick+lock 写入、测卡防御、Sweep 自动化+审批表、Reconcile 真实对账都已落地且有测试。缺口补齐。

### 2.2 P2 全部（工程化与上线必备）

**P2-1 CI**：新增 GitHub Actions（或等价），跑 build + vet + gofmt + `go test ./... -count=1`。

**P2-2 容器化**：
- 编写 `Dockerfile`（api + worker，多阶段构建，最小镜像）。
- `docker-compose.yml` 加入 api、worker 服务，连同 postgres、redis，`docker compose up` 一键起全栈。
- 加入迁移执行步骤（容器启动时或独立 migrate 服务）。

**P2-3 可观测**：
- 接入 Prometheus `/metrics`：关键资金指标（payout 冻结/确认/释放计数、ledger 入账延迟）、路由指标（各 MAE 选中次数）、外部调用延迟与错误率、Outbox 积压。
- 接入 OpenTelemetry tracing（至少 HTTP 入口 + 外部调用 span）。
- `/healthz` 明确为 liveness；`/readyz` 保持真实依赖检查。

**P2-4 测试补全**：为所有 service、worker、auth 中间件补单测；关键资金路径覆盖率达标（资金测试断言借贷平衡 + 余额守恒）。

**P2-5 负载测试**：用 k6（或 vegeta）对 PRD §10.4 的 P95 目标写压测脚本，输出报告到 `loadtest/`，标注实测 P95 vs 目标。

**P2-6 迁移机制验证**：集成测试改用 golang-migrate 真实迁移路径（不再 raw-exec SQL 文件）；评估并记录 down 迁移的数据安全性。

---

## 3. 全局自洽性要求（切换前后逻辑一致）

做完上述后，必须全局回归确认以下端到端链路在 **mock 模式**下完整自洽、可演示：

1. **收单入账链路**：模拟支付 → 路由选 MAE → webhook captured/settled → ledger 入账 → 商户余额变化。
2. **代付链路**：创建出金地址（清洗+加密）→ payout 请求 → 本地冻结 → Outbox → FXLock → LP Payout → Confirm 核销 / Failed 释放 → 对账。
3. **归集链路**：银行余额累计 → Sweep ticker 评估 → 窗口内提交 / 非窗口入 Delay Queue → 被消费 → 对账。
4. **风控链路**：测卡触发 → MAE 熔断 → 写 business_day_lock_until → 路由剔除 → 次日 Testing 灰度恢复。
5. **Reserve 链路**：计提 → 到期 ticker → 自动释放。
6. **保证金/Token 链路**：创建 token → 收银台兑换 → 单次失效。

每条链路必须有集成测试覆盖，证明 mock 模式下端到端跑通、账目守恒。切真实时这些链路逻辑不变，仅底层连接器换实现。

---

## 4. 验收门禁（全部做完后必须全过）

```
go build ./...            # BUILD_OK
go vet ./...              # VET_OK
gofmt -l .                # 无输出
go test ./... -count=1    # 全绿；禁止 -short、禁止挑包、禁止 t.Skip 跳过失败
docker compose up         # 全栈一键起得来（api+worker+pg+redis）
```

附加：
1. 贴**完整** `go test ./... -count=1` 输出，零 FAIL、零 panic、零 SKIP（环境限制 skip 须显式说明）。
2. 资金测试断言借贷平衡 + 余额守恒。
3. 每个功能项有对应真实测试，证明真实行为而非状态字符串。
4. 每条 §3 端到端链路有集成测试。

---

## 5. 必须交付的文档（做完一并提交）

1. **PROJECT_STATUS.md**：逐项标注 `已实现并测试` / `接口就绪未接真实后端` / `未实现`，**主动列出全部已知缺口**，禁止"全部完成/可上线"这类未经证明的措辞。
2. **GO_LIVE.md**：mock→real 一键切换 checklist（改哪些配置、备哪些凭证、如何验证每条外部链路、回滚方法）。
3. **MEMORY.md**：记录本轮关键决策、踩坑、未决项。
4. **.env.example**：补全所有切真实所需配置项（占位符 + 注释）。

---

## 6. 完工定义（DoD）

- P1 八项全部真实落地 + 测试。
- P2 六项全部完成（CI、容器、可观测、测试补全、压测、迁移验证）。
- Mock/Real 一键切换架构成立：工厂模式覆盖 LP/Bank/MAE/KMS，业务代码零硬编码 mock，`*_MODE=real` 即切换。
- §3 六条端到端链路在 mock 下全部跑通且账目守恒，有集成测试。
- 门禁全过，`docker compose up` 全栈可起。
- 四份文档齐全且如实。

> 达成以上 = 预上线级别：在 mock 上功能齐全、逻辑自洽、测试完整，换真实凭证一键切换即可上线。
> 本轮连续做完，不分批停下。中途失败按 §0.2 换方案。完成后输出门禁结果 + 完整测试输出 + 四份文档摘要，等待审计。
