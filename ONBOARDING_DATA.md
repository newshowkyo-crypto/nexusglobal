# ONBOARDING_DATA — 上线前你需要准备的物料 + 录入格式 + 后台接入方案

> 本文件写给业务/运营/你本人。
> 目标：把"切真实需要准备什么、什么格式、怎么放进可视化后台"一次讲清。
> 配套：`GO_LIVE.md`（环境变量切换）、`.env.example`（配置项）、`PRD.md`（字段定义）。

---

## 0. 总览：三类物料

上线要准备的东西分三类，按"由谁给、放哪里"区分：

| 类别 | 内容 | 谁提供 | 放在哪 | 录入方式 |
|------|------|--------|--------|----------|
| A. 系统级密钥与连接 | 数据库、Redis、JWT、KMS、各上游 API 凭证 | 你 / 运维 | `.env`（服务器环境变量） | 改配置文件，不进后台 |
| B. 业务配置数据 | 收单实体(MAE)、银行账户、银行日历、归集规则、商户、出金地址、Webhook 接收代理 | 你 / 业务方 | 数据库（通过后台表单） | **可视化后台录入** |
| C. 收银台展示物料 | 商户名、Logo、条款、退款政策、客服联系方式 | 商户 | `descriptor.json` / 后台 | 后台录入或文件 |

**A 类是"开关和钥匙"，改 `.env` 即可，不要进后台**（密钥进数据库不安全）。
**B 类是"业务数据"，是后台要承载的主体。**
**C 类是"页面展示"，跟着商户走。**

---

## 1. A 类：系统级配置（填进 .env，不进后台）

切真实时，把下列值填入服务器的 `.env` 文件（参考 `.env.example`）。这部分是运维动作，不做成后台表单。

### 1.1 必填清单（生产）

| 配置项 | 格式 / 示例 | 从哪获取 | 备注 |
|--------|------------|----------|------|
| `APP_ENV` | `production` | 固定值 | 触发生产安全校验 |
| `POSTGRES_DSN` | `postgres://user:pass@host:5432/nexus?sslmode=require` | 你的数据库 | 生产建议 `sslmode=require` |
| `REDIS_ADDR` | `host:6379` | 你的 Redis | |
| `JWT_SECRET` | 32+ 位随机字符串 | 自己生成 | 见下方生成命令 |
| `INTERNAL_TOKEN` | 32+ 位随机字符串 | 自己生成 | 内部接口令牌 |
| `EXTERNAL_MODE` | `real` | 固定值 | 切真实上游 |
| `KMS_MODE` | `real` | 固定值 | 切真实加密 |
| `LP_BASE_URL` | `https://lp-api.xxx.com` | LP 服务商 | real 时必填，空则启动失败 |
| `LP_API_KEY` | LP 给的密钥 | LP 服务商 | |
| `BANK_BASE_URL` | `https://bank-api.xxx.com` | 银行/银行聚合方 | real 时必填 |
| `BANK_API_KEY` | 银行给的密钥 | 银行 | |
| `MAE_BASE_URL` | `https://acq-api.xxx.com` | 收单机构 | real 时必填 |
| `MAE_API_KEY` | 收单机构给的密钥 | 收单机构 | |
| `VAULT_BASE_URL` | `https://vault.xxx.com:8200` | 你的 Vault | KMS real 时必填 |
| `VAULT_TOKEN` | Vault Token | 你的 Vault | |
| `VAULT_MOUNT_PATH` | `transit` | 默认即可 | |
| `VAULT_KEY_NAME` | `mamr-master` | 默认即可 | |
| `OTEL_COLLECTOR_URL` | `otel-collector:4317` | 你的可观测栈 | 可选 |
| `OTEL_SAMPLE_RATIO` | `0.1` | 固定值 | 生产采样 10% |

### 1.2 密钥生成命令

```bash
# JWT_SECRET / INTERNAL_TOKEN（各跑一次，取输出）
openssl rand -base64 32

# 若仍要用本地 KMS（不推荐生产），生成 32 字节 master key
openssl rand -base64 32
```

### 1.3 校验

启动 api 时，若 `APP_ENV=production` 但任一 real 必填项为空，**服务会拒绝启动并报明确错误**（这是有意设计，防止误切）。看到启动成功 = A 类配置合规。

---

