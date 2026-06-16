# NexusGlobal 项目进度、功能完成度与开发交接总览

生成日期：2026-06-14  
适用对象：后续开发 AI、Trae/Mimo 执行端、人工验收负责人  
当前结论：代码与 mock/本地预上线门禁 100% 通过；真实外部 Stripe/Nxtapay sandbox 尚缺凭证，真实联调不能宣称 100%。

## 1. 当前总状态

| 维度 | 完成度 | 状态说明 |
|---|---:|---|
| 本地 mock 演示闭环 | 100% | Postgres/Redis/migration/seed/API/Dashboard/checkout mock 均可本地运行 |
| 代码门禁 | 100% | `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./... -short -count=1` 均已通过 |
| 可视化管理后台 | 100%（按当前测试口径） | Dashboard 全中文化，危险操作确认，详情 API 接入，admin API 集成测试通过 |
| Admin API 真实性 | 100%（本地 DB） | 使用 testcontainers Postgres 跑 migrations，真实调用 handler/API 并查库验证 |
| mock/real 边界 | 通过 | local/mock 可用 mock；real/production 禁止 raw PAN/CVV，未知 provider fail-closed |
| 真实外部 sandbox | 未完成 | 缺 `STRIPE_SANDBOX_SECRET`、`NXTAPAY_SANDBOX_API_KEY` 等凭证，真实外部测试被 skip |
| 生产上线 | 未批准 | 需先跑真实 sandbox smoke、配置真实 KMS/Vault、替换正式凭证与审计参数 |

## 2. 已验收命令

最后一轮人工复核已运行：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/platform/httpx -count=1 -v
go test ./internal/admin -count=1 -v
go test ./... -short -count=1
go test ./test/integration -run "TestAcquirerSandbox_Real" -count=1 -v
```

结果：

- `gofmt -l .` 无输出。
- build/vet/short 全仓通过。
- Dashboard 测试通过。
- Admin API testcontainers 集成测试通过。
- `TestAcquirerSandbox_RealStripeSuccess` 因缺少 `STRIPE_SANDBOX_SECRET` 跳过。
- `TestAcquirerSandbox_RealNxtapaySuccess` 因缺少 `NXTAPAY_SANDBOX_API_KEY` 跳过。

## 3. 本地运行状态

本地运行方式已验证：

- Postgres：Docker Compose `postgres:15`
- Redis：Docker Compose `redis:7`
- API：`http://localhost:8080`
- Dashboard 预览代理：`http://127.0.0.1:8090/`
- 原始 Dashboard：`http://localhost:8080/admin/dashboard`，需要 `Authorization: Bearer <admin_jwt>`

本地已执行：

```bash
docker compose up -d postgres redis
go run ./cmd/migrate up
go run ./cmd/seed
go run ./cmd/api
```

本地 mock seed 结果：

- Provider：`mock`
- Channel：`mock_default`
- Tenant ID：`00000000-0000-0000-0000-000000000001`
- Merchant ID：`00000000-0000-0000-0000-000000000001`
- mock 场景：
  - `order_success` -> success
  - `order_fail` -> failure
  - `order_timeout` -> timeout

## 4. 项目核心功能

### 4.1 Checkout 托管收银台

已完成：

- `/api/checkout/token` 创建 checkout token。
- `/checkout?token=...` 托管收银台页面。
- `/api/checkout/validate` 校验 token。
- `/api/checkout/charge` 发起支付。
- mock/local 模式允许 raw card，用于本地演示。
- real/production 模式拒绝 raw card/PAN/CVV，必须使用 `payment_method_token`。
- checkout 已接入 ChannelRegistry，不再硬编码 mock gateway。
- mock order provider 可根据 order_id 生成确定性订单信息。

### 4.2 Acquirer Channel Registry

已完成：

- `internal/acquirer/registry`
- 内存对象池加载 active channel。
- Redis Pub/Sub 热重载。
- mock、stripe、nxtapay provider 适配入口。
- real mode 下缺凭证或未知 provider fail-closed。
- mock channel 支持 success/failure/timeout 场景。

