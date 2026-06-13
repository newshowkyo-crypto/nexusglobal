# STATUS — NexusGlobal MAMR-System 真实进度与核查报告

> 单一权威进度文件。生成于 2026-06-13，基于**独立代码核查 + 强制 `go test ./... -count=1`（含 docker 集成测试）**，不采信历史文档的自我标注。
> 核查方法：4 路并行子代理审计（资金/安全/完整性/规格对照）+ 关键发现作者亲自读码 + 可复现实验证伪。
> 本文件取代并收敛此前分散的进度/计划文档（见末尾「文档收敛」）。

---

## 0. 一句话结论

代码体量与功能覆盖确实接近完整，五包加固也真实接线；**但历史文档「≈98%、资金闭环✅真实、P0全完成、可预上线」的结论被本轮核查证伪**：发现 1 个**严重资金精度 BUG**（已修）、1 个**越权（IDOR）漏洞**（属 P0 明确要求但从未真正满足）、对账主路径**实际不可达**，以及多处 stub。**当前不具备「切真实即可上线」条件**，按下表修完关键项后才到预上线级别。

诚实就绪度（资金安全维度）：**约 70–75%**，而非文档宣称的 ~98%。

---

## 1. 本轮已修复（含验证）

### ✅ C1【严重】小数金额转换溢出，破坏所有非整数金额 — 已修 + 加回归测试
- **根因**：`internal/payout/service/service.go` 与 `internal/ledger/repository/repository.go` 各有一份手写 `decimalToNumeric`，写成 `Exp: -int32(d.Exponent())`（把指数取反）。
- **实测证伪**（运行可复现）：`100.5 → 10050`（×100）、`3000.00 → 30000000`（×10000）、`0.01 → 100`，**只有纯整数 `100` 才正确**。即任何带小数/带小数位的金额写库都被放大若干个数量级；随后 `CHECK(frozen_payout_balance>=0)` 会让 Confirm/Release 事务回滚，**资金被永久卡死**。
- **为何测试全绿却没抓到**：集成测试金额用**原生 SQL** 写入（绕过该 Go 路径），单测用 mock。Go 转换路径从未在真实 DB 上被非整数金额覆盖 → 这是典型「绿测试掩盖资金 BUG」。
- **修复**：两处统一委托给已有且正确的 `money.DecimalToNumeric`（`StringFixed(4)`）。新增回归测试 `internal/payout/service/numeric_test.go`、`internal/ledger/repository/numeric_test.go` 锁死，防止再次手写。
- **验证**：`go build/vet/gofmt` 干净，`go test ./... -count=1` 全绿（含 testcontainers 真 PG+Redis，集成 ~45s）。

### ✅（上一轮）T5-2 manual_review 字段读取 — 已修
- payout_request 的 4 个 reconcile 字段补齐 SELECT/Scan，Release 门卫 `if payoutReq.ManualReview` 真正生效，`TestManualReviewBlocksRelease` 真绿。详见 git diff 与下文「已确认扎实」。

---

## 2. ⚠️ 未修复 — 关键缺口（建议按序处理）

> 下列「亲自核查」项均已读码确认；「审计报告」项为子代理高置信度发现。

### ✅ G1【高 / 漏洞】越权：merchant_id 取自请求体而非 JWT — 已修
- `internal/payout/handler/handler.go` 的 `CreateAddress` 和 `CreatePayoutRequest` 已改为从 `httpx.GetMerchantID(r.Context())` 取商户身份。
- `CreatePayoutRequest` 校验地址归属（`address.MerchantID == ctxMerchantID`），否则 403。

### ✅ G2【高】对账主路径不可达：provider_ref/lock_id 从不落库 — 已修
- `internal/worker/fxlock/fxlock.go` 成功后调用 `repo.UpdatePayoutRequestLockID` 写库。
- `internal/worker/payout/payout.go` 提交后调用 `repo.UpdatePayoutRequestProviderRef` 写库。
- `internal/payout/repository/repository.go` 新增 `UpdatePayoutRequestLockID` 和 `UpdatePayoutRequestProviderRef` 方法。

### ✅ G3【中】`/admin/outbox` 路由重复注册 — 已修
- 删除 `cmd/api/main.go` 中重复的 `outboxH.List` 和 `outboxH.Replay` 注册，保留 `adminH.ListOutbox`。

### ✅ G4【中 / 安全】Webhook 验签直接用密文列当密钥，未解密 — 已修
- `internal/webhook/handler/handler.go` 验签前用 `Encryptor.Decrypt` 还原真实密钥，解密失败返回 500（fail-closed）。

