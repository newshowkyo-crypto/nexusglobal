# API — 接口契约文档

> 版本：V1.0（对应 PRD V1.5 第 11 章）
> 用途：AI 据此编写 handler、请求/响应结构体、集成测试。契约即测试基准。
> 配套：错误码见 §3，幂等规则见 §2，鉴权见 §1。金额/类型规���见 CODING_STANDARDS.md。

---

## 0. 全局约定

- **协议**：HTTP/1.1 + JSON（`Content-Type: application/json; charset=utf-8`）。
- **路由前缀**：`/admin/*`（后台管理）、`/merchant/*`（商户）、`/internal/*`（内部/Worker 专用，不对公网）、`/webhook/*`（外部回调入口，仅 Receiver）。
- **时间**：ISO-8601 UTC（`2026-06-12T08:00:00Z`）。
- **金额**：请求/响应中金额一律 **字符串**传递（避免 JSON number 精度丢失）。minor unit 字段以 `_minor` 结尾为整数字符串；账本金额为十进制字符串（如 `"1000.0000"`）。币种单独字段。
- **ID**：UUID 字符串。
- **分页**：`?limit=20&cursor=<opaque>`，响应含 `next_cursor`。

---

## 1. 鉴权

| 路由前缀 | 鉴权方式 | Header |
| --- | --- | --- |
| `/admin/*` | 后台 JWT（admin 角色） | `Authorization: Bearer <jwt>` |
| `/merchant/*` | 商户 API Key / JWT | `Authorization: Bearer <token>` |
| `/internal/*` | 内部服务令牌（mTLS 或共享密钥，仅集群内） | `X-Internal-Token: <token>` |
| `/webhook/*` | 提供方签名（HMAC/RSA） | `X-Signature`, `X-Signature-Timestamp`, `X-Provider-Event-Id` |

- 鉴权失败 → `401 UNAUTHORIZED`；越权 → `403 FORBIDDEN`。
- `/webhook/*` 不用 JWT，用提供方签名 + IP Allowlist；验签失败 → `401`，但**仍记录** `webhook_ingress_event(status=Rejected)`。

---

## 2. 幂等规则

- 所有**写接口**（POST/PATCH）必须支持 `Idempotency-Key` header（webhook 用 provider event id 派生）。
- 规则：
  - 同 Key + 同 Payload → 返回**首次结果**（200/201，附 `Idempotent-Replayed: true`）。
  - 同 Key + 不同 Payload → `409 IDEMPOTENCY_CONFLICT`。
  - Payload 比对用 `payload_hash`（请求体规范化后 SHA-256）。
- 保存周期：支付/账务/出金类 ≥365 天。
- 强制幂等对象：Payment Intent、Token、MAE 路由提交、Webhook、Sweep Transfer、Ledger Transaction、Reserve Hold、Payout Freeze、FX Lock、LP Payout、Reconciliation Adjustment。

请求头示例：

```
POST /merchant/payout-request
Authorization: Bearer <token>
Idempotency-Key: 9f1c2e7a-merchant42-20260612-001
Content-Type: application/json
```

---

## 3. 统一错误响应与错误码表

**错误响应体**（所有非 2xx）：

```json
{
  "error": {
    "code": "IDEMPOTENCY_CONFLICT",
    "message": "same idempotency key with different payload",
    "request_id": "req_01H...",
    "details": {}
  }
}
```

