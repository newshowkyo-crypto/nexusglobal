# FIX_ORDER_R3 — 收尾工单（预上线剩余开发项）

> 来源：`STATUS.md` §4「切真实上线动作」。G1-G10 + C1 已修并复验通过（勿动）。
> 本工单做**剩下可由代码完成的开发项**（R3-1~R3-3）。另有 2 项是**纯运维动作、需真实凭证/基础设施，AI 代理无法完成**，列在 §尾，由人工在上线时执行。
> 纪律同 `FIX_ORDER.md` §0（禁 stub/禁谎报/资金红线/金额禁 float 用 `money.DecimalToNumeric`/每项真实测试/门禁全过贴完整输出/失败2次换方案）。
> **禁止 `sqlc generate`**（生成码与 sqlc.yaml 的 numeric→decimal override 不一致，会全仓乱改）；改生成码请定向手改，字段顺序对齐 `models.go`。

---

## R3-1 🟠 托管收银台 HandleCharge 真实化 + OrderProvider 接线

**现状（已核查）**：
- `internal/checkout/handler.go:165` `HandleCharge` 收 `card_number/card_cvv` 但**只返回写死 `{"success":true,"redirect_url":"/checkout/success"}`**，从不发起扣款。
- `cmd/api/main.go:236` `checkout.NewHandler(tokenSvc, nil, ...)` —— `OrderProvider` 传 **nil**，导致 `ServeCheckoutPage`/`HandleValidateToken` 只能渲染占位商户/金额。

**要做**（在 mock 模式下让整条扣款链路真实可跑，切 real 仅换底层连接器）：
1. **OrderProvider 真实实现**：实现一个按 token 绑定的订单/商户信息提供者（读 merchant descriptor + 订单金额/币种/订单号），在 `main.go` 注入真实实例，不再传 nil。`ServeCheckoutPage`/`HandleValidateToken` 渲染真实商户名/金额/订单。
2. **HandleCharge 接 MAE 路由扣款**：
   - 先用 Single-Use Token 校验（已实现）；token 无效/过期/已用 → 拒绝。
   - 经 **MAE 路由引擎选 MAE + MAE 连接器**（`pkg/external/mae`，工厂按 `EXTERNAL_MODE` 切 mock/real）发起扣款；mock 实现要能模拟成功/显式失败/超时三态（超时→不臆断成功）。
   - 扣款结果驱动后续：成功→标记 token 已用、返回真实结果；失败→返回错误，不返回假成功。
   - **资金/合规红线**：账务仍走既有 webhook captured→核心消费者入账路径，HandleCharge 不直接写账本；**绝不持久化或日志记录 PAN/CVV/明文卡号**（确认 RequestLogger 不记录 body）。
   - 幂等：同一 token/订单重复提交不重复扣款。
3. 若 `web/checkout/` 模板缺失的优雅降级保留，但模板存在时不得退化为占位数据。

**验收**：
- 集成/单测：有效 token + mock MAE 成功 → 扣款成功路径；mock MAE 失败/超时 → 对应错误，**无假成功**；过期/重复 token → 拒绝；断言无任何地方持久化/打印卡号。
- `ServeCheckoutPage` 渲染真实订单数据（非 `"NexusGlobal Merchant"`/`"0.00"` 占位）。

---

## R3-2 🟠 Single-Use Token 存储换 Redis（支持多副本）

**现状（已核查）**：`internal/token/` 只有 `MemoryStore`；`Store` 接口（token.go:48）已定义；`cmd/api/main.go:230` 用 `token.NewMemoryStore()`。多副本部署 token 不共享、重启丢失。

**要做**：
1. 新增 `internal/token/redis_store.go`：用 `*redis.Client` 实现 `token.Store` 接口全部方法（Set/Get/MarkUsed/TTL 等，以接口为准）；token 状态、使用次数、TTL 用 Redis 原子操作（单次使用要防并发兑换——用 `SETNX`/Lua 保证「检查+置已用」原子）。
2. **工厂/配置切换**：按配置选择 store（如 `TOKEN_STORE=memory|redis` 或复用现有 mode；real/production 默认 redis）。`main.go` 注入对应实现，禁止硬编码。
3. 复用既有 `rdb`（worker/api 已有 redis 客户端）。

**验收**：
- 单测（可用 miniredis 或 testcontainers redis）：单次使用成功后再次兑换被拒；TTL 过期失效；并发兑换只成功一次。
- `RedisStore` 与 `MemoryStore` 行为契约一致（同一组测试两种实现都过）。

---

## R3-3 🟠 运营后台数据录入（B 类业务数据）

**现状（已核查）**：`internal/admin/handler.go` 全是 `List`/`Get`（只读），无任何 Post/Put/INSERT/UPDATE；`/admin/dashboard` 10 标签页只能看。`ONBOARDING_DATA.md §4` 要求能录入 7 张 B 类表。

**要做**：为以下 7 张表提供**创建/编辑**能力（后端 endpoint + 后台最小表单），按 `ONBOARDING_DATA.md` 的字段与录入顺序：
1. 银行账户 2. 银行日历 3. MAE 配置 4. 归集规则 5. Webhook 代理 6. 商户 7. 出金地址。
- 后端：在 `/admin` 鉴权组下加 `POST`/`PUT` 端点，走真实 repo 写库（参数校验、敏感字段加密如 MAE api_key / webhook secret / 出金地址）。
- 前端：`dashboard.go` 对应标签页加最小可用表单（新增/编辑），提交到上述端点。
- 不求花哨，但**必须真实写库**，禁止假成功。

**验收**：集成测试——经新端点创建一条记录后能被对应 `List`/`Get` 读出；加密字段落库为密文；非法输入被拒。
（若工作量大，可按表分批，但每张表「能录入+能读出+有测试」才算完成，禁止只做表单不接库。）

---

## 完工定义（DoD）

- R3-1/R3-2/R3-3 全部 `已实现并测试`；每项真实测试，资金/卡号红线不破。
- 门禁全过并贴**完整** `go test ./... -count=1` 输出（零 FAIL/panic/SKIP；`internal/platform/stream` 偶发 `rootless Docker ... failed to create Docker provider` 是 Windows testcontainers 抖动，**重跑确认通过**并说明，不得当作通过略过）。
- 如实更新 `STATUS.md` §4 对应条目。不得声称「全部完成/可上线」。

---

## ⛔ 不在本工单内（纯运维动作，需真实凭证/基础设施，AI 代理不要尝试伪造）

> 这两项不是代码缺陷，是上线时人工执行（照 `GO_LIVE.md`）：
1. **填真实凭证 + 切 `*_MODE=real`**：LP/Bank/MAE 的 base_url+api_key、Vault KMS、生产 PG/Redis、JWT/Internal 密钥。代理只需保证代码路径完整（R3 已覆盖），**不得把假凭证写进仓库冒充完成**。
2. **生产部署与切换后验证**：`docker compose up` 全栈、跑 `GO_LIVE.md §9` 9 项切换后验证 checklist、灰度放量、监控 metrics/tracing。