### 4.3 Universal Adapter Registry

已完成：

- `CARD_ACQUIRER`
- `BANK_COLLECTION`
- `CRYPTO_PAY`
- `BANK_RAIL`
- `EXCHANGE`
- `LP`
- `ONCHAIN_WALLET`

已具备：

- provider/channel 通用注册表。
- 通用 adapter interface。
- tenant/entity/type 维度查询。
- local/mock 模式可回退 mock。
- real/production 模式未知 provider fail-closed。

### 4.4 Anti-Carding 风控网关

已完成：

- checkout charge 前置风控检查。
- Redis 滑动窗口维度：
  - buyer IP
  - merchant/app
  - card BIN
- 命中阈值直接阻断，不透传下游收单通道。
- 风控事件 API 与 Dashboard 风控运维 tab 已接入。

### 4.5 Dispute Ledger 逆向账本

已完成：

- 拒付、退款、拒付撤销状态机。
- `ORDER_REFUND`
- `CHARGEBACK_RECEIVED`
- `CHARGEBACK_REVERSED`
- platform dispute reserve 相关分录。
- settled balance 不足时 negative balance 表达。
- 事务一致性修复：repository 方法使用传入 tx。
- 0 金额 settled debit entry 已修复。

### 4.6 Multi-Asset Ledger 与 Asset Bridge

已完成：

- 多资产字段扩展。
- `FIAT` / `CRYPTO` / `STABLECOIN` 资产定义。
- `ASSET_EXCHANGE` 双轨分录测试。
- 20 bps 最大滑点保护测试。
- `UNKNOWN_EXTERNAL_STATE` 状态。
- `APPROVAL_REQUIRED` 状态。
- bridge profile、buffer percent、batching/execution/withdrawal policy。

### 4.7 Bank Collection 与 Crypto Pay

已完成：

- 银行虚拟户入金 adapter 骨架。
- 银行入金 intent、对账、金额/币种/reference mismatch 测试。
- Crypto address allocation 幂等。
- confirmations threshold。
- wrong network / amount mismatch / duplicate tx 测试。

### 4.8 Corporate Entity Isolation

已完成：

- `corporate_entity`
- entity risk profile
- entity asset limit
- entity daily flow
- RiskHold 隔离测试。
- RiskHold 阻断 sweep 测试。
- Dashboard 法人实体管理 tab。

### 4.9 Wallet Policy

已完成：

- transit wallet policy。
- allowlist 地址。
- wrong network 拒绝。
- single/daily limit。
- tx hash 幂等。
- confirmations 不足不得 finalize。
- Dashboard 钱包白名单 tab。

### 4.10 Outbox 与 Worker

已完成：

- transactional outbox。
- worker claim/ack/fail。
- retry_count / status / next_retry_at。
- Dashboard 待处理任务列表。
- `/admin/outbox/{id}` 详情 API 已接入。

## 5. 可视化管理后台完成情况

Dashboard 文件：`internal/platform/httpx/dashboard.go`

已完成：

- 全中文页面标题、导航、按钮、表头、状态展示。
- `/admin/dashboard` 与 `/admin/dashboard/data`。
- local preview 可通过 `http://127.0.0.1:8090/` 查看。
- JWT token 通过 localStorage/Authorization 机制用于真实后台访问。
- Dashboard 测试覆盖：
  - 无非法 `< button`
  - 无乱码字符
  - 新 tab 中文标签存在
  - 禁止英文裸标签
  - 状态翻译存在
  - 类型翻译存在
  - `errMsg` 统一错误函数存在
  - 危险操作 confirm 存在
  - 不含“请到数据库查询”
  - 详情按钮调用真实 API

已完成 tab：

- 总览
- 通用适配器
- 法人实体
- 资产桥配置
- 钱包白名单
- 风控运维
- 异步任务
- 商户余额
- 接口清单
- 系统信息
- 老通道管理兼容入口

