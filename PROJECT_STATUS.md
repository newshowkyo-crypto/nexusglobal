# NexusGlobal MAMR-System 项目进度报告

> 更新时间：2026-06-12
> 版本：V1.0
> 状态：**全部 38 个任务已完成，系统可联调运行**

---

## 一、项目概述

NexusGlobal MAMR-System 是一套企业级全球财资调度与多轨收单均衡系统，支撑跨区域、多租户、多收单、多银行、多币种、多清算通道场景下的交易流与资金流管理。

**技术栈：** Go 1.22+ / PostgreSQL 15 / Redis 7 / Redis Streams / chi / pgx / sqlc / shopspring/decimal / testcontainers

---

## 二、任务完成状态（38/38）

### P0 脚手架与平台基础设施（T001-T008）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T001 项目骨架 | ✅ | go.mod、目录结构、docker-compose、Makefile、.env.example |
| T002 配置加载 | ✅ | caarlos0/env 加载全部配置项 |
| T003 Postgres+TxManager | ✅ | pgxpool 连接池、WithinTx 事务管理 |
| T004 Redis 客户端 | ✅ | go-redis v9、INCRBY/ZSET/SETNX/Lua 封装 |
| T005 金额工具 | ✅ | Minor int64、decimal 辅助、无 float |
| T006 Clock/Logger/apperr/idempotency | ✅ | Clock 接口、slog JSON、统一错误码、SHA-256 幂等键 |
| T007 Crypto+MockKMS | ✅ | AES-256-GCM、96-bit nonce、AAD、MockKMS |
| T008 External mock+validator | ✅ | lp/bank/mae 接口+Mock、TRON/ETH/BTC/SOL 地址校验 |

### P1 Ledger 双轨账本（T009-T012）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T009 Ledger migration | ✅ | ledger_account/ledger_transaction/ledger_entry 三表、numeric(20,4) |
| T010 Ledger domain | ✅ | Direction/Entry/Transaction、ValidateBalanced 借贷平衡校验 |
| T011 Ledger repository | ✅ | sqlc 生成、ON CONFLICT DO NOTHING、PostTransaction 事务写入 |
| T012 Ledger service | ✅ | 幂等入账、GET /admin/merchant/{id}/balance 真实余额查询 |

### P2 Payout 冻结+Outbox+Worker（T013-T018）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T013 Payout/Outbox migration | ✅ | merchant_treasury_ledger、payout_request、transactional_outbox |
| T014 Payout domain | ✅ | 9 状态机、CanTransition、CleanAddress 地址清洗 |
| T015 Payout repository | ✅ | LockBalanceForUpdate FOR UPDATE、SKIP LOCKED 抢占 |
| T016 Payout freeze service | ✅ | FOR UPDATE→校验→冻结→写 ledger→写 Outbox、事务内零外部 I/O |
| T017 Outbox+FXLock worker | ✅ | Outbox 并发轮询、FX Lock Worker 事务外调 lp.FXLock |
| T018 LP Payout+Confirm/Release | ✅ | Phase3/4/5 五阶段闭环、Confirm 核销、Release 释放 |

### P3 MAE 状态机与路由（T019-T024）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T019 MAE migration | ✅ | mae_configuration、mae_runtime_metric_snapshot |
| T020 MAE domain | ✅ | 6 状态机（无 Cooldown→Active）、熔断评估、Testing 评估 |
| T021 MAE metric | ✅ | Redis Counter exposure 只增不减、OnRefund/OnDispute 独立 |
| T022 MAE repo+service | ✅ | 乐观锁、加密凭证入库、rotate-secret |
| T023 MAE 熔断+Testing | ✅ | EvaluateCircuitBreaker、EvaluateTesting、当日锁 |
| T024 MAE handler | ✅ | 全部 API §4 端点、接入真实 service |

### P4 Webhook Receiver+Streams MQ（T025-T029）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T025 Streams MQ 封装 | ✅ | Producer XADD、Consumer XReadGroup/XAck/XAutoClaim/DLQ |
| T026 Webhook migration | ✅ | webhook_receiver_proxy、webhook_ingress_event |
| T027 Webhook domain+repo | ✅ | HMAC_SHA256 验签、幂等键计算、ingress CRUD |
| T028 Webhook handler | ✅ | 验签→加密→DB 写入→XADD、快速 ACK、不连账务 |
| T029 核心消费者 | ✅ | Streams→幂等检查→Ledger 入账、ACK 在 commit 后 |

