# TDD — NexusGlobal MAMR-System 技术设计文档

> 版本：V1.0（对应 PRD V1.5）
> 适用：Codex 类 AI Agent 直接照此落地编码
> 阅读顺序：本文档为所有后续文档（CONSTRAINTS / API / CODING_STANDARDS / TASKS）的技术基础，**必须最先读完**。
> 配套强约束闭环：每轮开发前强制读 `AGENT_WORKFLOW.md` + `MEMORY.md` + `CONSTRAINTS.md`。

---

## 0. 文档使用说明（给 AI）

1. 本文档把 PRD V1.5 的业务需求翻译成**可直接编码的技术方案**。所有目录、表结构、interface、流程伪代码都可逐字落地。
2. 当 PRD 与本 TDD 冲突时，以本 TDD 为准（TDD 已做工程取舍）；当本 TDD 与 CONSTRAINTS.md 冲突时，以 CONSTRAINTS.md 为准（资金红线最高）。
3. 本文档约定的技术栈、目录、命名、selector 是**锁定项**，不得擅自更换库或改目录结构。
4. 所有"外部依赖"（LP / Bank / MAE）在本地一律走 Mock 实现，**本地零外部网络依赖即可跑通全部测试**。

---

## 1. 技术栈锁定与版本约束

### 1.1 核心运行时

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Go | **1.22+**（go.mod 写 `go 1.22`） | 使用标准库 `log/slog`、`net/http` ServeMux 增强路由不强依赖 |
| PostgreSQL | **15** | 主数据源、资金真相、唯一约束、行级锁、`SKIP LOCKED` |
| Redis | **7** | 计数器/滑动窗口、Delay Queue、分布式锁（非资金边界）、**Redis Streams 作为 MQ** |

### 1.2 锁定依赖库（go.mod）

| 用途 | 库 | 版本约束 | 备注 |
| --- | --- | --- | --- |
| HTTP 路由 | `github.com/go-chi/chi/v5` | v5.0.x | 轻量、中间件友好 |
| PostgreSQL 驱动 | `github.com/jackc/pgx/v5` | v5.5+ | 使用 `pgxpool` 连接池 |
| SQL 代码生成 | `sqlc`（CLI，开发期工具） | v1.25+ | **显式 SQL，资金系统可审查**，不用 ORM 隐藏行为 |
| 数据库迁移 | `github.com/golang-migrate/migrate/v4` | v4.17+ | 版本化 migration，CLI + 库 |
| Redis 客户端 | `github.com/redis/go-redis/v9` | v9.5+ | Streams / Lua / Pipeline |
| 金额（账本） | `github.com/shopspring/decimal` | v1.4+ | `decimal(20,4)` 映射，**禁用 float** |
| UUID | `github.com/google/uuid` | v1.6+ | 主键 |
| 配置 | `github.com/caarlos0/env/v11` + `.env` | v11.x | 12-factor，环境变量注入 |
| 测试断言 | `github.com/stretchr/testify` | v1.9+ | require/assert/mock |
| 集成测试容器 | `github.com/testcontainers/testcontainers-go` | v0.27+ | 起真实 PG/Redis，可被 `-short` 跳过 |
| 限流 | `github.com/go-chi/httprate` | v0.9+ | Webhook/接口限流 |
| 结构化日志 | 标准库 `log/slog` | - | JSON handler |

> **选型理由（写给 AI，不要推翻）**：
> - 选 **sqlc 而非 GORM**：资金系统每一条 SQL 都要可审计、可读、可解释；ORM 的隐式 N+1、隐式事务、隐式类型转换在金额场景是事故源。sqlc 从 `.sql` 生成类型安全的 Go 代码，SQL 完全显式。
> - 选 **pgx 而非 database/sql + lib/pq**：pgx 原生支持 `SKIP LOCKED`、批量、`bytea`、`jsonb`、`numeric`，性能与类型映射更好。
> - 金额双轨：MAE 指标用 `int64` minor unit（与 Redis INCRBY 对齐）；账本余额用 `decimal(20,4)`（PG `numeric` ↔ `shopspring/decimal`）。**两者都绝不用 float**。

### 1.3 版本约束规则

- `go.mod` 锁定 `go 1.22`，CI 用 `golang:1.22` 镜像。
- 所有依赖在 `go.mod` 固定 minor 版本，`go.sum` 提交。
- 不引入未在上表列出的第三方库；确需新增时，先在 `MEMORY.md` 的"未决问题"登记并标注理由。

---

## 2. 模块化单体目录结构

> AI 照此**逐目录创建**。模块化单体 = 单一可部署二进制，内部按模块强边界隔离；模块间只能通过 `service` 接口调用，不得跨模块直接访问对方 `repository`。