### 🟠 G5【中】只有 LP 一条 webhook 流被消费，MAE/Bank 流无消费者 — 已修
- `internal/webhook/consumer/consumer.go` 改为可配置 stream/group，新增 `NewLPConsumer`、`NewMAEConsumer`、`NewBankConsumer` 工厂函数。
- `cmd/worker/main.go` 启动 LP、MAE、Bank 三个消费者实例。

### ⚠️ G6【中】多个已接线端点是 stub — 已修（R2-2）
- ✅ `webhook/handler` `ListEvents` 已改为真实查询（`repo.ListIngressEvents`）。
- ✅ `webhook/handler` `Replay` 已改为真实重投：从 repo 加载原始事件，重投到对应 provider stream。
- ✅ `internal/mae/handler` `TestingEvaluate` 已下线（worker tick 覆盖真实评估）。
- ✅ `internal/payout/handler/outbox.go` 死代码已删除。

### ✅ G7【中 / 合规】出金请求不校验地址 screening/whitelist — 已修
- `internal/payout/service/service.go` 的 `CreatePayoutRequest` 校验 `screening_status == "Passed"` 和 `whitelist_status == "Active"`，否则返回 422。

### ✅ G8【中】幂等冲突（409）未强制 — 已修（R2-1）
- `CreatePayoutRequest` handler 先 `io.ReadAll` 取原始 body 算 `idempotency.PayloadHash`，再传入 service。
- `CreatePayoutRequest` service 查到 existing 时比对 `existing.PayloadHash` vs 新哈希，不一致返回 409。
- `InsertPayoutRequest` 生成码增加 `payload_hash` 列，新建时写入。
- `GetPayoutRequestByIdempotencyKey` 的 SELECT/Scan 已含 `payload_hash`。
- Freeze 路径里的 G8 残留已清理。

### 🟡 G9【中-低】其余安全/健壮性 — 已修
- `/metrics` 移入 `InternalAuth` 保护的路由组。
- 加密失败改为 fail-closed（报错，不落明文）。
- dashboard 不再回显 token（避免 self-XSS）。
- production config 校验 JWT 密钥强度（≥32 字节）。

### ✅ G10【中】Sweep 源余额从不扣减 — 已修
- `internal/treasury/service/service.go` 归集确认后调用 `repo.DecrementBankAccountBalance` 扣减源账户。
- `internal/treasury/repository/repository.go` 新增 `DecrementBankAccountBalance` 方法（带 `accumulated_minor >= amount` 守卫）。

---

## 3. ✅ 已确认真实扎实（核查通过，勿回退）

- **双轨账本借贷平衡**：所有 `PostTransaction` 活跃路径均为「一借一贷等额」，借贷恒平衡。
- **资金事务边界**：余额走 `SELECT ... FOR UPDATE`；LP/Bank HTTP 调用全部在 DB 事务**之外**，事务内零外呼。
- **负余额防线**：迁移 `CHECK(settled/frozen/reserve/dispute_hold >= 0)`、`CHECK(amount>0)` 齐备。
- **五包加固确属真实接线**（非死代码）：
  - 包三 Sweep 审批：`ApproveSweep` 已接路由，满额从 rule 取真实 source/target 账户建 transfer。
  - 包一 MAE Testing 退出：失败计数器 `MAE_TESTING_FAIL` + 超时回 Cooldown，worker 注入 + 巡检 tick。
  - 包二 Webhook DLQ：`ReclaimPending`(XAutoClaim) ticker 已接线。
  - 包五 reconcile 分布式锁：`locker==nil` 直接拒绝，无静默无锁路径；worker 无 Redis 即 fail-fast。
  - 包四 Outbox 指数退避 + CHECK 约束 + Freeze 事务边界静态测试。
- **认证中间件本体**：JWT 断言 `*SigningMethodHMAC`（防 alg=none / 密钥混淆），`MerchantAuth` 要求 merchant_id claim；`InternalAuth` 用 `subtle.ConstantTimeCompare`。（注意：缺口在 G1 —— handler 没用中间件给的身份。）
- **AES-256-GCM**：每次 `Seal` 用 `crypto/rand` 随机 12B nonce，AAD 绑定租户/记录/字段/KeyVersion，无 nonce 复用。
- **config 生产门禁**：production 拒绝 mock KMS/External、示例主密钥、空 JWT/Internal；real 模式校验各 BaseURL 非空。
- **manual_review 门卫**（上一轮修复）：Release 真正读到标记并拦截。
- 测试纪律：`t.Skip` 仅 `-short`/容器启动失败兜底，本轮全量运行均真实执行未跳过。

---

## 4. 属「切真实上线动作」非开发缺口（照 GO_LIVE.md / ONBOARDING_DATA.md）