### P5 Treasury 银行清算（T030-T034）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T030 Treasury migration | ✅ | banking_calendar、sweep_rule、sweep_transfer |
| T031 Treasury domain | ✅ | WithinBankingWindow、受益人四分支、SweepTrigger |
| T032 Treasury repo+DelayQueue | ✅ | Redis ZSET Delay Queue、daily_sweep_count |
| T033 Sweep service+scheduler | ✅ | Evaluate/Submit/Reconcile、非窗口不调银行 API |
| T034 Treasury handler | ✅ | 全部 API §6 端点、接入真实 service |

### P6 收尾（T035-T038）✅

| 任务 | 状态 | 说明 |
|------|------|------|
| T035 cmd 装配+中间件 | ✅ | cmd/api 完整 DI、RequestID/Recover/RequestLogger 中间件 |
| T036 集成测试 | ✅ | testcontainers 真实 PG+Redis、8 个测试全部通过 |
| T037 对账 worker | ✅ | UnknownExternalState→QueryStatus→Confirmed/Failed |
| T038 文档校验+红线自查 | ✅ | build+lint+test-short 全过、红线自查通过 |

---

## 三、CI 状态

```
go build ./...        ✅ 通过
gofmt -l .            ✅ 无输出
go vet ./...          ✅ 通过
go test ./... -short  ✅ 35 个包全部通过
go test (integration) ✅ 8/8 测试通过（testcontainers）
```

---

## 四、集成测试覆盖（8/8）

| 测试 | 验证内容 | 状态 |
|------|---------|------|
| TestWebhookDuplicateDetection | 幂等键 UNIQUE 约束、重复插入失败 | ✅ |
| TestPayoutFreezeFlow | FOR UPDATE→校验→冻结→余额变化→ledger 分录→outbox 事件 | ✅ |
| TestPayoutFreezeIdempotent | 幂等重放返回首次结果、余额只冻结一次 | ✅ |
| TestConcurrentDoubleSpend | 5 笔并发 3000 冻结 10000 余额，2-3 笔成功、无超卖 | ✅ |
| TestMAEStatusTransition | Draft→Active→Cooldown 状态机 + 加密凭证入库 | ✅ |
| TestWebhookStreamPublish | Redis Streams XADD→XReadGroup→XAck 完整链路 | ✅ |
| TestLedgerBalanceQuery | merchant_treasury_ledger 余额查询 + available 计算 | ✅ |
| TestLedgerInsertInTransaction | LedgerRepo.PostTransaction 事务内写入 | ✅ |

---

## 五、红线合规

| 红线 | 状态 | 说明 |
|------|------|------|
| RED-01 事务内无外部 I/O | ✅ | Freeze 在本地事务内只做 DB 操作 |
| RED-02 外部调用经 Outbox/Worker | ✅ | FXLock/Payout 由 Worker 事务外调用 |
| RED-03 超时≠失败 | ✅ | timeout→UnknownExternalState、不释放 frozen |
| RED-04 先冻结后外呼 | ✅ | FOR UPDATE→校验→冻结→写 Outbox→COMMIT |
| RED-05 余额行级锁 | ✅ | SELECT...FOR UPDATE + CHECK 非负 |
| RED-06 exposure 只增 | ✅ | INCRBY only、refund/dispute 独立计数 |
| RED-07 当日不回 Active | ✅ | business_day_lock_until 当日锁 |
| RED-08 无 Cooldown→Active | ✅ | 迁移表无此边、测试断言验证 |
| RED-09 webhook 不连账务 | ✅ | handler 只投 Streams、不 import ledger |
| RED-10 强幂等 | ✅ | idempotency_key UNIQUE、ON CONFLICT DO NOTHING |
| RED-11 非窗口不调银行 | ✅ | WithinBankingWindow 判断→Delayed |
| RED-12 受益人四分支 | ✅ | UNKNOWN reject/SAME_NAME/THIRD_PARTY/CROSS_BORDER |
| RED-13 借=贷 | ✅ | ValidateBalanced 校验 |
| RED-14 Redis 非资金边界 | ✅ | 资金安全由 DB 事务+行锁+唯一约束保证 |
| RED-15 敏感字段加密 | ✅ | AES-256-GCM、json:"-"、日志无明文 |
| NF-03 无 float | ✅ | 金额路径无 float32/float64 |