```
nexusglobal/
├── go.mod
├── go.sum
├── Makefile                      # 一键 build/test/lint/migrate/run（见 §9）
├── docker-compose.yml            # postgres + redis（本地一键起依赖）
├── .env.example                  # 配置模板，复制为 .env
├── sqlc.yaml                     # sqlc 配置
├── README.md
│
├── cmd/
│   ├── api/
│   │   └── main.go               # HTTP API 主进程（handler 装配）
│   ├── worker/
│   │   └── main.go               # Outbox/Streams 消费 Worker 主进程
│   └── migrate/
│       └── main.go               # 迁移执行器（封装 golang-migrate）
│
├── migrations/                   # golang-migrate 版本化 SQL
│   ├── 000001_init_mae.up.sql
│   ├── 000001_init_mae.down.sql
│   ├── 000002_init_webhook.up.sql
│   ├── 000002_init_webhook.down.sql
│   ├── 000003_init_treasury.up.sql
│   ├── 000003_init_treasury.down.sql
│   ├── 000004_init_ledger.up.sql
│   ├── 000004_init_ledger.down.sql
│   ├── 000005_init_payout.up.sql
│   ├── 000005_init_payout.down.sql
│   └── 000006_init_outbox.up.sql
│
├── internal/
│   ├── platform/                 # 跨模块基础设施（不含业务）
│   │   ├── config/               # env 加载、Config struct
│   │   ├── postgres/             # pgxpool 初始化、TxManager（事务边界）
│   │   ├── redis/                # go-redis 客户端、Lua 脚本封装
│   │   ├── stream/               # Redis Streams MQ 封装（生产/消费组/ack/DLQ）
│   │   ├── crypto/               # AES-256-GCM Envelope Encryption（含 Mock KMS）
│   │   ├── idempotency/          # 幂等键计算与存储读写
│   │   ├── money/                # minor-unit int64 + decimal 辅助（禁 float）
│   │   ├── clock/                # Clock 接口（可注入，测试可控时间）
│   │   ├── logger/               # slog 初始化
│   │   ├── apperr/               # 统一错误类型 + 错误码
│   │   └── httpx/                # 中间件：鉴权、幂等、限流、recover、requestID
│   │
│   ├── mae/                      # 模块一：MAE 池化与动态路由
│   │   ├── domain/               # 实体、状态机、纯业务规则（无 I/O）
│   │   ├── repository/           # sqlc 生成代码 + 仓储封装
│   │   ├── service/              # 用例编排（事务边界在此）
│   │   ├── handler/              # HTTP handler
│   │   ├── metric/               # Redis Counter / 滑动窗口聚合
│   │   └── queries.sql           # sqlc 输入 SQL
│   │
│   ├── webhook/                  # 模块二：Webhook Receiver 解耦代理
│   │   ├── domain/
│   │   ├── repository/
│   │   ├── service/
│   │   ├── handler/              # /webhook/{mae,bank,lp}/{provider_id}
│   │   ├── consumer/             # 核心消费者（从 Streams 消费入账）
│   │   └── queries.sql
│   │
│   ├── treasury/                 # 模块三：银行清算与阶梯式 Sweep
│   │   ├── domain/               # banking calendar / cut-off / sweep 规则机
│   │   ├── repository/
│   │   ├── service/
│   │   ├── handler/
│   │   ├── scheduler/            # Tiered Scheduled Sweep + Redis Delay Queue
│   │   └── queries.sql
│   │
│   ├── ledger/                   # 模块四(账本部分)：双轨总账
│   │   ├── domain/               # 复式记账规则、借贷平衡校验
│   │   ├── repository/
│   │   ├── service/              # 入账事务（幂等、唯一约束）
│   │   ├── handler/
│   │   └── queries.sql
│   │
│   ├── payout/                   # 模块四(代付部分)：冻结 + Outbox 生产
│   │   ├── domain/               # payout 状态机、地址清洗校验
│   │   ├── repository/
│   │   ├── service/              # 本地冻结事务 + 写 Outbox
│   │   ├── handler/
│   │   └── queries.sql
│   │
│   └── worker/                   # 模块五：异步 Worker（Outbox 消费 + LP 调用）
│       ├── outbox/               # Outbox 轮询/抢占（SKIP LOCKED）
│       ├── fxlock/               # FX Lock Worker
│       ├── payout/               # LP Payout Worker
│       └── reconcile/            # 对账 Worker
│
├── pkg/                          # 可被外部/多模块复用的纯库（无业务状态）
│   ├── external/                 # 外部依赖 interface + Mock（LP/Bank/MAE）
│   │   ├── lp/                   # LPClient interface + MockLP
│   │   ├── bank/                 # BankClient interface + MockBank
│   │   └── mae/                  # MAEClient interface + MockMAE
│   └── validator/                # 链上地址格式校验（TRON/ETH/BTC/SOL）
│
└── test/
    ├── integration/              # 跨模块集成测试（testcontainers）
    └── fixtures/                 # 测试数据
```

### 2.1 模块边界规则（强制）

1. `internal/<module>/handler` → 只调用本模块 `service`。
2. `internal/<module>/service` → 调用本模块 `repository` + `platform/*` + `pkg/external`（接口）。
3. **跨模块调用只能 service → 另一模块 service 接口**，禁止 `mae/service` 直接 import `ledger/repository`。
4. `pkg/external` 只暴露 interface 与 Mock；真实实现（如真连 LP）后续单独包，**MVP 阶段只交付 Mock**。
5. `domain` 包**不得有任何 I/O**（无 DB、无 HTTP、无 Redis），只有纯函数与状态机，便于单测。

---

## 3. 六大模块分层设计

每个业务模块统一四层：`handler`（协议） → `service`（用例/事务） → `repository`（持久化） → `domain`（纯规则）。

### 3.1 通用分层职责

| 层 | 职责 | 禁止 |
| --- | --- | --- |
| handler | 解析请求、鉴权、幂等键提取、调用 service、序列化响应、错误码映射 | 写业务规则、直接访问 DB |
| service | 用例编排、**开启/提交事务**、调用 repository、发 Outbox、调用 domain 校验 | 在事务内做外部网络 I/O（红线） |
| repository | sqlc 生成的查询 + 事务内执行、唯一约束处理 | 业务判断、跨模块查询 |
| domain | 状态机迁移、金额规则、借贷平衡、地址校验等纯逻辑 | 任何 I/O |

### 3.2 模块清单与核心职责

