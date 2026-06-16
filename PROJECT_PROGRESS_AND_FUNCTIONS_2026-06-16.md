# NexusGlobal 3.0 项目进度与功能总览

生成时间：2026-06-16  
当前分支：master  
当前基准 HEAD：1e44829  
阶段结论：mock/local 与 pre-production 框架层面达到 100% 可验收；真实服务商 sandbox 因缺少凭证仍为 SKIPPED_MISSING_CREDENTIAL。

## 一、当前阶段判定

NexusGlobal 3.0 已从早期 mock 清算系统推进到“多模态收单 + 多级发卡 + 双轨资金路由 + 可视化运营管理”的 pre-production 验收阶段。

当前可签收范围：

- 本地 mock 全链路可运行。
- Go 代码格式、构建、静态检查通过。
- 核心包单元测试与集成测试通过。
- Admin V3 动态配置 API 可用。
- Dashboard 已具备运营可视化管理入口。
- Production 配置门禁可拒绝 mock provider 与缺失安全配置。
- Sandbox 测试框架已具备，缺凭证时明确 skip。

当前不可夸大范围：

- Stripe、Nxtapay、VCC 真实 sandbox 没有凭证，不能声明真实联调通过。
- Vault/KMS、真实 LP/Bank/MAE、真实发卡 provider 尚需 staging 环境实配。
- 生产部署前仍需安全审计、性能压测、故障演练和真实回滚演练。

## 二、门禁结果

最近一次复核已通过：

```text
gofmt -l .
go build ./...
go vet ./...
go test ./internal/platform/config -count=1 -v
go test ./internal/platform/httpx -count=1 -v
go test ./internal/admin -run "TestV3(Channels|IssuingPools)" -count=1 -v
go test ./internal/acquirer/... -count=1
go test ./internal/issuing/... -count=1
go test ./internal/treasury/... -count=1
go test ./internal/worker/vcc_funding/... -count=1
go test ./test/integration -count=1 -v
go test ./... -count=1 -p 1
```

说明：Windows 并发执行 `go test ./...` 可能出现临时 test exe `Access is denied`，当前以串行 `-p 1` 全量测试作为本机签收依据。

## 三、功能模块总览

### 1. Checkout 收单入口

核心路径：

- `internal/checkout`
- `internal/acquirer`
- `internal/adapter`
- `test/integration/acquirer_sandbox_test.go`

已具备能力：

- mock/http/disabled 订单源边界。
- production 禁止 `CHECKOUT_ORDER_PROVIDER=mock`。
- 真实模式拒绝原始卡数据，要求 token/payment method。
- 多 provider 收单通过统一适配器抽象进入注册表。
- sandbox 测试在缺凭证时明确 skip，不冒充通过。

### 2. Tier-Based Acquiring 收单抽象

核心路径：

- `internal/acquirer/domain/domain.go`
- `internal/acquirer/adapter`
- `internal/acquirer/registry`
- `internal/admin/v3_channels_handler.go`
- `migrations/000028_tier_based_acquiring_issuing.up.sql`

已具备能力：

- `provider_tier` 解耦服务商名称与风险层级。
- `credential_vault_key` 只保存 Vault key，不保存明文凭证。
- `strategy_mode` 支持 `DEFAULT` 与 `BURN_MODE`，工程语义为人工审查/锁定模式。
- provider factory 未注册时 fail-closed。
- 配置更新通过 Redis Pub/Sub 通知热重载。

### 3. Tier-Based Issuing 发卡抽象

核心路径：

- `internal/issuing/adapter.go`
- `internal/issuing/domain.go`
- `internal/issuing/service.go`
- `internal/issuing/fraud_guard.go`
- `internal/admin/v3_issuing_pools_handler.go`

已具备能力：

- `IssuingAdapter` 统一抽象：CreateCard、FreezeCard、TerminateCard、ProcessAuthWebhook。
- `issuing_tier` 解耦聚合卡池与直连卡池。
- `funding_mode` 区分 `CRYPTO_DESENSITIZED` 与 `FIAT_DIRECT`，作为合规资金隔离模式。
- fraud guard 可基于滑动窗口拒付/欺诈阈值触发开卡权限冻结。
- 卡池动态新增、禁用、启用、reload。

### 4. VCC Funding 与复式记账

核心路径：