**错误码表**：

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` | 参数缺失/格式错误 |
| 400 | `ADDRESS_FORMAT_INVALID` | 出金地址格式校验失败 |
| 401 | `UNAUTHORIZED` | 鉴权失败/验签失败 |
| 403 | `FORBIDDEN` | 越权 |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `IDEMPOTENCY_CONFLICT` | 同 Key 不同 Payload |
| 409 | `ILLEGAL_STATE_TRANSITION` | 状态机非法迁移 |
| 409 | `VERSION_CONFLICT` | 乐观锁版本冲突 |
| 422 | `INSUFFICIENT_BALANCE` | 可出金余额不足 |
| 422 | `BENEFICIARY_NOT_ALLOWED` | 受益人类型禁止（UNKNOWN 等） |
| 422 | `OUTSIDE_BANKING_WINDOW` | 非银行窗口，已转累计/延迟 |
| 422 | `APPROVAL_REQUIRED` | 需四眼/合规审批 |
| 423 | `MAE_DAY_LOCKED` | MAE 当日 Exposure 锁定，不可恢复 |
| 429 | `RATE_LIMITED` | 限流 |
| 500 | `INTERNAL` | 内部错误 |
| 503 | `UNKNOWN_EXTERNAL_STATE` | 外部状态未知（超时），勿重试资金提交 |

---

## 4. MAE 管理接口

### 4.1 `POST /admin/mae` — 创建 MAE

**Request**：

```json
{
  "mae_id": "mae_acq_us_01",
  "display_name": "US Card Gateway 01",
  "mae_type": "CARD_GATEWAY",
  "tenant_scope": ["tenant_a", "tenant_b"],
  "jurisdiction": "US",
  "supported_currencies": ["USD", "EUR"],
  "api_base_url": "https://acq.example.com",
  "api_key": "sk_live_xxx",
  "webhook_secret": "whsec_xxx",
  "webhook_proxy_url": "https://wh.nexus.io/webhook/mae/<provider_id>",
  "risk_label": "tier1_low_risk",
  "settlement_bank_account_id": "uuid",
  "daily_vmax_minor": "100000000",
  "monthly_vmax_minor": "2000000000",
  "cmax_bps": 300,
  "card_testing_limit": 50,
  "routing_weight": 100,
  "cooldown_minutes": 30,
  "testing_traffic_bps": 500,
  "testing_min_txn_count": 100,
  "testing_min_success_rate_bps": 9500,
  "testing_max_p95_latency_ms": 800
}
```

- `api_key` / `webhook_secret`：服务端 **AES-256-GCM 加密** 后入库，响应**绝不回传明文**。
- 校验：`mae_type ∈ {CARD_GATEWAY,LOCAL_APM,BANK_TRANSFER,WALLET,A2A}`；vmax ≥ 0。

**Response 201**：

```json
{
  "id": "uuid",
  "mae_id": "mae_acq_us_01",
  "status": "Draft",
  "version": 0,
  "created_at": "2026-06-12T08:00:00Z"
}
```

错误：`400 INVALID_ARGUMENT`，`409 IDEMPOTENCY_CONFLICT`（mae_id 重复亦可 `409`）。

### 4.2 `PATCH /admin/mae/{mae_id}` — 更新 MAE

- 乐观锁：请求带 `version`，不匹配 → `409 VERSION_CONFLICT`。
- 可更新：路由权重、阈值、testing 参数、tenant_scope 等。不可直接改 `status`（用 4.4）。

### 4.3 `POST /admin/mae/{mae_id}/rotate-secret` — 轮换凭证

**Request**：`{ "api_key": "new", "webhook_secret": "new" }` → 重新加密入库，`secret_version` 递增。**Response 200**：`{ "rotated_at": "...", "secret_version": "v2" }`。

### 4.4 `POST /admin/mae/{mae_id}/status` — 手动状态变更

**Request**：`{ "target_status": "Suspended", "reason": "compliance_freeze", "version": 3 }`

- 仅允许状态机合法迁移；非法 → `409 ILLEGAL_STATE_TRANSITION`。
- 手动可达：`Active→Suspended`、`Suspended→Testing`、`Suspended→Disabled` 等。
- **Response 200**：`{ "status": "Suspended", "version": 4 }`

### 4.5 `GET /admin/mae/{mae_id}/metrics` — 实时指标

**Response 200**（来自 Redis 聚合 + Snapshot）：

```json
{
  "mae_id": "mae_acq_us_01",
  "business_date": "2026-06-12",
  "daily_captured_amount_minor": "5000000",
  "daily_settled_amount_minor": "4800000",
  "daily_gross_exposure_amount_minor": "5000000",
  "daily_refund_amount_minor": "120000",
  "daily_dispute_amount_minor": "30000",
  "abnormal_rate_bps": 150,
  "p95_latency_ms": 420,
  "card_testing_count": 2,
  "status": "Active"
}
```

### 4.6 `POST /internal/mae/{mae_id}/circuit-break` — 内部熔断

**Request**：`{ "reason": "EXPOSURE_THRESHOLD" }` → `Active→Cooldown`，若因 Exposure 则置 `business_day_lock_until`。**Response 200**：`{ "status": "Cooldown", "business_day_lock_until": "2026-06-12" }`。

### 4.7 `POST /internal/mae/{mae_id}/testing-evaluate` — 灰度评估

触发 Testing→Active / Cooldown / Suspended 评估。**Response 200**：`{ "decision": "PROMOTE|HOLD|SUSPEND", "status": "Active" }`。若当日锁未过 → `423 MAE_DAY_LOCKED`。

---

## 5. Webhook Proxy 接口

### 5.1 `POST /webhook/mae/{provider_id}` · `/webhook/bank/{provider_id}` · `/webhook/lp/{provider_id}`

**Headers**：`X-Signature`、`X-Signature-Timestamp`、`X-Provider-Event-Id`。

**Body**：提供方原始 payload（透传，服务端不解析业务前先验签）。

**处理**（见 TDD §6）：验 TLS/IP/限流 → 验签 → `payload_hash` → `idempotency_key` → 加密存 raw → 写 `webhook_ingress_event` → 投递 Redis Streams。

**Response 200**（**快速 ACK，不等核心入账**）：

```json
{ "received": true, "ingress_id": "uuid", "idempotency_key": "..." }
```

- 验签失败 → `401 UNAUTHORIZED`，记录 `status=Rejected`。
- 重复 `provider_event_id` → `200`（幂等，`"duplicated": true`），不重复投递。
- **契约红线**：此 handler 绝不直连账务/DB 写账，只投 MQ（CONSTRAINTS RED-09）。

### 5.2 `GET /admin/webhook/events` — 查询回调事件

Query：`provider_type`、`status`、`from`、`to`、`limit`、`cursor`。**Response 200**：事件列表（不含明文 payload，仅 hash/掩码）+ `next_cursor`。

### 5.3 `POST /admin/webhook/events/{id}/replay` — 重放

将事件重新投递回原 Streams。**Response 200**：`{ "replayed": true, "new_stream_id": "..." }`。仅 `DeadLetter/Rejected/Consumed` 可重放。

---

## 6. Treasury / Sweep 接口

### 6.1 `POST /admin/banking-calendar` — 创建银行日历

```json
{
  "calendar_name": "US_FED",
  "jurisdiction": "US",
  "timezone": "America/New_York",
  "business_days": ["MON","TUE","WED","THU","FRI"],
  "holidays": ["2026-07-04"],
  "emergency_closure_dates": [],
  "cut_off_matrix": { "ACH": "16:00", "FedWire": "17:00", "SWIFT": "15:00" }
}
```

**Response 201**：`{ "id": "uuid", "status": "Active" }`。

### 6.2 `POST /admin/sweep-rule` — 创建归集规则

```json
{
  "source_bank_account_id": "uuid",
  "target_bank_account_id": "uuid",
  "currency": "USD",
  "rail": "ACH",
  "beneficiary_type": "SAME_NAME",
  "same_name_verified": true,
  "trigger_threshold_minor": "50000000",
  "sweep_ratio_bps": 10000,
  "max_single_sweep_minor": "100000000",
  "min_residual_minor": "1000000",
  "time_window_matrix": { "ACH": [["09:00","16:00"]] },
  "scheduled_sweep_times": ["15:00"],
  "sweep_mode": "TIERED_SCHEDULED",
  "max_daily_sweep_count": 2,
  "delay_queue_enabled": true,
  "require_four_eyes": false,
  "approval_threshold_minor": "200000000"
}
```

校验：`beneficiary_type ∈ {SAME_NAME,THIRD_PARTY,THIRD_PARTY_CROSS_BORDER,UNKNOWN}`；`time_window_matrix` 必填。**Response 201**：`{ "id": "uuid", "status": "Active", "version": 0 }`。

### 6.3 `PATCH /admin/sweep-rule/{id}` — 更新（乐观锁 version）

### 6.4 `POST /internal/sweep/evaluate` — 触发归集评估

**Request**：`{ "sweep_rule_id": "uuid", "as_of": "2026-06-12T15:30:00Z" }`

**Response 200**（多种结局）：

```json
{ "decision": "SUBMIT|ACCUMULATE|DELAYED|APPROVAL_REQUIRED|REJECT",
  "sweep_transfer_id": "uuid|null",
  "reason": "OUTSIDE_BANKING_WINDOW|..." }
