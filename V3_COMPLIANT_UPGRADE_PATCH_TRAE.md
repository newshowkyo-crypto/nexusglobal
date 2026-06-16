# NexusGlobal V3.0 合规升级补丁规划

生成日期：2026-06-14  
用途：给 Trae 执行端使用的 V3.0 增量补丁说明。  
边界：本补丁只实现合规风控、准备金、费用摊销、管道隔离和真实资料包管理；禁止实现关闭风控、绕过 AML/卡组织规则、虚假单据或 silent mock。

## 0. 红线

- 不得实现 `BURN_MODE`、关闭风控、无视拒付率、无视 AML、全量放行高危订单。
- 不得生成虚假发票、虚假合同、虚假物流轨迹。
- 不得把 mock success 写入 real/production 分支。
- 不得删除或绕过 ChannelRegistry、UniversalAdapterRegistry、Anti-Carding、Dispute Ledger、Outbox。
- Override 只能加严、转人工、或临时调整到平台硬下限以上；必须写 audit log。
- 所有外部写动作继续走 Outbox，不得放进 DB transaction。

## 1. Migration 目标

新增 migration：`migrations/000025_v3_compliant_controls.up.sql` / `.down.sql`

核心变更：

```sql
CREATE TYPE channel_strategy_mode AS ENUM ('DEFAULT', 'STRICT', 'MANUAL_REVIEW');

ALTER TABLE adapter_channel
ADD COLUMN IF NOT EXISTS strategy_mode channel_strategy_mode NOT NULL DEFAULT 'DEFAULT',
ADD COLUMN IF NOT EXISTS chargeback_threshold_bps INT NOT NULL DEFAULT 100,
ADD COLUMN IF NOT EXISTS refund_threshold_bps INT NOT NULL DEFAULT 500,
ADD COLUMN IF NOT EXISTS failure_rate_threshold_bps INT NOT NULL DEFAULT 3000,
ADD COLUMN IF NOT EXISTS pipeline_group_id UUID NULL,
ADD COLUMN IF NOT EXISTS risk_override_enabled BOOLEAN NOT NULL DEFAULT FALSE,
ADD COLUMN IF NOT EXISTS risk_override_reason TEXT NULL,
ADD COLUMN IF NOT EXISTS risk_override_expires_at TIMESTAMPTZ NULL;
```

新增表：

- `admin_override_audit_log`
- `merchant_risk_profiles`
- `rolling_reserve_ledger`
- `settlement_fee_allocation`
- `pipeline_isolation_groups`
- `compliance_rfi_documents`

## 2. Task 1.1 通道风控模式

涉及文件：

- `internal/adapter/domain/domain.go`
- `internal/adapter/repository/repository.go`
- `internal/risk/carding/service.go`
- `internal/admin/handler.go`
- `internal/platform/httpx/dashboard.go`

模式：

- `DEFAULT`：标准阈值。
- `STRICT`：更严格阈值。
- `MANUAL_REVIEW`：高风险订单转人工，不自动放行。

测试：

- 超阈值阻断。
- STRICT 更严格。
- MANUAL_REVIEW 不自动放行。
- Override 无 reason/expires_at 失败。
- Override 写 `admin_override_audit_log`。
- 任何模式都不能绕过 hard block。

## 3. Task 1.2 滚动准备金

新增包：

- `internal/merchant/riskprofile/`
- `internal/worker/reserve_release/`

核心逻辑：

- capture 后按 `rolling_reserve_rate_bps` 拆分。
- 可结算部分入 merchant settled。
- 准备金入 rolling reserve/dispute reserve。
- 到期由 `ReserveReleaseWorker` 释放。
- RiskHold 时不得释放。

测试：

- 10000 minor，10% reserve -> 1000 reserve + 9000 settled。
- 未到期不释放。
- 到期释放。
- RiskHold 不释放。
- release 幂等。
- 账本平衡。

## 4. Task 1.3 费用与汇兑损益摊销

涉及文件：

- `internal/ledger/domain/domain.go`
- `internal/ledger/service/service.go`
- `internal/treasury/service/service.go`
- `internal/adapter/provider/exchange.go`
- `internal/adapter/provider/lp.go`
- `internal/worker/fxlock/fxlock.go`
- `internal/worker/payout/payout.go`

费用类型：

- `LP_FEE`
- `FX_SLIPPAGE`
- `GAS_FEE`
- `BANK_FEE`
- `NETWORK_FEE`

要求：

- fee 必须来自 provider response 或 reconcile 结果。
- provider 未返回 fee 时进入 review/pending。
- 不得硬编码固定 fee。
- 写 `settlement_fee_allocation`。
- ledger 必须平衡。

## 5. Task 1.4 管道隔离

新增包：

- `internal/pipeline/`

核心规则：

- acquirer channel、bank rail、exchange account、wallet policy 必须属于同一 pipeline group。
- 跨 group 转账、归集、代付、bridge 操作 fail-closed。
- RISK_HOLD pipeline 阻断新操作。
- 冻结 pipeline 必须写 audit log。

测试：

- 同组通过。
- 跨组 fail-closed。
- RiskHold 阻断。
- audit log 写入。

## 6. Task 1.5 RFI 真实资料包

新增包：

- `internal/compliance/rfi/`

API：

- `POST /admin/compliance/rfi-docs/generate`
- `GET /admin/compliance/rfi-docs`
- `GET /admin/compliance/rfi-docs/{id}`
- `POST /admin/compliance/rfi-docs/{id}/approve`
- `POST /admin/compliance/rfi-docs/{id}/reject`

要求：

- 只基于真实订单、真实商户资料、真实物流/履约数据。
- 缺源数据 -> `REVIEW_REQUIRED`。
- PDF metadata 必须含 `order_id`、`merchant_id`、`source_data_hash`。
- approve/reject 写 audit log。

测试：

- 订单不存在 -> 404。
- 缺发票/物流源数据 -> REVIEW_REQUIRED。
- 禁止随机轨迹。
- approve/reject 写 audit log。

## 7. Task 1.6 Dashboard

新增中文 tab：

- 通道风控
- 商户准备金
- 费用摊销
- 管道隔离
- 合规资料包

要求：

- 全中文。
- 所有危险操作 confirm。
- 所有 create/update/enable/disable/freeze 调真实 API。
- 不出现英文裸标签。
- 不出现“请到数据库查询”。
- 不出现 `BURN_MODE`、关闭风控、无视红线等文案。

## 8. 验收命令

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./internal/risk/... -count=1 -v
go test ./internal/ledger/... -count=1 -v
go test ./internal/treasury/... -count=1 -v
go test ./internal/pipeline/... -count=1 -v
go test ./internal/compliance/... -count=1 -v
go test ./internal/admin -count=1 -v
go test ./internal/platform/httpx -count=1 -v
go test ./... -short -count=1
```

新增包必须有测试，不能用 `[no test files]` 冒充完成。

## 9. 交付报告必须回答

- 是否存在 `BURN_MODE`：必须否。
- 是否存在关闭 Anti-Carding：必须否。
- 是否存在 real/prod mock fallback：必须否。
- Override 是否必须 reason/operator/expires_at：必须是。
- Override 是否写 audit log：必须是。
- RFI 是否只使用真实源数据：必须是。
- 跨 pipeline 是否 fail-closed：必须是。
- Ledger 是否全部平衡：必须是。