危险操作确认：

- 禁用通道
- 风险冻结法人实体
- 禁用资产桥配置
- 禁用钱包策略

详情能力：

- 风险事件详情：调用 `/admin/risk/events/{id}`
- 任务详情：调用 `/admin/outbox/{id}`

## 6. Admin API 完成情况

核心路由：

- `GET/POST/PATCH /admin/adapter/providers`
- `GET/POST/PATCH /admin/adapter/channels`
- `POST /admin/adapter/channels/{id}/enable`
- `POST /admin/adapter/channels/{id}/disable`
- `POST /admin/adapter/channels/{id}/reload`
- `GET/POST/PATCH /admin/entities`
- `POST /admin/entities/{id}/risk-hold`
- `POST /admin/entities/{id}/enable`
- `GET/POST/PATCH /admin/asset-bridge/profiles`
- `POST /admin/asset-bridge/profiles/{id}/enable`
- `POST /admin/asset-bridge/profiles/{id}/disable`
- `GET/POST/PATCH /admin/wallet-policies`
- `POST /admin/wallet-policies/{id}/enable`
- `POST /admin/wallet-policies/{id}/disable`
- `GET /admin/outbox`
- `GET /admin/outbox/{id}`
- `GET /admin/risk/events`
- `GET /admin/risk/events/{id}`

Admin API 测试：

- `TestAdminAdapterProvidersAPI`
- `TestAdminAdapterChannelsAPI`
- `TestAdminEntitiesAPI`
- `TestAdminBridgeProfilesAPI`
- `TestAdminWalletPoliciesAPI`

测试方式：

- testcontainers 启动真实 Postgres。
- 运行 migrations。
- 调用 handler。
- POST 后查库验证字段。

## 7. 当前真实边界

不得误报：

- 本地 mock 100% 不等于真实外部通道 100%。
- Dashboard 100% 不等于生产可上线 100%。
- 真实 sandbox 缺凭证，Stripe/Nxtapay 外部请求未在当前环境完成。

真实 sandbox 待跑：

```bash
set STRIPE_SANDBOX_SECRET=...
set NXTAPAY_SANDBOX_API_KEY=...
set NXTAPAY_SANDBOX_API_SECRET=...

go test ./test/integration -run "TestAcquirerSandbox_Real" -count=1 -v
```

生产前还必须确认：

- `APP_ENV=production`
- `KMS_MODE=real`
- `EXTERNAL_MODE=real`
- `ACQUIRER_MODE=real`
- Vault/KMS 可用
- 真实 provider credential 已加密入库
- Stripe/Nxtapay webhook secret 已配置
- 真实 sandbox 成功、幂等入账成功、失败/超时/拒付路径通过

## 8. 后续开发 AI 接手规则

1. 不要重写已通过的 mock/local 闭环。
2. 不要删除 ChannelRegistry、UniversalAdapterRegistry、Anti-Carding、Dispute Ledger、Outbox。
3. 不要把 mock success 写进 real/production 分支。
4. 不要在数据库事务里做外部 HTTP 写操作。
5. 所有新增 admin 页面必须同步新增真实 API 测试。
6. 所有真实外部 provider 必须有 contract/integration tests。
7. 报告必须区分：
   - 本地 mock 已完成
   - sandbox 已完成
   - production 已完成

## 9. 下一步建议

优先级 P0：

- 配置 Stripe/Nxtapay sandbox 凭证。
- 跑 `TestAcquirerSandbox_Real`。
- 修复真实 sandbox 暴露的签名、错误码、3DS、capture/refund/webhook 映射问题。

优先级 P1：

- 用真实浏览器人工走一遍 Dashboard 所有 tab 的创建、启用、禁用、详情查看。
- 增加 E2E 浏览器截图验收。
- 把 README 中仍有历史乱码/旧命令的部分整理掉。