- `internal/treasury/vcc_funding_router.go`
- `internal/worker/vcc_funding/worker.go`
- `internal/ledger`
- `internal/treasury`

已具备能力：

- 商户收单结算余额到 VCC 可用余额的内部资金调拨。
- 高风险收单到法币直充卡池强制 fail-closed。
- 资金调拨保持审计链，不删除来源、不伪装来源。
- 失败时避免半成品余额。
- worker 执行异步 funding job。

### 5. Ledger 与资金一致性

核心路径：

- `internal/ledger/domain`
- `internal/ledger/repository`
- `internal/ledger/service`
- `internal/ledger/fee`

已具备能力：

- 双分录复式记账。
- 手续费分摊。
- 余额查询。
- 幂等交易。
- 与 payout、treasury、VCC funding 链路衔接。

### 6. MAE、风控与后场编排

核心路径：

- `internal/mae`
- `internal/risk`
- `internal/merchant/riskprofile`
- `internal/pipeline`

已具备能力：

- 交易/资金状态机。
- 手动审查阻断。
- 风险事件、风险冻结。
- 商户风险参数、滚动保证金、费用分摊。
- carding/fraud guard fail-closed 机制。

### 7. Payout、Reserve 与 Reconcile

核心路径：

- `internal/payout`
- `internal/worker/payout`
- `internal/worker/reconcile`
- `internal/worker/reserve_release`
- `internal/worker/fxlock`

已具备能力：

- payout 冻结、确认、释放。
- 幂等确认。
- reserve hold/release。
- reconcile 超限后进入 manual review。
- LP/Bank 外部调用接口抽象。

### 8. Compliance、RFI 与审计

核心路径：

- `internal/compliance/rfi`
- `internal/admin/handler.go`
- `internal/platform/httpx/dashboard.go`

已具备能力：

- RFI 文档生成、查看、审批、拒绝。
- approve/reject 危险操作确认。
- override audit logs 查询。
- 审计链记录 operator、reason、状态变化。

### 9. Admin API 与 Dashboard

核心路径：

- `cmd/api/main.go`
- `internal/admin`
- `internal/platform/httpx/dashboard.go`

已具备能力：

- `/admin/api/v3/channels` 收单通道管理。
- `/admin/api/v3/issuing/pools` 发卡池管理。
- `/admin/v3/*` 风控、保证金、费用、隔离组、RFI、审计查询。
- 中文错误、空态、成功/失败提示。
- 危险操作 confirm。
- 凭证不回显，仅显示 `has_credential`。

## 四、数据库迁移进度

关键迁移：

- `000015` dispute
- `000016` risk event
- `000017` acquirer registry
- `000020` universal adapter registry
- `000021` bank collection
- `000022` crypto pay
- `000023` multi asset ledger
- `000024` corporate entity
- `000025` v3 compliant controls
- `000028` tier-based acquiring and issuing

`000028` 核心枚举：

```text
provider_tier:
- TIER_1_HIGH_RISK
- TIER_2_COMPLIANT

issuing_tier:
- TIER_1_AGGREGATOR
- TIER_2_DIRECT

issuing_funding_mode:
- CRYPTO_DESENSITIZED
- FIAT_DIRECT
```

## 五、剩余工作

pre-production 后续进入 staging/production 前，必须完成：

1. 配置真实 Stripe sandbox 凭证并执行收单联调。
2. 配置真实 Nxtapay sandbox 凭证并执行收单/回调联调。
3. 配置真实 VCC provider sandbox 或 contract sandbox。
4. 接入真实 Vault/KMS，验证凭证密钥生命周期。
5. 接入真实 LP/Bank/MAE endpoint。
6. 执行迁移 up/down/up 演练。
7. 执行生产只读演练、故障注入、回滚演练。
8. 完成安全审计、日志脱敏审计和权限审计。

## 六、签收口径

可对外口径：

```text
NexusGlobal 3.0 已达到 mock/local 100% 与 pre-production 100% 可验收状态。
真实服务商 sandbox 联调因缺少凭证标记为 SKIPPED_MISSING_CREDENTIAL，凭证补齐后可立即执行真实联调。
```

不可对外口径：

```text
真实 Stripe/Nxtapay/VCC sandbox 已全部通过。
生产环境已 100% 就绪。
真实资金链路已完成生产验证。
```

