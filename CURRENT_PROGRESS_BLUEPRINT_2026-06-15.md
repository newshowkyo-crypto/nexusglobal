# NexusGlobal 当前进度、功能清单与工程蓝图

生成日期：2026-06-15  
文档定位：当前唯一的 mock/local 预上线进度说明与后续工程蓝图。  
结论：mock/local 预上线级 100% 已达成；真实 sandbox 与 production 仍需凭证、部署和外部联调单独验收。

---

## 1. 当前总判定

| 范围 | 当前状态 | 说明 |
|---|---:|---|
| mock/local 后端闭环 | 100% | API、worker、Postgres、Redis、migration、seed、checkout、dashboard、admin API、账本和异步任务均可本地运行与测试 |
| mock/local 预上线门禁 | 100% | gofmt/build/vet/test 已复核通过 |
| 可视化管理后台 | 100%（mock/local 范围） | Dashboard V3 tab 自动拉取真实 Admin API；危险操作有确认；错误和空态有中文提示 |
| V3 合规管理 API | 100%（mock/local 范围） | 通道风控、商户准备金、费用摊销、管道隔离、RFI、override audit 均有 handler 与集成测试 |
| checkout 订单源边界 | 通过 | local 可用 mock；production 禁止 mock；http order provider 未实现时 fail-closed |
| Stripe/Nxtapay 真实 sandbox | 未完成 | 缺少真实 sandbox 凭证，相关测试显式 skip |
| production 上线 | 未完成 | 需要真实 KMS/Vault、真实 provider 凭证、真实数据库/Redis、部署和外部链路 smoke |

一句话版本：项目已经到达 **mock/local 预上线 100%**，但不能写成“真实外部通道/生产 100%”。

---

## 2. 已复核门禁