## 2. B 类：业务配置数据（可视化后台录入）

这是你说的"放进可视化后台、只需录入"的主体。下面给出**每张表你要准备的字段 + 格式 + 录入顺序**。

> **录入顺序很重要**（有外键依赖）：
> `银行账户 → 银行日历 → MAE 收单实体 → 归集规则 → Webhook 接收代理 → 商户 → 出金地址`

### 2.1 银行账户（最先录）

> 资金落地账户。MAE 结算、Sweep 来源/目标都引用它。

| 字段 | 格式 | 示例 | 必填 | 说明 |
|------|------|------|------|------|
| 账户名称 | 文本 | `US Landing USD` | ✅ | 展示名 |
| 账号 | 文本（将加密存储） | `1234567890` | ✅ | |
| 路由号/SWIFT | 文本 | `021000021` | ✅ | ABA/IBAN/SWIFT |
| 币种 | 3字母 | `USD` | ✅ | ISO 4217 |
| 账户类型 | 枚举 | `LANDING` / `TREASURY` | ✅ | 落地户/财资户 |
| 司法辖区 | 2字母 | `US` | ✅ | |

### 2.2 银行日历

> 控制 Sweep 在哪些时间窗口能提交。

| 字段 | 格式 | 示例 | 必填 |
|------|------|------|------|
| 日历名称 | 文本 | `US_FED` | ✅ |
| 司法辖区 | 2字母 | `US` | ✅ |
| 时区 | IANA 时区 | `America/New_York` | ✅ |
| 工作日 | JSON 数组 | `["Mon","Tue","Wed","Thu","Fri"]` | ✅ |
| 节假日 | JSON 数组(日期) | `["2025-12-25","2026-01-01"]` | ✅ |
| Cut-off 矩阵 | JSON 对象 | `{"ACH":"16:00","FedWire":"17:00","SEPA":"15:00"}` | ✅ |

### 2.3 MAE 收单实体（核心）

> 对应 PRD §3.4 `mae_configuration`。这是路由和熔断的主体。

| 字段 | 格式 | 示例 | 必填 | 说明 |
|------|------|------|------|------|
| MAE 编码 | 文本(唯一) | `acq_stripe_us_01` | ✅ | 唯一标识 |
| 展示名 | 文本 | `Stripe US 主通道` | ✅ | |
| 类型 | 枚举 | `CARD_GATEWAY` / `LOCAL_APM` / `BANK_TRANSFER` / `WALLET` / `A2A` | ✅ | |
| 司法辖区 | 2字母 | `US` | ✅ | |
| 支持币种 | JSON 数组 | `["USD","EUR"]` | ✅ | |
| 可服务租户 | JSON 数组 | `["tenant_a","tenant_b"]`（空=全部） | ⬜ | |
| API 地址 | URL | `https://api.acq.com` | ✅ | |
| **API Key** | 文本（加密存储） | `sk_live_xxx` | ✅ | 录入后即加密 |
| **Webhook Secret** | 文本（加密存储） | `whsec_xxx` | ✅ | 验签用 |
| 结算银行账户 | 选择(引用2.1) | `US Landing USD` | ✅ | |
| 单日额度上限(minor) | 整数 | `100000000`（=$1,000,000） | ✅ | 以最小单位(分) |
| 单月额度上限(minor) | 整数 | `2000000000` | ✅ | |
| 异常率阈值(bps) | 整数 | `500`（=5%） | ✅ | 熔断阈值 |
| 测卡阈值 | 整数 | `20` | ✅ | |
| 路由权重 | 整数 | `100` | ✅ | 越大越优先 |
| 冷却时长(分钟) | 整数 | `30` | ✅ | |
| Testing 灰度流量(bps) | 整数 | `500`（=5%） | ✅ | |
| Testing 最小交易数 | 整数 | `100` | ✅ | |
| Testing 最小成功率(bps) | 整数 | `9500`（=95%） | ✅ | |
| Testing 最大P95延迟(ms) | 整数 | `2000` | ✅ | |

> **金额单位说明**：所有 `_minor` 字段是"最小货币单位"。美元用分，$1,000,000 = `100000000`。

### 2.4 归集规则（Sweep Rule）

> 对应 PRD §5.4。控制资金怎么从落地户归集到财资户。