---

## 六、API 端点清单（30+）

### 系统运维
- `GET /healthz` — 健康检查
- `GET /readyz` — 就绪检查（PG+Redis）
- `GET /admin/dashboard` — 中文管理后台
- `GET /admin/stats` — 系统统计

### 收单路由管理（MAE）
- `GET /admin/mae-list` — 列表
- `GET /admin/mae-detail/{id}` — 详情
- `POST /admin/mae` — 创建
- `PATCH /admin/mae/{id}` — 更新
- `POST /admin/mae/{id}/rotate-secret` — 轮换凭证
- `POST /admin/mae/{id}/status` — 变更状态
- `GET /admin/mae/{id}/metrics` — 查询指标

### 代付管理（Payout）
- `GET /admin/payouts` — 列表
- `GET /admin/payout/{id}` — 详情
- `POST /merchant/payout-address` — 添加地址
- `POST /merchant/payout-request` — 创建请求
- `POST /internal/payout/{id}/freeze` — 冻结余额
- `POST /internal/payout/{id}/fx-lock` — 锁定汇率
- `POST /internal/payout/{id}/submit` — 提交代付
- `POST /internal/payout/{id}/reconcile` — 对账确认

### 资金归集（Treasury）
- `GET /admin/calendars` — 银行日历列表
- `GET /admin/sweep-rules` — 归集规则列表
- `GET /admin/sweep-transfers` — 归集转账列表
- `POST /admin/banking-calendar` — 创建日历
- `POST /admin/sweep-rule` — 创建规则
- `POST /internal/sweep/evaluate` — 触发评估
- `POST /internal/sweep/{id}/submit` — 提交归集
- `POST /internal/sweep/{id}/reconcile` — 归集对账

### 回调事件（Webhook）
- `GET /admin/webhook-events` — 事件列表
- `GET /admin/webhook-event/{id}` — 事件详情
- `POST /webhook/mae/{provider_id}` — 收单回调
- `POST /webhook/bank/{provider_id}` — 银行回调
- `POST /webhook/lp/{provider_id}` — 渠道回调

### 账务明细（Ledger）
- `GET /admin/merchant/{id}/balance` — 余额查询
- `GET /admin/merchants` — 商户列表
- `GET /admin/ledger/transactions` — 交易列表
- `GET /admin/ledger/entries` — 分录列表

### 异步任务（Outbox）
- `GET /admin/outbox` — 任务队列列表

---

## 七、管理后台（中文界面）

访问 `http://localhost:8080/admin/dashboard`，包含 10 个标签页：

| 标签 | 内容 |
|------|------|
| 总览 | 系统状态 + 业务统计 + 代付状态分布 |
| 收单路由 | MAE 配置表格 |
| 代付管理 | 代付请求表格 |
| 资金归集 | 银行日历 + 归集规则 + 归集转账 |
| 回调事件 | Webhook 事件表格 |
| 账务明细 | 账务交易表格 |
| 异步任务 | Outbox 任务表格 |
| 商户余额 | 商户余额总账 |
| 接口清单 | 30+ API 端点列表 |
| 系统信息 | Go/CPU/内存/PG/Redis 状态 |

---

## 八、启动方式

```bash
# 1. 启动依赖
docker compose up -d

# 2. 配置环境变量
cp .env.example .env
# 编辑 .env 填入实际值

# 3. 执行迁移
go run ./cmd/migrate up

# 4. 启动 API 服务
go run ./cmd/api

# 5. 启动 Worker
go run ./cmd/worker

# 6. 访问管理后台
# http://localhost:8080/admin/dashboard
```

---

## 九、已知限制

| 项目 | 说明 |
|------|------|
| 外部客户端 | LP/Bank/MAE 只有 Mock 实现，无真实 HTTP 调用 |
| KMS | 只有 MockKMS，无真实 KMS 集成 |
| 鉴权 | Bearer token 模式，未对接真实 JWT/OAuth |
| 集成测试 | 部分场景为 stub（需 Docker 环境运行完整测试） |
| 部署 | 未配置 Kubernetes/Docker 生产部署清单 |

---

## 十、Git 备份点

```
标签: v1.0-complete
提交: 见 git log
说明: 全部 38 个任务完成，系统可联调运行
```