已实跑并通过：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/platform/httpx -count=1 -v
go test ./internal/admin -count=1 -v
go test ./...
```

真实 sandbox 复核：

```bash
go test ./test/integration -run "TestAcquirerSandbox_Real" -count=1 -v
```

结果：测试包 PASS，但真实 Stripe/Nxtapay case 因缺凭证显式 skip：

- `STRIPE_SANDBOX_SECRET`
- `NXTAPAY_SANDBOX_API_KEY`
- `NXTAPAY_SANDBOX_API_SECRET`
- `NXTAPAY_SANDBOX_WEBHOOK_SECRET`

---

## 3. 当前已完成功能

### 3.1 Checkout 托管收银台

已完成：

- `/api/checkout/token` 创建 checkout token。
- `/checkout?token=...` 打开托管收银台页面。
- `/api/checkout/validate` 校验 token 并返回订单展示数据。
- `/api/checkout/charge` 发起支付。
- mock/local 支持 raw card 演示。
- real/production 拒绝 raw PAN/CVV，必须使用 `payment_method_token`。
- 支付链路接入 Acquirer Channel Registry，不再依赖硬编码 mock gateway。
- `CHECKOUT_ORDER_PROVIDER=mock|http|disabled` 已接入：
  - `mock`：仅 local/mock 使用。
  - `disabled`：不注册 checkout 路由。
  - `http`：当前未实现真实 HTTP 订单源，启动 fail-closed，禁止 mock fallback。
- production 禁止 `CHECKOUT_ORDER_PROVIDER=mock`。

仍未完成：

- 真实商户订单 HTTP provider。
- 真实前线站点 SDK 与订单系统联调。

### 3.2 Acquirer Channel Registry

已完成：

- provider/channel/credential 管理。
- active channel 启动预热。
- Redis Pub/Sub 热重载。
- mock/stripe/nxtapay provider 入口。
- real/production 缺凭证或未知 provider fail-closed。
- mock channel 支持 success/failure/timeout 三态。

仍未完成：

- Stripe/Nxtapay 真实 sandbox 凭证联调。
- 真实 3DS/requires_action 前端处理闭环。
- 真实 provider refund/query/webhook 全路径验收。

### 3.3 Universal Adapter Registry

已完成 adapter 类型：

- `CARD_ACQUIRER`
- `BANK_COLLECTION`
- `CRYPTO_PAY`
- `BANK_RAIL`
- `EXCHANGE`
- `LP`
- `ONCHAIN_WALLET`

已完成能力：

- 通用 provider/channel 注册表。
- tenant/entity/type 维度查询。
- mock/local fallback。
- real/production 未知 provider fail-closed。

### 3.4 Anti-Carding 风控网关

已完成：

- checkout charge 前置风控检查。
- Redis 滑动窗口。
- buyer IP、merchant/app、card BIN 多维度计数。
- 命中阈值后直接阻断，不调用下游收单通道。
- 风控事件进入 Admin/Dashboard 运维视图。

### 3.5 Ledger / Payout / Treasury

已完成：

- 双轨账本。
- 借贷平衡断言。
- Payout 五阶段：冻结、FX lock、LP payout、confirm、failed release。
- 外部调用通过 worker/outbox，不在 DB transaction 内做外呼。
- 幂等 payload hash 冲突检测。
- 对账分布式锁。
- Sweep 窗口判断、审批和源余额扣减。
- 非整数金额精度回归测试。

### 3.6 Dispute / Reserve / Fee

已完成：

- refund confirmed。
- chargeback received。
- chargeback reversed。
- platform dispute reserve。
- merchant negative balance。
- rolling reserve release worker。
- fee allocation service。
- provider 未返回 fee 时进入 review/pending 边界。

### 3.7 Bank Collection / Crypto Pay / Wallet Policy

已完成：

- 银行入金 intent。
- 银行入金对账。
- reference/amount/currency mismatch 测试。
- crypto address allocation。
- confirmations threshold。
- wrong network、amount mismatch、duplicate tx 测试。
- wallet allowlist。
- single/daily limit。
- tx hash 幂等。

### 3.8 Corporate Entity / Pipeline Isolation

已完成：

- corporate entity。
- entity risk profile。
- entity asset limit。
- entity daily flow。
- RiskHold 阻断 sweep。
- pipeline isolation group。
- freeze/unfreeze audit log。
- 跨 group 操作 fail-closed 的工程骨架。

### 3.9 Webhook / Stream / DLQ

已完成：

- Webhook receiver proxy。
- provider webhook 验签边界。
- Redis Streams 投递。
- LP/MAE/Bank 三类 consumer。
- pending reclaim。
- DLQ 触发。
- replay/list events 接真实 repo。
- webhook secret decrypt 后验签，缺密钥 fail-closed。

### 3.10 Dashboard 可视化管理后台

已完成：

- `/admin/dashboard`
- `/admin/dashboard/data`
- 全中文化主界面。
- JWT localStorage/Authorization 访问。
- V3 tab 点击自动拉取真实 API：
  - 通道风控。
  - 商户准备金。
  - 费用摊销。
  - 管道隔离。
  - 合规资料包。
- 危险操作确认：
  - 禁用通道。
  - 禁用桥接配置。
  - 禁用钱包策略。
  - 风险冻结。
  - pipeline freeze/unfreeze。
  - RFI approve/reject。
- 空态、失败态、保存失败均有中文提示。
- 删除 provider 的前端按钮与后端 `DELETE /admin/adapter/providers/{id}` 已对齐。

---

## 4. V3 合规升级完成范围

已完成 API：

- `GET/PATCH /admin/v3/channel-risk`
- `GET/POST /admin/v3/merchant-risk-profiles`
- `GET /admin/v3/rolling-reserves`
- `GET /admin/v3/fee-allocations`
- `GET/POST /admin/v3/isolation-groups`
- `POST /admin/v3/isolation-groups/{id}/freeze`
- `POST /admin/v3/isolation-groups/{id}/unfreeze`
- `POST /admin/v3/rfi-documents/generate`
- `GET /admin/v3/rfi-documents`
- `GET /admin/v3/rfi-documents/{id}`
- `POST /admin/v3/rfi-documents/{id}/approve`
- `POST /admin/v3/rfi-documents/{id}/reject`
- `GET /admin/v3/override-audit-logs`

已完成测试：

- `TestV3ChannelRiskAPI`
- `TestV3MerchantRiskProfilesAPI`
- `TestV3RollingReservesAPI`
- `TestV3FeeAllocationsAPI`
- `TestV3IsolationGroupsAPI`
- `TestV3IsolationGroupFreezeUnfreeze`
- `TestV3RFIDocumentsAPI`
- `TestV3OverrideAuditLogsAPI`

---

## 5. 工程蓝图

### Phase A：mock/local 预上线闭环（已完成）

目标：本地零真实外部依赖，完整证明业务逻辑、账本、风控、后台、异步任务和可视化管理可跑通。

完成状态：100%。

### Phase B：真实 sandbox 联调（下一阶段 P0）

目标：接入真实 sandbox provider，但仍不进生产。

任务：

1. 配置 Stripe sandbox 凭证。
2. 配置 Nxtapay sandbox 凭证。
3. 跑通 authorize/capture/refund/query。
4. 跑通 provider webhook 验签。
5. 跑通 captured/settled/failed/timeout/dispute/refund 事件映射。
6. 补齐真实 sandbox 报告。

验收命令：

```bash
go test ./test/integration -run "TestAcquirerSandbox_Real" -count=1 -v
```

### Phase C：staging 预生产部署

目标：真实基础设施、真实 secret 管理、真实网络边界，但仍不承载生产资金。

任务：

1. `APP_ENV=staging`
2. `KMS_MODE=real`
3. `EXTERNAL_MODE=real`
4. `ACQUIRER_MODE=real`
5. `CHECKOUT_ORDER_PROVIDER=http`
6. 接入真实 order provider。
7. 部署 Postgres/Redis/Vault/OTel。
8. 跑 smoke、loadtest、dashboard 手工验收。

### Phase D：production 上线

目标：正式生产运行。

前置条件：

- 真实 sandbox 全绿。
- staging smoke 全绿。
- KMS/Vault 可用。
- provider credential 加密入库。
- webhook secret 加密入库。
- dashboard 管理操作 audit log 完整。
- rollback checklist 已演练。
- 监控、告警、日志、备份、灾备已就绪。

---

## 6. 不得误报的边界

以下说法不允许：

- “真实生产 100% 完成”
- “Stripe/Nxtapay 已 100% 完成”
- “sandbox 已通过”但实际缺凭证 skip
- “real 模式可用”但内部 fallback mock
- “RFI 可自动生成真实资料”但源数据缺失

允许说法：

- “mock/local 预上线级 100% 达成”
- “真实 sandbox 待凭证联调”
- “production 待部署与外部验收”

---

## 7. 下一步最短路径

1. 准备 Stripe/Nxtapay sandbox 凭证。
2. 实现真实 HTTP checkout order provider。
3. 在 staging 部署 Vault/Postgres/Redis/API/Worker/Dashboard。
4. 跑真实 sandbox test。
5. 用浏览器人工点完 Dashboard 所有 tab、创建、启用、禁用、详情、审核、冻结、解冻。
6. 补一份 staging smoke report。