| 模块 | 包 | 核心职责 | 关键表 |
| --- | --- | --- | --- |
| 模块一 MAE | `mae` | 池化配置、状态机、熔断、Testing 灰度、Redis 指标聚合 | `mae_configuration`、`mae_runtime_metric_snapshot` |
| 模块二 Webhook | `webhook` | 验签、幂等、Raw Payload 加密、MQ 投递、DLQ、核心消费者入账 | `webhook_receiver_proxy`、`webhook_ingress_event` |
| 模块三 Treasury/Sweep | `treasury` | 银行日历、Cut-off、Tiered Sweep、同名/三方校验、Delay Queue | `banking_calendar`、`sweep_rule`、`sweep_transfer` |
| 模块四a Ledger | `ledger` | 双轨复式记账、借贷平衡、幂等入账 | `ledger_account`、`ledger_transaction`、`ledger_entry` |
| 模块四b Payout | `payout` | 余额快照、本地冻结事务、地址金库、Outbox 生产、状态机 | `merchant_treasury_ledger`、`merchant_payout_address`、`payout_request`、`transactional_outbox` |
| 模块五 Worker | `worker` | Outbox 抢占、FX Lock、LP Payout、UnknownExternalState 处理、对账 | 复用 `transactional_outbox`、`payout_request` |

> **依赖注入方式（锁定）**：构造函数注入。每个 service 暴露 `New(repo Repository, ext lp.Client, clk clock.Clock, log *slog.Logger) *Service`。`main.go` 在装配点显式 new 所有依赖。**不引入 DI 框架**（wire/fx），手工装配，可读可审查。

---

## 4. 数据表 → Go 映射、sqlc 选型、Migration

### 4.1 类型映射总则

| PG 类型 | Go 类型 | 备注 |
| --- | --- | --- |
| `uuid` | `uuid.UUID` | pgx 原生支持 |
| `varchar/text` | `string` | - |
| `bigint`（minor unit / 计数） | `int64` | **金额 minor unit、计数器** |
| `integer` | `int32` | 权重、bps、version |
| `numeric(20,4)` | `decimal.Decimal` | **账本金额**，pgx 注册 decimal 编解码 |
| `boolean` | `bool` | - |
| `jsonb` | `[]byte` / 具体 struct（`json.Marshal`） | tenant_scope、cut_off_matrix 等 |
| `bytea` | `[]byte` | 密文字段 |
| `timestamptz` | `time.Time` | UTC 存储 |
| `date` | `time.Time`（截断到日） | business_date |

### 4.2 sqlc 配置（sqlc.yaml）

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "migrations"
    queries: "internal"            # 各模块 queries.sql
    gen:
      go:
        package: "db"
        out: "internal/platform/postgres/db"   # 或各模块 repository/db
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_interface: true                    # 便于 mock
        emit_pointers_for_null_types: true
        overrides:
          - db_type: "uuid"
            go_type: "github.com/google/uuid.UUID"
          - db_type: "numeric"
            go_type: "github.com/shopspring/decimal.Decimal"
```

> 若 sqlc 对 `numeric` 处理不便，允许 repository 层用手写 pgx 查询包裹，但**对外仍暴露 decimal.Decimal**，不得退化为 float64。

### 4.3 关键表 Go struct 映射（示例，AI 照此扩展全部表）

```go
// internal/mae/domain/mae.go
type MAEStatus string

const (
    MAEStatusDraft     MAEStatus = "Draft"
    MAEStatusActive    MAEStatus = "Active"
    MAEStatusCooldown  MAEStatus = "Cooldown"
    MAEStatusTesting   MAEStatus = "Testing"
    MAEStatusSuspended MAEStatus = "Suspended"
    MAEStatusDisabled  MAEStatus = "Disabled"
)

type MAEConfiguration struct {
    ID                       uuid.UUID  `json:"id"`
    MAEID                    string     `json:"mae_id"`
    DisplayName              string     `json:"display_name"`
    MAEType                  string     `json:"mae_type"`
    TenantScope              []byte     `json:"tenant_scope"`      // jsonb
    Jurisdiction             string     `json:"jurisdiction"`
    SupportedCurrencies      []byte     `json:"supported_currencies"`
    APIBaseURL               string     `json:"api_base_url"`
    APIKeyCiphertext         []byte     `json:"-"`                  // 密文，禁止序列化
    WebhookSecretCiphertext  []byte     `json:"-"`
    WebhookProxyURL          string     `json:"webhook_proxy_url"`
    RiskLabel                string     `json:"risk_label"`
    SettlementBankAccountID  uuid.UUID  `json:"settlement_bank_account_id"`
    DailyVmaxMinor           int64      `json:"daily_vmax_minor"`
    MonthlyVmaxMinor         int64      `json:"monthly_vmax_minor"`
    CmaxBps                  int32      `json:"cmax_bps"`
    CardTestingLimit         int32      `json:"card_testing_limit"`
    RoutingWeight            int32      `json:"routing_weight"`
    CooldownMinutes          int32      `json:"cooldown_minutes"`
    TestingTrafficBps        int32      `json:"testing_traffic_bps"`
    TestingMinTxnCount       int32      `json:"testing_min_txn_count"`
    TestingMinSuccessRateBps int32      `json:"testing_min_success_rate_bps"`
    TestingMaxP95LatencyMs   int32      `json:"testing_max_p95_latency_ms"`
    Status                   MAEStatus  `json:"status"`
    StatusReason             string     `json:"status_reason"`
    BusinessDayLockUntil     *time.Time `json:"business_day_lock_until"`
    Version                  int32      `json:"version"`           // 乐观锁
    CreatedAt                time.Time  `json:"created_at"`
    UpdatedAt                time.Time  `json:"updated_at"`
}
```

```go
// internal/ledger/domain/ledger.go — 注意金额一律 decimal
type MerchantTreasuryLedger struct {
    ID                     uuid.UUID       `json:"id"`
    MerchantID             uuid.UUID       `json:"merchant_id"`
    Currency               string          `json:"currency"`
    PendingBalance         decimal.Decimal `json:"pending_balance"`
    SettledBalance         decimal.Decimal `json:"settled_balance"`
    ReserveBalance         decimal.Decimal `json:"reserve_balance"`
    FrozenPayoutBalance    decimal.Decimal `json:"frozen_payout_balance"`
    DisputeHoldBalance     decimal.Decimal `json:"dispute_hold_balance"`
    AvailablePayoutBalance decimal.Decimal `json:"available_payout_balance"`
    Version                int32           `json:"version"`
    UpdatedAt              time.Time       `json:"updated_at"`
}

