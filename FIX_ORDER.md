# FIX_ORDER — 预上线缺口修复工单（NexusGlobal MAMR-System）

> 来源：2026-06-13 独立核查（见 `STATUS.md`）。本工单只列**待修**项；C1 小数金额溢出已修复（勿重做，见下「样板」）。
> 执行对象：mimo / 开发代理。完成后由审计方按 `go test ./... -count=1`（含 docker 集成测试）+ 读码复验。

---

## 0. 工作纪律（强制，违反即视为失败）

1. **禁止 stub 糊弄**：禁止写死返回值、空数组冒充查询、`t.Skip` 跳过失败、`_ = x` 丢弃参数后假装完成。
2. **禁止谎报**：完成度如实写入 `STATUS.md`，区分 `已实现并测试` / `接口就绪未接真实后端` / `未实现`。
3. **资金红线不可破**：DB 事务内禁止外部 I/O；外部调用走 Outbox/Worker；超时≠失败（UnknownExternalState）；借贷必平衡；余额分项守恒；幂等唯一约束；敏感字段加密；Redis 不作资金边界。
4. **金额**：禁用 float；统一 `shopspring/decimal` 或 int64 minor。**写库一律用 `money.DecimalToNumeric`，禁止手写 decimal→numeric 转换**（见样板）。
5. **每项必须有真实测试**，断言真实行为而非状态字符串；资金类断言借贷平衡 + 余额守恒。
6. **同一问题失败 2 次**：停止打补丁，定位根因换方案，记录到 `MEMORY.md`。
7. **验收门禁**（每完成一项即自测，全部完成跑全量）：
   ```
   go build ./...            # BUILD_OK
   go vet ./...              # VET_OK
   gofmt -l .                # 无输出
   go test ./... -count=1    # 全绿；禁 -short、禁挑包、禁 t.Skip 跳过失败
   ```
   贴**完整** test 输出，证明零 FAIL / 零 panic / 零 SKIP（环境限制 skip 须显式说明）。

### 样板（C1，已修，照此风格）
小数金额转换溢出已修：`internal/payout/service/service.go`、`internal/ledger/repository/repository.go` 的手写 `decimalToNumeric` 已改为委托 `money.DecimalToNumeric`，并加回归测试 `numeric_test.go`。**勿回退、勿再手写转换。**

---

## 1. 🔴 G1【高 · 越权漏洞】merchant_id 取自请求体而非 JWT

**文件**：`internal/payout/handler/handler.go`（`CreateAddress` ~L40-84、`CreatePayoutRequest` ~L92-140）；参考 `internal/platform/httpx/httpx.go` 的 `GetMerchantID`。

**现状（已核查）**：
- `CreateAddress` 用 `req.MerchantID`（请求体）作商户身份，**从不**与 `httpx.GetMerchantID(r.Context())`（JWT 校验出的商户）比对。
- `CreatePayoutRequest` 不绑定认证商户（service 收到零值 merchant_id），且**不校验** `payout_address_id` 是否属于该商户。

**要做**：
1. 两个 handler 的 merchant_id **一律从 `httpx.GetMerchantID(r.Context())` 取**；请求体里的 `merchant_id` 字段忽略或仅作一致性校验（不一致返回 403）。
2. `CreatePayoutRequest`：取出地址后校验 `address.MerchantID == ctxMerchantID`，否则 403/404。
3. `MerchantAuth` 中间件已正确把 merchant_id 放进 context（勿改中间件，只改 handler 取值来源）。

**验收**：新增越权测试——A 商户 token 创建/引用 B 商户的地址被拒绝（403）；合法本人放行；缺/伪造身份被拒。

---

## 2. 🔴 G2【高】对账主路径不可达：provider_ref / lock_id 从不落库

**文件**：`internal/worker/fxlock/fxlock.go`（~L101-111 拿到 lock_id 处）、`internal/worker/payout/payout.go`（~L107-111 拿到 provider_ref 处）、`internal/payout/repository/repository.go` + `internal/payout/queries.sql`（补 update 查询）、`internal/worker/reconcile/reconcile.go:120`。

**现状（已核查）**：全仓无任何 `SET provider_ref`/`SET lock_id`；worker 只 `log` 不写库。`reconcile.go:120` 读到 `ProviderRef==""` 即 `return nil`，**永不调用 `lpCli.QueryStatus`** → 卡在 `UnknownExternalState` 的单子永远无法被对账解决。