| 字段 | 格式 | 示例 | 必填 |
|------|------|------|------|
| 来源账户 | 选择(引用2.1) | `US Landing USD` | ✅ |
| 目标账户 | 选择(引用2.1) | `US Treasury USD` | ✅ |
| 币种 | 3字母 | `USD` | ✅ |
| 清算轨道 | 枚举 | `ACH`/`FedWire`/`SEPA`/`SWIFT` | ✅ |
| 受益人类型 | 枚举 | `SAME_NAME`/`THIRD_PARTY`/`THIRD_PARTY_CROSS_BORDER` | ✅ |
| 是否已验证同名 | 布尔 | `true` | ✅ |
| 触发阈值(minor) | 整数 | `50000000` | ✅ |
| 归集比例(bps) | 整数 | `10000`(=100%) | ✅ |
| 单笔最大(minor) | 整数 | `100000000` | ✅ |
| 保留余额(minor) | 整数 | `1000000` | ✅ |
| 归集模式 | 枚举 | `TIERED_SCHEDULED`/`THRESHOLD`/`MANUAL` | ✅ |
| 每日最大归集次数 | 整数 | `2` | ✅ |
| 定时归集时间 | JSON 数组 | `["15:00","16:30"]` | ✅ |
| 是否四眼复核 | 布尔 | `true` | ✅ |
| 审批阈值(minor) | 整数 | `50000000` | ✅ |

### 2.5 Webhook 接收代理（Receiver Proxy）

> 对应 PRD §4.5。每个上游(MAE/Bank/LP)回调的接收配置。

| 字段 | 格式 | 示例 | 必填 |
|------|------|------|------|
| 代理编码 | 文本(唯一) | `proxy_mae_us_01` | ✅ |
| 提供方类型 | 枚举 | `MAE`/`BANK`/`LP` | ✅ |
| 提供方ID | 选择 | (引用对应 MAE/银行/LP) | ✅ |
| 接收URL | URL | `https://wh.xxx.com/webhook/mae/acq_01` | ✅ |
| 区域 | 文本 | `us-east` | ⬜ |
| 环境 | 枚举 | `PROD`/`STAGING` | ✅ |
| 限流(每分钟) | 整数 | `600` | ✅ |
| 签名算法 | 枚举 | `HMAC_SHA256`/`RSA_SHA256` | ✅ |
| **Secret** | 文本(加密) | `whsec_xxx` | ✅ |
| Secret 版本 | 整数 | `1` | ✅ |
| 证书指纹 | 文本 | `AB:CD:...` | ⬜ |
| 证书到期 | 日期时间 | `2026-12-31T00:00:00Z` | ⬜ |
| IP 白名单 | JSON 数组 | `["1.2.3.4","5.6.7.8"]` | ⬜ |
| MQ Topic | 文本 | `stream:webhook:mae` | ✅ |

### 2.6 商户

| 字段 | 格式 | 示例 | 必填 |
|------|------|------|------|
| 商户ID | UUID/文本 | (系统生成) | ✅ |
| 币种 | 3字母 | `USD` | ✅ |
| 初始各项余额 | decimal | 一般全 0 起 | ✅ |

### 2.7 出金地址

> 对应 PRD §6.8。商户提现目标地址。

| 字段 | 格式 | 示例 | 必填 |
|------|------|------|------|
| 商户 | 选择(引用2.6) | | ✅ |
| 目标资产 | 枚举 | `USDT`/`USDC`/`BTC`/`ETH` | ✅ |
| 目标网络 | 枚举 | `TRON_TRC20`/`ETH_ERC20`/`BTC`/`SOL` | ✅ |
| 地址 | 文本(加密存储) | `TQ9a...8xLm` | ✅ |
| 地址备注 | 文本 | `主提现地址` | ⬜ |

> 录入时系统会自动：清洗格式、按网络校验、生成掩码和哈希、加密落库、做黑白名单匹配。你只需贴地址。

---

## 3. C 类：收银台展示物料

对应 `web/checkout/descriptor.json`。每个商户一份。