// available = settled - reserve - frozen - dispute
func (l MerchantTreasuryLedger) RecalcAvailable() decimal.Decimal {
    return l.SettledBalance.
        Sub(l.ReserveBalance).
        Sub(l.FrozenPayoutBalance).
        Sub(l.DisputeHoldBalance)
}
```

### 4.4 Migration 编写规则

1. 每个模块一个 migration 文件对（`up`/`down`），编号见 §2 目录。
2. **所有幂等键字段必须建 UNIQUE 索引**（`idempotency_key`、`provider_event_id` 组合等）。
3. 所有金额字段：minor 用 `bigint`，账本用 `numeric(20,4)`，**禁止 `double precision/real`**。
4. 乐观锁字段 `version integer NOT NULL DEFAULT 0`。
5. `created_at/updated_at timestamptz NOT NULL DEFAULT now()`。

### 4.5 关键 Migration SQL（节选，AI 照此补全六大模块）

```sql
-- 000001_init_mae.up.sql
CREATE TABLE mae_configuration (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mae_id                      varchar(64)  NOT NULL UNIQUE,
    display_name                varchar(128) NOT NULL,
    mae_type                    varchar(32)  NOT NULL,
    tenant_scope                jsonb        NOT NULL DEFAULT '[]',
    jurisdiction                varchar(8)   NOT NULL,
    supported_currencies        jsonb        NOT NULL DEFAULT '[]',
    api_base_url                text         NOT NULL,
    api_key_ciphertext          bytea        NOT NULL,
    webhook_secret_ciphertext   bytea        NOT NULL,
    webhook_proxy_url           varchar(512) NOT NULL DEFAULT '',
    risk_label                  varchar(128) NOT NULL DEFAULT '',
    settlement_bank_account_id  uuid         NOT NULL,
    daily_vmax_minor            bigint       NOT NULL CHECK (daily_vmax_minor >= 0),
    monthly_vmax_minor          bigint       NOT NULL CHECK (monthly_vmax_minor >= 0),
    cmax_bps                    integer      NOT NULL DEFAULT 0,
    card_testing_limit          integer      NOT NULL DEFAULT 0,
    routing_weight              integer      NOT NULL DEFAULT 0,
    cooldown_minutes            integer      NOT NULL DEFAULT 30,
    testing_traffic_bps         integer      NOT NULL DEFAULT 500,
    testing_min_txn_count       integer      NOT NULL DEFAULT 0,
    testing_min_success_rate_bps integer     NOT NULL DEFAULT 0,
    testing_max_p95_latency_ms  integer      NOT NULL DEFAULT 0,
    status                      varchar(32)  NOT NULL DEFAULT 'Draft',
    status_reason               varchar(128) NOT NULL DEFAULT '',
    business_day_lock_until     date,
    version                     integer      NOT NULL DEFAULT 0,
    created_at                  timestamptz  NOT NULL DEFAULT now(),
    updated_at                  timestamptz  NOT NULL DEFAULT now()
);
CREATE INDEX idx_mae_status ON mae_configuration(status);

-- 000005_init_payout.up.sql （节选 merchant_treasury_ledger）
CREATE TABLE merchant_treasury_ledger (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id               uuid           NOT NULL,
    currency                  varchar(16)    NOT NULL,
    pending_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    settled_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    reserve_balance           numeric(20,4)  NOT NULL DEFAULT 0,
    frozen_payout_balance     numeric(20,4)  NOT NULL DEFAULT 0,
    dispute_hold_balance      numeric(20,4)  NOT NULL DEFAULT 0,
    available_payout_balance  numeric(20,4)  NOT NULL DEFAULT 0,
    version                   integer        NOT NULL DEFAULT 0,
    updated_at                timestamptz    NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, currency),
    CHECK (settled_balance >= 0),
    CHECK (frozen_payout_balance >= 0)
);

-- 000006_init_outbox.up.sql
CREATE TABLE transactional_outbox (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type  varchar(64)  NOT NULL,
    aggregate_id    uuid         NOT NULL,
    event_type      varchar(64)  NOT NULL,
    payload         jsonb        NOT NULL,
    idempotency_key varchar(128) NOT NULL UNIQUE,
    status          varchar(32)  NOT NULL DEFAULT 'Pending',
    retry_count     integer      NOT NULL DEFAULT 0,
    next_retry_at   timestamptz  NOT NULL DEFAULT now(),
    locked_by       varchar(128),
    locked_at       timestamptz,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now()
);
-- Worker 抢占用：按 status + next_retry_at 拉取
CREATE INDEX idx_outbox_dispatch ON transactional_outbox(status, next_retry_at)
    WHERE status IN ('Pending', 'Failed');