### ✅ R3-1 HandleCharge 真实化 — 已修（复验通过）
- `HandleCharge` 已接入 `PaymentGateway` 接口（`internal/checkout/payment.go`），mock 实现支持成功/失败/超时三态。
- `OrderProvider` 接口已定义；`main.go` 注入真实实例（`OrderProvider` 之前传 nil 已修），`ServeCheckoutPage` 渲染真实商户/金额/订单。
- 卡号仅传给支付网关，不落库、不日志记录（已逐处搜 `card_number/card_cvv` 确认无持久化/打印）。
- 401/410/409/402/504 状态码按 token 错误/支付失败/超时分支真实返回，无假成功。

### ✅ R3-2 Single-Use Token Redis 存储 — 已修（复验通过）
- 新增 `internal/token/redis_store.go`，实现 `Store` 接口。
- `IncrementUseCount` 使用 Lua 脚本（`EVAL` + `cjson.decode/used_count++/SET ... EX ttl`）保证"检查+递增"原子性。
- 按 `TOKEN_STORE=memory|redis` 配置切换（`config.go:37` + `main.go:240` 注入）。

### ✅ R3-3 运营后台数据录入 — 已修（复验+审计补全）
- 7 个端点全部真实写库（`internal/admin/handler.go` + `cmd/api/main.go`）：`POST /admin/bank-account`（银行账户，迁移 000007 表 `bank_account_balance`，UPSERT 幂等）、`/bank-calendar`、`/mae-config`、`/sweep-rule-create`、`/webhook-proxy`、`/merchant-create`、`/payout-address`。
- **3 处「简化处理」明文落库已全部改为真加密**（缺 encryptor 时 fail-closed 拒写，不退化）：
  - `CreateMAEConfig`：`api_key_ciphertext`/`webhook_secret_ciphertext` 经 `Encryptor.Encrypt` 写入；同时补 `settlement_bank_account_id` 入参（之前遗漏导致 NOT NULL 约束拒绝，是 mimo 的次生 bug）。
  - `CreateWebhookProxy`：`secret_ciphertext` 经 `Encryptor.Encrypt` 写入；`ip_allowlist` 改为 `[]string` + jsonb 序列化（之前用 `[]byte(string)` 写到 jsonb 列是类型错误）。
  - `CreatePayoutAddress`：`target_address_enc` 经 `Encryptor.Encrypt` 写入；`target_address_hash` 改用 `domain.CleanAddress` 的真哈希（之前 `hash := req.TargetAddress` 是用地址当 hash）。
- 审计新增回归测试 `TestAdminEncryptedFieldsStoredAsCiphertext`：assert 4 字段 ciphertext != 明文、能 Decrypt 还原、且 fail-closed 在缺 encryptor 时拒绝（500）。**反向验证**：把加密改回明文后该测试如期 FAIL（消息：「MAE api_key stored as plaintext」），证明是有效守卫，已改回。

### 纯运维动作（需人工执行）
1. 填真实凭证 + 切 `*_MODE=real`，按 GO_LIVE.md 验证各外部链路。
2. 生产部署与切换后验证。

---

## 5. 测试与门禁现状（本轮亲自运行）

```
go build ./...            ✅ OK
go vet ./...              ✅ OK
gofmt -l .                ✅ 无输出
go test ./... -count=1    ✅ 全绿（含 testcontainers 真 PG+Redis；integration ~45s、postgres/redis/stream 真起容器）
```

> ⚠️ 全绿不等于无 BUG：C1 即在全绿下存在，因测试金额绕过了出问题的 Go 路径。资金类断言应改为**端到端经 service→真实 DB 写入非整数金额并校验回读值**，而非仅原生 SQL 造数。建议补此类回归集成测试。

---

## 7. 本轮修复汇总（G1-G10 + R2 返工 + R3 收尾）

| 项 | 状态 | 修复内容 |
|----|------|----------|
| G1 | ✅ 已修 | handler 从 context 取 merchant_id；校验地址归属 |
| G2 | ✅ 已修 | fxlock/payout worker 落库 lock_id/provider_ref |
| G3 | ✅ 已修 | 删除重复 outbox 路由 |
| G4 | ✅ 已修 | 验签前 Decrypt 还原密钥（fail-closed） |
| G5 | ✅ 已修 | 为 MAE/Bank 流创建消费者 |
| G6 | ✅ 已修 | Replay 真实重投；TestingEvaluate 下线；outbox.go 死代码删除 |
| G7 | ✅ 已修 | 校验 screening/whitelist 状态 |
| G8 | ✅ 已修 | CreatePayoutRequest 路径幂等冲突检测（PayloadHash 比对 + 落库） |
| G9 | ✅ 已修 | /metrics 鉴权、加密 fail-closed、token 不回显、JWT 密钥强度校验 |
| G10 | ✅ 已修 | Sweep 归集后扣减源余额 |
| R2-3 | ✅ 已修 | C1 端到端守卫测试（非整数金额精度验证） |
| R3-1 | ✅ 已修 | HandleCharge 真实化 + PaymentGateway 接口 |
| R3-2 | ✅ 已修 | Token Redis 存储 + 配置切换 |
| R3-3 | ✅ 已修 | 7 张 B 类表数据录入端点 |