```json
{
  "name": "ACME 商城",
  "logo_url": "https://cdn.xxx.com/acme-logo.png",
  "terms_url": "https://acme.com/terms",
  "refund_policy": "30 天内可退款",
  "support_email": "support@acme.com",
  "support_phone": "+1-800-000-0000"
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | ✅ | 收银台和账单显示的商户名 |
| `logo_url` | ⬜ | 商户 Logo（https） |
| `terms_url` | ⬜ | 服务条款链接 |
| `refund_policy` | ✅ | 退款政策文字 |
| `support_email` | ✅ | 客服邮箱 |
| `support_phone` | ⬜ | 客服电话 |

---

## 4. 把 B 类做成"可视化后台录入"——给开发的接入方案

> 现状：后台 `/admin/dashboard` 已有 10 个只读标签页（总览/收单路由/代付/资金归集/回调/账务/异步/商户/接口/系统），数据走 `loadList(endpoint, tableId)`，后端 `internal/admin/handler.go` 用 SQL 查询返回 JSON。
> **缺口**：目前全是"只读列表"，没有"新增/编辑表单"。要实现"你只在后台录入即可"，需要补录入表单 + 写接口。

下面是给 mimo 的接入指令（可直接派工）：

```
为可视化后台补「业务配置录入」能力，让运营能在 /admin/dashboard 直接录入 B 类业务数据，无需写 SQL 或调 API。

范围（按 ONBOARDING_DATA.md §2 的 7 类）：银行账户、银行日历、MAE、归集规则、Webhook代理、商户、出金地址。

每类要做：
1. 后端写接口（已有部分 POST，缺的补全），全部走已有 service 层（含校验、加密、幂等），禁止前端直连 SQL。
   - 敏感字段(API Key/Secret/银行账号/出金地址)后端加密存储，前端只提交明文一次，列表只回显掩码。
2. 前端在对应 tab 增加「+ 新增」按钮 → 弹出表单(字段按 §2 各表格)，提交调 POST 接口。
   - 表单字段类型按文档：枚举做下拉、JSON 字段给带校验的文本域、金额字段标注 minor 单位并提供"元→minor"换算提示。
   - 引用类字段(如结算银行账户)做成下拉，选项来自已有列表接口。
3. 录入顺序依赖在 UI 上提示（银行账户→日历→MAE→归集规则→Webhook→商户→出金地址）。
4. 校验失败时把后端错误信息(apperr)展示到表单。
5. 每类补一个 handler 测试 + 一个前端提交冒烟测试。

铁律：复用 service 层、敏感字段加密、列表只回显掩码、不破资金红线。
做完跑 build/vet/gofmt/go test ./... -count=1 全绿，docker compose up 后实际在后台录入一条各类数据验证落库。
```

---

## 5. 你的行动清单（按顺序）

1. **找上游要凭证**：LP / Bank / 收单机构(MAE) 的 `BASE_URL` + `API_KEY` + `Webhook Secret`（A 类 + B 类 2.3/2.5 要用）。
2. **备好基础设施**：生产 PostgreSQL、Redis、Vault（或决定先用本地 KMS 过渡）。
3. **生成密钥**：`JWT_SECRET`、`INTERNAL_TOKEN`（§1.2 命令）。
4. **整理业务数据**：按 §2 的 7 张表格，把银行账户、日历、MAE、归集规则、Webhook、商户、出金地址的值填进 Excel/表单草稿。
5. **整理收银台物料**：每个商户的 descriptor（§3）。
6. **让 mimo 补后台录入表单**（§4 指令）。
7. **staging 演练**：先 `EXTERNAL_MODE=mock` 把 B 类数据录入跑通全链路，再切 `real` 填 A 类凭证，按 `GO_LIVE.md` §9 checklist 逐项验证。
8. **上线**。

---

## 6. 重要提醒

- **密钥永远不要进数据库/后台明文存储**：API Key、Webhook Secret、银行账号、出金地址，后端用 AES-256-GCM(+Vault) 加密，后台列表只显示掩码（如 `sk_live_****1234`）。
- **金额一律用 minor 单位**：录入时想清楚是"元"还是"分"，$100 = `10000`(美元 minor)。后台表单建议加换算提示。
- **token store 多副本问题**：收银台目前用内存 token store，若 api 部署多副本，需先让 mimo 换成 Redis 实现（已记入 AUDIT_LOG 待办）。
- **HandleCharge 是占位**：收银台的扣款端点目前只返回成功，未接真实支付链路，切真实前需让 mimo 接入 MAE 路由扣款（已在 PROJECT_STATUS 标注）。