```

- 非窗口 → `decision=DELAYED`（已入 Delay Queue）或 `422 OUTSIDE_BANKING_WINDOW`。
- UNKNOWN 受益人 → `422 BENEFICIARY_NOT_ALLOWED`。
- 三方大额 → `decision=APPROVAL_REQUIRED`（或 `422 APPROVAL_REQUIRED`）。

### 6.5 `POST /internal/sweep/{id}/submit` — 提交归集（Worker 内部）

校验 `status=Locked` → 经 Worker 调银行 API（**非本接口同步外呼**，本接口仅驱动）。结果：`Submitted` / `UnknownExternalState`。超时 → `503 UNKNOWN_EXTERNAL_STATE`。

### 6.6 `POST /internal/sweep/{id}/reconcile` — 对账归集

依据银行 Webhook/查询确认 → `Confirmed→Reconciled`。

---

## 7. Ledger / Payout 接口

### 7.1 `GET /admin/merchant/{id}/balance` — 查询商户余额

**Response 200**：

```json
{
  "merchant_id": "uuid",
  "currency": "USD",
  "pending_balance": "1000.0000",
  "settled_balance": "8000.0000",
  "reserve_balance": "500.0000",
  "frozen_payout_balance": "200.0000",
  "dispute_hold_balance": "0.0000",
  "available_payout_balance": "7300.0000",
  "version": 12,
  "updated_at": "2026-06-12T08:00:00Z"
}
```

`available = settled - reserve - frozen - dispute`。

### 7.2 `POST /merchant/payout-address` — 添加出金地址

```json
{
  "target_asset": "USDT",
  "target_network": "TRON_TRC20",
  "address_format_type": "TRON_TRC20",
  "target_address": "TQ9a31...8xLm",
  "address_label": "main wallet"
}
```

处理（PRD 第 8 章）：trim → 去不可见字符 → `address_format_type` 与 `target_network` 一致校验 → 按网络格式校验 → normalize → `target_address_hash` → 黑/白名单匹配 → 加密存 → 触发 AML/Sanctions 筛查（异步，`screening_status=Pending`）。

**Response 201**：

```json
{ "id": "uuid", "target_address_masked": "TQ9a31****8xLm",
  "screening_status": "Pending", "whitelist_status": "Pending" }