---

## 6. 建议修复优先级

| 序 | 项 | 级别 | 状态 |
|----|----|------|------|
| 1 | C1 小数金额溢出 | 严重·资金 | ✅ 本轮已修 |
| 2 | G1 越权 merchant_id（P0 欠账） | 高·漏洞 | ✅ 已修 |
| 3 | G2 reconcile 不可达（provider_ref 不落库） | 高·资金恢复 | ✅ 已修 |
| 4 | G7 出金不校验 screening/whitelist | 中·合规 | ✅ 已修 |
| 5 | G4 webhook 密钥未解密 | 中·安全 | ✅ 已修 |
| 6 | G5 MAE/Bank 流无消费者 | 中·入账 | ✅ 已修 |
| 7 | G3 /admin/outbox 重复路由 | 中 | ✅ 已修 |
| 8 | G10 Sweep 源余额不扣减 | 中 | ✅ 已修 |
| 9 | G6 stub 端点 | 中 | ✅ R2 已修并复验（Replay 真实重投 / TestingEvaluate 下线 / outbox.go 删除） |
| 10 | G8 幂等409 | 中 | ✅ R2 代码已修并复验正确；审计已补 `TestCreatePayoutRequest_PayloadConflict`（同 key 不同 payload→409）测试 |
| 11 | G9 安全杂项 | 中-低 | ✅ 已修（复验通过） |

> **R1 复验（2026-06-13）**：G8 首轮为空操作（handler 未设 PayloadHash）、G6 仅修 1/4——已打回返工。
>
> **R2 复验（2026-06-13，逐处读码 + 实跑）**：
> - ✅ **G8 已真正修复**：`CreatePayoutRequest` 读原始 body 算 `idempotency.PayloadHash`→service 比对 `existing.PayloadHash`→不一致返回 409；`InsertPayoutRequest` 已写 `payload_hash` 列；`GetPayoutRequestByIdempotencyKey` SELECT/Scan 含该列；Freeze 残留已清。先前缺的「同 key 不同 payload→409」行为测试已由审计补上（`TestCreatePayoutRequest_PayloadConflict`）。
> - ✅ **G6 已修**：webhook `Replay` 改为加载原事件重投对应 provider stream（消费者幂等防重复入账）；mae `TestingEvaluate` 路由下线；`payout/handler/outbox.go` 死代码删除。
> - ✅ **R2-3 C1 守卫测试有效**：`TestNonIntegerAmountPrecision` 经 service→真实 DB 写 100.50/0.01/100.1234/999.99 并校验回读+余额增量+借贷平衡。**反向验证**：临时把 `decimalToNumeric` 改回取反指数，该测试如期 FAIL（100.5→10050、0.01→100…），证明是有效守卫而非摆设，已改回。
> - ✅ **门禁实跑**：`go build/vet/gofmt` 干净，`go test ./... -count=1` `TEST_EXIT=0`（stream 本轮未抖动；如遇 Windows testcontainers `rootless Docker` 偶发，重跑即过）。

---

## 7. 文档收敛（已执行）

本文件 `STATUS.md` 为唯一权威进度。本轮已完成文档清理：
- **保留（参考/规格，长期有效）**：`PRD.md`、`TDD.md`、`API.md`、`CONSTRAINTS.md`、`CODING_STANDARDS.md`、`AGENT_WORKFLOW.md`、`TASKS.md`、`README.md`、`.env.example`。
- **保留（上线运维）**：`GO_LIVE.md`、`ONBOARDING_DATA.md`、`SYSTEM_OVERVIEW.md`（注意其「≈98%/资金闭环✅」结论已被本文件修正）、`MEMORY.md`。
- **保留（历史诚信记录）**：`AUDIT_LOG.md`。
- **已归档至 `docs/archive/`**（使命完成的历史指令/看板）：`PROGRESS.md`、`DEV_ROADMAP.md`、`PRELAUNCH_FIX_ORDER.md`、`RUN_TO_PRELAUNCH.md`、`BUILD_COMPLETION_ORDER.md`、`HANDOFF_加固进度.md`、`PROJECT_STATUS.md`（内容已并入本文件）。详见 `docs/archive/README.md`。
</content>