**要做**：
1. 新增持久化查询：`UpdatePayoutRequestLockID(id, lock_id)`（fxlock 锁价成功后写）、`UpdatePayoutRequestProviderRef(id, provider_ref)`（payout 提交后写）。用 raw SQL 或 sqlc 均可，但**注意**：本仓库 sqlc 生成码与 `sqlc.yaml`（numeric→decimal override）不一致，**不可直接 `sqlc generate`**（会全仓乱改）；若改生成码请定向手改，模型字段顺序见 `internal/payout/repository/db/models.go`。
2. fxlock / payout worker 在相应阶段调用上述写库（在已有事务边界内，保持事务内零外呼）。
3. 复验 reconcile：provider_ref 落库后能进入 `QueryStatus` 分支。

**验收**：集成测试——payout 走到 LPProcessing 后 `provider_ref` 已落库；reconcile 对 `UnknownExternalState` 单子能真正调用 LP（mock 返回 confirmed/failed/unknown 三分支均覆盖），confirmed→Confirm 动账、failed→Release 动账、unknown→累计 attempts。

---

## 3. 🟠 G3【中】`/admin/outbox` 路由重复注册，真实实现被空 stub 覆盖

**文件**：`cmd/api/main.go:166`（`r.Get("/outbox", adminH.ListOutbox)` 真实）、`:203`（`r.Get("/outbox", outboxH.List)` stub）。

**现状（已核查）**：chi v5.3.0 不 panic、后注册覆盖先注册 → 线上 `/admin/outbox` 返回空数组，`admin.ListOutbox` 成死代码。

**要做**：删除 `:203-204` 的 `outboxH.List`/`outboxH.Replay` 重复注册（保留 `adminH.ListOutbox`）；若需要 replay，给它一个不冲突的真实实现（见 G6），否则一并删除其 stub。

**验收**：`/admin/outbox` 返回真实数据；无重复路由。

---

## 4. 🟠 G4【中 · 安全】Webhook 验签直接用密文列当密钥，未解密

**文件**：`internal/webhook/handler/handler.go:115,117`（`VerifyRSA256/VerifyHMACSHA256(payload, signature, proxy.SecretCiphertext)`）；`internal/webhook/repository/`。

**现状（已核查）**：`proxy.SecretCiphertext` 原样传入验签，全仓无 `Decrypt`。若该列存密文→验签用错密钥（真实回调验不过）；若存明文→密钥在库明文（与字段名矛盾）。

**要做**：明确密钥存储口径。推荐：入库时加密（`Encryptor.Encrypt`），验签前 `Encryptor.Decrypt` 还原真实密钥再传入验签函数。

**验收**：真实密钥往返测试——加密入库的 secret，解密后 HMAC/RSA 验签能通过；错误密钥/签名被拒。

---

## 5. 🟠 G5【中】只消费 LP 一条 webhook 流，MAE/Bank 流无消费者

**文件**：`internal/webhook/consumer/consumer.go:62-63`（硬编码 `stream:webhook:lp` / `cg-core-lp`）、`cmd/worker/main.go:129`（只起 `lpConsumer`）。

**现状（已核查）**：发往 `stream:webhook:mae` / `:bank` 的回调无人入账。

**要做**：为 MAE、Bank 流各起一个消费者（独立消费组），或把 consumer 改为可配置 stream/group 并在 worker 起多个实例。确认 producer 端 stream 名与消费端一致。

**验收**：端到端——分别向 mae/bank 流投递 captured/settled 回调，核心消费者真实入账、账目守恒。

---

## 6. 🟠 G6【中】已接线但是 stub 的端点

**文件/现状（已核查）**：
- `internal/webhook/handler/handler.go` `ListEvents`（恒 `{"events":[]}`，接口无 List 方法）、`Replay`（发合成 `"replay"` 事件、消费者无对应处理、恒 `{"replayed":true}`）。
- `internal/payout/handler/outbox.go` `List`（恒空、丢弃 status 过滤）、`Replay`（恒假成功、零副作用）。
- `internal/mae/handler/handler.go` `TestingEvaluate`（喂全 0 指标，永远只会 HOLD/Suspend，无法 Promote）。