```

格式错误 → `400 ADDRESS_FORMAT_INVALID`。响应**绝不回传明文地址**，仅掩码。

### 7.3 `POST /merchant/payout-request` — 创建代付请求

```json
{
  "payout_address_id": "uuid",
  "amount": "1000.0000",
  "fiat_currency": "USD",
  "target_asset": "USDT",
  "target_network": "TRON_TRC20"
}
```

- 必须 `Idempotency-Key`。
- 校验：地址 `screening_status=Passed` 且 `whitelist_status=Active`，否则 `422`。
- 创建 `payout_request(status=Draft)`，随后由 `/internal/payout/{id}/freeze` 驱动冻结（或本接口内部直接触发冻结 service）。

**Response 201**：

```json
{ "id": "uuid", "status": "BalanceFrozen|Draft", "idempotency_key": "..." }
```

余额不足 → `422 INSUFFICIENT_BALANCE`。

### 7.4 `POST /internal/payout/{id}/freeze` — 本地事务冻结余额

驱动 TDD §5.3 Phase1：`FOR UPDATE` → 校验 available → `settled-=amount, frozen+=amount` → 双轨分录 → `payout_request=BalanceFrozen` → 写 Outbox(FX_LOCK_REQUESTED)。**全程无外部 I/O**。

**Response 200**：`{ "status": "BalanceFrozen", "ledger_freeze_txn_id": "uuid" }`。余额不足 → `422 INSUFFICIENT_BALANCE`（事务回滚，不写 Outbox，不调 LP）。

### 7.5 `POST /internal/payout/{id}/fx-lock` — Worker 内部锁价

仅 Worker 调；校验 `status=BalanceFrozen`；Worker 在**事务外**调 LP FX Lock。成功 → `FxLocked` + Outbox(LP_PAYOUT_REQUESTED)；明确失败 → 触发释放；超时 → `503 UNKNOWN_EXTERNAL_STATE`（frozen 不动）。

### 7.6 `POST /internal/payout/{id}/submit` — Worker 内部提交 LP 代付

校验 `status=FxLocked`；事务外调 LP Payout。已提交 → `LPProcessing`；明确失败 → `Failed`（触发释放）；超时 → `503 UNKNOWN_EXTERNAL_STATE`。

### 7.7 `POST /internal/payout/{id}/reconcile` — 对账代付

依据 LP Webhook/查询：Confirmed → 核销 Frozen（Phase4）；Failed → 释放 Frozen（Phase5）；Unknown → 保持 Frozen 并继续查询。

### 7.8 `GET /admin/outbox` — 查询 Outbox 任务

Query：`status`、`aggregate_type`、`event_type`、`limit`、`cursor`。**Response 200**：任务列表（payload 不含明文敏感字段）。

### 7.9 `POST /admin/outbox/{id}/replay` — 重放 Outbox 任务

`Failed/DeadLetter` → 重置 `status=Pending, next_retry_at=now`。**Response 200**：`{ "replayed": true }`。

---

## 8. 健康检查与运维

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活（进程） |
| GET | `/readyz` | 就绪（PG + Redis 可达） |

`/readyz` 不就绪 → `503`。

---

## 9. Payout 状态机（响应中的 status 取值，契约约束）

```
Draft → BalanceFrozen → Screening? → FxLockPending → FxLocked
      → PayoutSubmitting → LPProcessing → Confirmed → Reconciled
分支：→ Failed → BalanceReleased
     → UnknownExternalState（任一外呼超时；不释放 frozen）
```

- handler/service 必须拒绝非法 status 跃迁，返回 `409 ILLEGAL_STATE_TRANSITION`。
- `UnknownExternalState` 期间任何"释放/重提"请求 → `409` 或 `503`，禁止操作 frozen。

---

## 10. 给 AI 的接口落地检查点

- [ ] 每个写接口实现 `Idempotency-Key` 中间件 + payload_hash 比对。
- [ ] 金额字段全部字符串进出，内部转 int64/decimal，**响应无 float**。
- [ ] 密文/明文敏感字段绝不出现在任何响应或日志。
- [ ] `/webhook/*` handler 不 import 账务 repository，只投 Streams。
- [ ] `/internal/*` 路由加内部令牌中间件，不暴露公网。
- [ ] 每个错误路径返回 §3 的标准错误体与正确 code。
- [ ] 状态机非法迁移统一 `409 ILLEGAL_STATE_TRANSITION`。
- [ ] 每个端点有对应集成测试（成功 + 主要错误码 + 幂等重放）。