```

---

## 5. 三大核心流程：时序 + 伪代码

### 5.1 MAE 状态机引擎

**状态集**：`Draft / Active / Cooldown / Testing / Suspended / Disabled`

**合法迁移表**（在 `mae/domain` 实现为 `map[MAEStatus][]MAEStatus`，非法迁移直接返回 `apperr.ErrIllegalTransition`）：

| From | 允许 To | 触发 |
| --- | --- | --- |
| Draft | Active | 凭证验证+银行绑定+合规启用 |
| Active | Cooldown | Exposure≥vmax*0.8 / 异常率超 cmax / 测卡 / API超时 / 验签失败升高 |
| Active | Suspended | 严重故障 / 合规冻结 / 人工熔断 |
| Cooldown | Testing | cooldown 到期 + 健康检查 + 当日 Exposure 锁已过 |
| Testing | Active | 灰度达标 |
| Testing | Cooldown | 轻度灰度失败 |
| Testing | Suspended | 连续灰度失败≥3 / 风险升高 |
| Suspended | Testing | 人工复核通过 |
| Suspended | Disabled | 永久停用 |

**Exposure 只增不减 + 当日锁定（核心伪代码）**：

```text
// 指标累加（Redis，整数 minor unit）
func OnCaptured(maeID, businessDate, capturedMinor):
    INCRBY MAE_METRIC:{maeID}:{businessDate}:gross_exposure_amount  capturedMinor
    INCRBY MAE_METRIC:{maeID}:{businessDate}:captured_amount        capturedMinor
    // gross_exposure 永不因 refund/chargeback 调用 DECRBY —— 见 CONSTRAINTS 红线

func OnRefund(maeID, businessDate, refundMinor):
    INCRBY MAE_METRIC:{maeID}:{businessDate}:refund_amount refundMinor
    // 绝不触碰 gross_exposure_amount

// 熔断评估（L1，目标 ≤100ms；读 Redis，不读 DB）
func EvaluateCircuit(mae):
    exposure = GET gross_exposure_amount
    if exposure >= mae.DailyVmaxMinor * 0.8:
        transition(mae, Active->Cooldown, reason="EXPOSURE_THRESHOLD")
        mae.BusinessDayLockUntil = endOfBusinessDay(now)   // 当日锁死
    if abnormalRateBps > mae.CmaxBps: transition(->Cooldown,"ABNORMAL_RATE")
    if cardTestingCount > mae.CardTestingLimit: transition(->Cooldown,"CARD_TESTING")
    ...
    // 所有 transition 走 DB 乐观锁 version + 写审计日志

// Cooldown -> Testing 半开恢复
func TryRecoverToTesting(mae):
    require cooldownExpired(mae)
    require healthCheckPassed(mae)
    require now.businessDate > mae.BusinessDayLockUntil   // 当日锁未过则禁止
    transition(mae, Cooldown->Testing)

// Testing -> Active
func EvaluateTesting(mae, stats):
    if stats.txnCount >= mae.TestingMinTxnCount
       && stats.successRateBps >= mae.TestingMinSuccessRateBps
       && stats.abnormalRateBps < mae.CmaxBps/2
       && stats.p95LatencyMs <= mae.TestingMaxP95LatencyMs
       && stats.cardTestingCount == 0:
        transition(mae, Testing->Active)
    else:
        mae.testingFailCount++
        if mae.testingFailCount >= 3: transition(->Suspended)
        else: transition(->Cooldown)
```

**状态迁移持久化（乐观锁，必须）**：

```sql
-- queries.sql: UpdateMAEStatus
UPDATE mae_configuration
SET status = $2, status_reason = $3, business_day_lock_until = $4,
    version = version + 1, updated_at = now()
WHERE id = $1 AND version = $5;     -- version 不匹配 => 0 行 => 重试或报冲突
```

### 5.2 Tiered Scheduled Sweep 引擎

**时序**：

```
[trigger 来源]  CashPosition 累加 / 定时器 tick / 管理员触发
      │
      ▼
1) 加载 sweep_rule + banking_calendar
2) 判断 now 是否在 banking window 内（工作日 ∧ 未到 cut-off ∧ 非节假日）
      ├─ 否 ─► status=Accumulating/Delayed；push Redis Delay Queue；不调用银行 API
      └─ 是 ─► 继续
3) 校验：accumulated ≥ trigger_threshold
        ∧ daily_sweep_count < max_daily_sweep_count
        ∧ beneficiary 规则通过（见下）
        ∧ approval 规则通过
      └─ 任一不过 ─► Accumulating / ApprovalRequired
4) 创建 sweep_transfer (status=Locked)，DB 行级锁 + idempotency_key 唯一
5) 调用 Bank API 提交（注意：提交动作在 service 内，但若走 Saga，则通过 Outbox/Worker）
6) 结果：
      Submitted ─► 等待银行 Webhook ─► Confirmed ─► Reconciled
      timeout   ─► UnknownExternalState（禁止盲重试，查询/等 Webhook/人工对账）