**要做**：
- 要么接真实实现（repo 真实查库 / replay 真实重投原事件 / TestingEvaluate 读真实 Redis 指标聚合，参考 worker 熔断 tick 的读法），
- 要么**删除并下线该路由**（若功能不需要），不允许留假成功 stub。`admin` 包已有真实的 `ListWebhookEvents`/`ListOutbox` 可复用。

**验收**：保留的端点返回真实数据/产生真实副作用并有测试；下线的端点路由已移除。

---

## 7. 🟠 G7【中 · 合规】出金请求不校验地址 screening/whitelist

**文件**：`internal/payout/handler/handler.go` `CreatePayoutRequest`、`internal/payout/service/service.go` 创建出金请求路径。

**现状（已核查，对照 API §7.3）**：不检查 `screening_status==Passed` / `whitelist_status==Active`；AML/制裁筛查未实现（地址状态恒 `Pending`）。

**要做**：创建出金请求前校验地址 `screening_status`/`whitelist_status`，不满足返回 422。（AML 筛查器本身可标 `接口就绪未接真实后端`，但状态校验门必须有。）

**验收**：未通过筛查/未白名单的地址发起出金被拒（422）；通过的放行。

---

## 8. 🟠 G8【中】幂等冲突（409）未强制

**文件**：写端点（payout 创建等）、`internal/platform/idempotency`（`ErrIdempotencyConflict` 已定义未用）。

**现状（已核查，对照 NF-02）**：凭 idempotency_key 查库「返回首个结果」，但**不比对 payload 哈希** → 同 key 不同 body 静默返回旧结果。

**要做**：对带 `Idempotency-Key` 的写请求，存储 key→payload_hash；同 key 不同 payload 返回 `409 IDEMPOTENCY_CONFLICT`；同 key 同 payload 返回原结果。

**验收**：同 key+同 body→幂等返回；同 key+不同 body→409。

---

## 9. 🟡 G9【中-低】安全/健壮性杂项

**文件/现状（已核查）**，逐项处理：
- `cmd/api/main.go:144` `/metrics` 在 auth 组外、无鉴权 → 移入 `InternalAuth` 保护或加网络 ACL。
- Webhook 无时间戳/重放窗口保护（仅幂等去重）→ 加签名时间戳 + 容忍窗口校验。
- `webhook_receiver_proxy.ip_allowlist`/`rate_limit` 列存在但未强制 → handler 强制执行来源 IP 限制 + 验签限流。
- 加密失败静默落明文：`internal/webhook/handler/handler.go`（Encrypt 出错保留明文）、`internal/webhook/consumer/consumer.go`（Decrypt 失败回退原始字节）→ 改 **fail-closed**（报错，不落/不用明文）。
- `internal/platform/httpx/dashboard.go` 把调用方 token 原样拼进内联 `<script>` → 用 `html/template` 转义或不回显 token。
- `internal/platform/config/config.go` 生产不校验 JWT 密钥强度 → 加最小长度（≥32 字节）。

**验收**：各项对应单测（无鉴权 401、明文 fail-closed、弱密钥启动失败等）。

---

## 10. 🟠 G10【中】Sweep 源余额从不扣减（跨日重复归集风险）

**文件**：`internal/treasury/service/service.go`（~L84-176，按 `GetBankAccountBalance` 毛额计算）。

**现状（已核查）**：全仓无对 `bank_account_balance.accumulated_minor` 的扣减/高水位记录；跨日仅靠日级幂等键 `sweep:{rule}:{date}` 防重 → 次日同一笔未扣减的累计余额会被再次归集。

**要做**：归集确认后扣减源账户 `accumulated_minor`（或记录已归集高水位），确保同一笔资金不被跨日重复归集。需结合入账侧对 `accumulated_minor` 的写入口径确认一致。

**验收**：归集成功后源余额相应减少；次日同账户不重复归集已归集部分。

---

## 完工定义（DoD）

- G1–G10 全部 `已实现并测试`（或明确 `接口就绪未接真实后端` 并说明），每项有真实测试。
- 门禁全过，贴完整 `go test ./... -count=1` 输出（含 docker 集成，零 FAIL/panic/SKIP）。
- `STATUS.md` 逐项更新真实状态；`MEMORY.md` 记录关键决策/踩坑。
- **特别复验**：补一条**经 service→真实 DB 写入非整数金额（如 100.5、0.01）并校验回读值正确**的集成测试（C1 类问题的端到端守卫）。