```

**受益人规则（domain 纯函数）**：

```text
beneficiary_type == UNKNOWN                          => reject（禁止执行）
SAME_NAME && verified                                => 可自动执行
SAME_NAME && !verified                               => ApprovalRequired
THIRD_PARTY && amount >= approval_threshold          => ApprovalRequired（四眼）
THIRD_PARTY_CROSS_BORDER                             => 合规复核 + 四眼复核
```

**Redis Delay Queue（ZSET，score = next_execution_at unix）**：

```text
Key:   SWEEP_DELAY:{rail}:{source_bank_account_id}:{currency}
ZADD   key  <next_execution_unix>  <sweep_rule_id>
// 调度器轮询：ZRANGEBYSCORE key -inf now  => 到期任务出队评估
```

**银行 API 超时（铁律）**：`timeout != Failed` → `UnknownExternalState`，禁止盲目重复提交。

### 5.3 Payout 冻结 + Outbox（最关键资金流程）

**五阶段时序（PRD 第 7 章逐字落地）**：

```
Phase1 本地冻结事务 (api/payout service) ── 同步、无任何外部 I/O
   BEGIN
     SELECT ... FROM merchant_treasury_ledger WHERE merchant_id,currency FOR UPDATE
     校验 settled - reserve - frozen - dispute >= amount      // 不足 => 回滚报错
     settled_balance      -= amount
     frozen_payout_balance += amount
     available = recalc
     INSERT ledger_transaction(PAYOUT_FREEZE)
     INSERT ledger_entry x2  (Dr Settled / Cr FrozenPayout)   // 借贷必相等
     INSERT payout_request(status=BalanceFrozen)
     INSERT transactional_outbox(event=FX_LOCK_REQUESTED, status=Pending)
   COMMIT                                                     // 提交后才允许外呼
        │
        ▼ (Redis Streams / Outbox 轮询唤醒 worker)
Phase2 FX Lock Worker
   抢占 outbox(FX_LOCK_REQUESTED)  via UPDATE...SKIP LOCKED
   call LP.FXLock()                                          // 外部 I/O 在事务外
     成功 => payout_request.status=FxLocked; outbox(LP_PAYOUT_REQUESTED)
     明确失败 => 触发 Phase5 释放
     超时 => UnknownExternalState（frozen 不动）
        │
        ▼
Phase3 LP Payout Worker
   抢占 outbox(LP_PAYOUT_REQUESTED); 校验 status==FxLocked
   call LP.Payout()
     已提交 => status=LPProcessing
     明确失败 => status=Failed => Phase5
     超时 => UnknownExternalState（frozen 不释放）
        │
        ▼ (LP Webhook 经 Receiver -> Streams -> consumer)
Phase4 Confirmed 核销 Frozen（第二个本地事务）
   BEGIN
     幂等校验 LP provider_event_id
     SELECT payout_request FOR UPDATE; 校验 status ∈ {LPProcessing, UnknownExternalState}
     INSERT ledger_transaction(PAYOUT_CONFIRM)
     INSERT ledger_entry x2 (Dr FrozenPayout / Cr ExternalPayoutSettlement)
     frozen_payout_balance -= amount; available = recalc
     payout_request.status = Confirmed
   COMMIT

Phase5 Failed 释放 Frozen（第二个本地事务）
   BEGIN
     幂等校验失败事件
     SELECT payout_request FOR UPDATE; 校验状态允许释放
     INSERT ledger_transaction(PAYOUT_RELEASE)
     INSERT ledger_entry x2 (Dr FrozenPayout / Cr Settled)
     frozen_payout_balance -= amount; settled_balance += amount; available = recalc
     payout_request.status = BalanceReleased
   COMMIT
```

**并发双花防护要点**：

- 余额行 `SELECT ... FOR UPDATE` 是唯一资金边界；Redis 锁仅辅助、非边界。
- 并发 5 笔相同余额请求：只有落在余额覆盖范围内的请求能通过校验，其余在校验步回滚。
- `payout_request.idempotency_key` UNIQUE，重复提交不产生第二笔冻结。

**Outbox 抢占 SQL**：

```sql
-- queries.sql: ClaimOutboxBatch
UPDATE transactional_outbox
SET status = 'Processing', locked_by = $1, locked_at = now(), updated_at = now()
WHERE id IN (
    SELECT id FROM transactional_outbox
    WHERE status IN ('Pending','Failed') AND next_retry_at <= now()
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
RETURNING *;
```

---

## 6. Redis Streams 作为 MQ（消费组 / ACK / DLQ）

> 系统拓扑要求"外部回调 → Webhook Receiver → MQ → 核心消费者"。本系统用 **Redis Streams** 实现 MQ，**不引入 Kafka/RabbitMQ**（保持本地零额外依赖）。封装在 `internal/platform/stream`。

### 6.1 Topic / Stream 命名

| 用途 | Stream Key | 消费组 |
| --- | --- | --- |
| MAE 回调事件 | `stream:webhook:mae` | `cg-core-mae` |
| 银行回调事件 | `stream:webhook:bank` | `cg-core-bank` |
| LP 回调事件 | `stream:webhook:lp` | `cg-core-lp` |
| DLQ（统一） | `stream:dlq` | `cg-dlq-admin` |

### 6.2 生产（Webhook Receiver 验签幂等后投递）

```go
// internal/platform/stream/producer.go
func (p *Producer) Publish(ctx context.Context, stream string, evt Event) (string, error) {
    return p.rdb.XAdd(ctx, &redis.XAddArgs{
        Stream: stream,
        Values: map[string]any{
            "idempotency_key": evt.IdempotencyKey,
            "provider_type":   evt.ProviderType,
            "provider_id":     evt.ProviderID,
            "event_type":      evt.EventType,
            "payload_hash":    evt.PayloadHash,
            "ingress_id":      evt.IngressID.String(), // 指向 webhook_ingress_event
        },
    }).Result()
}
```

### 6.3 消费组（核心消费者，至少一次语义 + 幂等）

```go
// 初始化（幂等创建消费组）
rdb.XGroupCreateMkStream(ctx, stream, group, "0")

// 消费循环
for {
    res, _ := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
        Group: group, Consumer: consumerID,
        Streams: []string{stream, ">"}, Count: 16, Block: 5 * time.Second,
    }).Result()

    for _, msg := range res[0].Messages {
        err := handler.Handle(ctx, msg)         // 内部：幂等检查 -> Ledger 事务入账 -> Outbox
        if err == nil {
            rdb.XAck(ctx, stream, group, msg.ID) // 成功才 ACK
        } else if isRetryable(err) {
            // 不 ACK，留在 PEL，下次 XAutoClaim 重投
        } else {
            toDLQ(ctx, msg, err)                 // 不可重试 -> DLQ
            rdb.XAck(ctx, stream, group, msg.ID)
        }
    }
}
```

### 6.4 重投与 DLQ

- **超时未 ACK 重投**：定期 `XAutoClaim`（`MinIdle` 如 30s）认领 PEL 中卡住的消息，重投。
- **重试上限**：消息处理失败累计达 `maxDeliveries`（如 5，从 PEL 投递次数判断）→ 进 `stream:dlq` 并 ACK 原消息。
- **DLQ 内容**：原始 stream、原始 ID、错误原因、首次/末次处理时间。后台 `/admin/webhook/events/{id}/replay` 支持人工重放（重新 `XAdd` 回原 stream）。
- **消费者幂等**：每条消息处理第一步查 `webhook_ingress_event.idempotency_key` / `ledger_transaction.idempotency_key`，已处理直接 ACK 跳过。

### 6.5 关键约束

- 核心消费者**只从 Streams 消费**，绝不允许 Webhook handler 直连 Ledger / DB 写账。
- `XAck` 必须在业务事务 **COMMIT 成功之后**，避免"先 ACK 后崩溃"丢事件。
- 至少一次语义 → 入账幂等由 DB 唯一约束兜底。

---

## 7. 外部依赖 interface 抽象 + Mock 策略

> LP / Bank / MAE 是仅有的三类外部网络依赖。全部抽象为接口放 `pkg/external/*`，**MVP 只交付 Mock 实现**，本地与 CI 全程不触网。

### 7.1 接口定义

```go
// pkg/external/lp/client.go
package lp

type Client interface {
    FXLock(ctx context.Context, req FXLockRequest) (FXLockResult, error)
    Payout(ctx context.Context, req PayoutRequest) (PayoutResult, error)
    QueryStatus(ctx context.Context, providerRef string) (StatusResult, error) // UnknownExternalState 查询用
}

// 错误语义（worker 据此分流）
var (
    ErrExplicitFailure = errors.New("lp: explicit failure")  // -> 释放 Frozen
    ErrTimeout         = errors.New("lp: timeout")           // -> UnknownExternalState，不释放
)
```

```go
// pkg/external/bank/client.go
type Client interface {
    SubmitTransfer(ctx context.Context, req TransferRequest) (TransferResult, error)
    QueryTransfer(ctx context.Context, bankRef string) (TransferStatus, error)
}

// pkg/external/mae/client.go
type Client interface {
    HealthCheck(ctx context.Context, maeID string) (Health, error)
    // 路由侧如需主动探活 / 提交支付
}
```

### 7.2 Mock 实现策略

```go
// pkg/external/lp/mock.go
type MockLP struct {
    // 可配置行为：成功 / 明确失败 / 超时 / 延迟
    Behavior map[string]Behavior // key: scenario
}

func NewMockLP() *MockLP { ... }

func (m *MockLP) FXLock(ctx context.Context, req FXLockRequest) (FXLockResult, error) {
    switch m.scenarioFor(req) {
    case ScenarioTimeout:  return FXLockResult{}, ErrTimeout
    case ScenarioFailure:  return FXLockResult{}, ErrExplicitFailure
    default:               return FXLockResult{LockID: deterministicID(req), Rate: ...}, nil
    }
}
```

**Mock 设计要点**：

1. **确定性**：相同输入 → 相同输出（用输入 hash 生成 LockID/TxHash），便于断言。
2. **可注入场景**：通过请求中的特殊字段或预置表，触发 success / explicit_failure / timeout / delay，覆盖 UnknownExternalState 分支。
3. **可回调**：MockLP/MockBank 可在 Payout/Sweep 提交后，向 Webhook Receiver 发送模拟回调（用于端到端测试 Confirmed/Failed 闭环）。本地通过直接调用 Receiver handler 或往 Streams 投递模拟实现。
4. 装配开关：`.env` 中 `EXTERNAL_MODE=mock|real`，MVP 恒为 `mock`；`main.go` 据此 new Mock 或真实 client。

---

## 8. 加密、幂等、金额辅助（platform 公共能力）

### 8.1 Envelope Encryption（`platform/crypto`）

```text
Master Key: KMS/HSM（MVP 用 MockKMS：本地固定主密钥，接口与真实 KMS 一致）
DEK:        派生自 tenant_id + data_type
Algorithm:  AES-256-GCM
IV:         96-bit 随机 nonce（每次加密新生成）
AAD:        tenant_id + record_id + field_name + key_version
Auth Tag:   128-bit
```

接口：`Encrypt(ctx, plaintext, meta) ([]byte, error)` / `Decrypt(ctx, ciphertext, meta) ([]byte, error)`。所有 PRD §10.2 列出的敏感字段（API Key、Webhook Secret、银行账号、IBAN、链上地址、LP 凭证）入库前必须加密。

### 8.2 幂等（`platform/idempotency`）

- 写接口统一中间件读取 `Idempotency-Key` header。
- 计算 `payload_hash`；同 key 同 payload → 返回首次结果；同 key 不同 payload → `409 IDEMPOTENCY_CONFLICT`。
- 幂等记录入库，支付/账务/出金类保存 ≥365 天。
- Webhook 幂等键：`hash(provider_type+provider_id+provider_event_id+event_type)`；缺 event_id 时 `hash(provider_type+provider_id+payload_hash+received_business_date)`。

### 8.3 金额（`platform/money`）

- minor unit：`type Minor int64`，提供 `Add/Sub/Mul(bps)` 等，**无 float 参与**。
- 账本：`decimal.Decimal`，统一 4 位小数，`decimal.DivisionPrecision=8` 但落库前 round 到 4。
- 提供 `BpsOf(amount, bps)` 计算 `amount * bps / 10000`，全整数/decimal 运算。

---

## 9. 本地一键启动（docker-compose + Makefile）

### 9.1 docker-compose.yml

```yaml
services:
  postgres:
    image: postgres:15
    environment:
      POSTGRES_USER: nexus
      POSTGRES_PASSWORD: nexus
      POSTGRES_DB: nexus
    ports: ["5432:5432"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U nexus"]
      interval: 5s
      timeout: 3s
      retries: 10
  redis:
    image: redis:7
    ports: ["6379:6379"]
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 10
```

### 9.2 Makefile（核心目标，供 AGENT_WORKFLOW 的循环调用）

```makefile
.PHONY: up down migrate sqlc build test test-short lint run-api run-worker ci

up:            ## 起 PG + Redis
	docker compose up -d

down:
	docker compose down -v

migrate:       ## 执行数据库迁移
	go run ./cmd/migrate up

sqlc:          ## 生成 sqlc 代码
	sqlc generate

build:         ## 编译全部，失败即 fail
	go build ./...

test:          ## 全量测试（含集成，需 up）
	go test ./... -count=1 -race

test-short:    ## 仅单元测试（跳过 testcontainers）
	go test ./... -short -count=1

lint:
	gofmt -l . && go vet ./...

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker

ci:            ## AI 自检闭环用：构建+短测+lint 一把过
	$(MAKE) build && $(MAKE) lint && $(MAKE) test-short
```

### 9.3 一键启动顺序（写给 AI）

```text
1. cp .env.example .env
2. make up            # 起依赖
3. make migrate       # 建表
4. make sqlc          # 生成查询代码（改了 queries.sql 后重跑）
5. make build         # 编译
6. make test          # 全量测试（含集成）
   或 make test-short # 快速回路（开发循环默认用这个）
7. make run-api & make run-worker
```

---

## 10. 配置项（.env.example）

```dotenv
APP_ENV=local
HTTP_ADDR=:8080
POSTGRES_DSN=postgres://nexus:nexus@localhost:5432/nexus?sslmode=disable
REDIS_ADDR=localhost:6379
EXTERNAL_MODE=mock            # mock | real（MVP 恒 mock）
KMS_MODE=mock                 # mock | real
MASTER_KEY_BASE64=<32字节base64> # MockKMS 主密钥
OUTBOX_WORKER_CONCURRENCY=4
OUTBOX_CLAIM_BATCH=16
STREAM_MAX_DELIVERIES=5
SWEEP_TICK_INTERVAL=10s
LOG_LEVEL=info
```

---

## 11. 测试策略（与 TASKS DoD 对齐）

| 层 | 工具 | 要求 |
| --- | --- | --- |
| domain 纯逻辑 | testify，`-short` | 状态机全迁移、金额规则、地址校验：表驱动，覆盖率 ≥85% |
| service | testify + testcontainers（真 PG/Redis） | 事务边界、乐观锁、幂等、并发双花 |
| handler | httptest | 状态码、错误码、幂等头、鉴权 |
| 端到端 | test/integration | Payout 五阶段闭环、Webhook→Streams→入账、Sweep 窗口判断 |

**必备并发测试**：并发 5 笔相同余额 Payout，断言只有余额覆盖范围内成功、无超卖、无负余额（PRD §12.4）。

---

## 12. 安全与合规落点（编码内置）

1. DB 本地事务内**禁止任何外部网络 I/O**（LP/Bank/MAE/HTTP/gRPC）——见 CONSTRAINTS 红线 #10。
2. 外部调用一律经 Outbox/Worker/Saga。
3. 外部 API 超时 → UnknownExternalState，禁止盲重试。
4. Ledger 为资金真相，余额快照仅查询/并发控制；资金修正只能冲正分录。
5. 敏感字段全部 AES-256-GCM；密文字段 struct tag `json:"-"`，禁止出现在日志/响应。
6. Webhook Receiver 不持有 DB 写权限、不持有 LP 出金权限（部署/权限层面，代码侧 Receiver 包只 import producer，不 import ledger/payout repository）。

---

## 13. 给 AI 的落地检查点（构建前自查）

- [ ] 目录结构与 §2 完全一致。
- [ ] 所有金额：minor=`int64`，账本=`decimal.Decimal`，全局 grep 无 `float32/float64` 参与金额。
- [ ] 每个状态机非法迁移返回错误并有单测。
- [ ] 每个写接口有 `Idempotency-Key` 处理；每个幂等字段有 DB UNIQUE。
- [ ] Payout/Sweep 的外部调用全部在 service 事务**之外**、由 worker 执行。
- [ ] `grep` service 事务块内无 `http`/`lp.`/`bank.`/`rdb.Call外呼`。
- [ ] `make ci` 通过（build + lint + test-short）。

> 后续每个原子任务的精确范围、依赖与 DoD 见 `TASKS.md`；编码风格见 `CODING_STANDARDS.md`；不可逾越的红线见 `CONSTRAINTS.md`；接口契约见 `API.md`。
