# GO_LIVE.md — Mock → Real 切换 Checklist

> 目标：从 mock 模式切换到 real 模式，只需改配置，不改任何业务代码。

---

## 1. 环境变量切换清单

| 配置项 | mock 值 | real 值 | 说明 |
|--------|---------|---------|------|
| `APP_ENV` | `local` | `production` | 触发生产安全校验 |
| `EXTERNAL_MODE` | `mock` | `real` | 切换 LP/Bank/MAE 连接器 |
| `KMS_MODE` | `mock` | `real` | 切换加密后端 |
| `JWT_SECRET` | (任意) | (真实密钥) | JWT 签名密钥 |
| `INTERNAL_TOKEN` | (任意) | (真实令牌) | 内部 API 认证 |
| `MASTER_KEY_BASE64` | 测试密钥 | (不用) | production 时 KMS 必须 real |
| `OTEL_COLLECTOR_URL` | (空) | `otel-collector:4317` | OpenTelemetry Collector 地址 |
| `OTEL_SAMPLE_RATIO` | `1.0` | `0.1` | 采样率（生产建议 10%） |

## 2. LP 连接器切换

```bash
# 切换
EXTERNAL_MODE=real
LP_BASE_URL=https://lp-api.example.com
LP_API_KEY=<真实 API Key>
```

**验证方法**：
1. 调用 `/internal/payout/{id}/fx-lock`，确认 LP 返回真实 lock_id
2. 检查 `fxlock` worker 日志，确认超时/失败路径正常

## 3. Bank 连接器切换

```bash
BANK_BASE_URL=https://bank-api.example.com
BANK_API_KEY=<真实 API Key>
```

**验证方法**：
1. 触发 Sweep Evaluate，确认银行转账请求发出
2. 检查 `sweep_transfer` 表状态变化

## 4. MAE 连接器切换

```bash
MAE_BASE_URL=https://mae-api.example.com
MAE_API_KEY=<真实 API Key>
```

**验证方法**：
1. 调用 MAE HealthCheck，确认响应正常
2. 检查路由引擎选中的 MAE 状态

## 5. KMS 切换

```bash
KMS_MODE=real
VAULT_BASE_URL=https://vault.example.com:8200
VAULT_TOKEN=<真实 Token>
VAULT_MOUNT_PATH=transit
VAULT_KEY_NAME=mamr-master
```

**验证方法**：
1. 创建出金地址，确认地址加密成功
2. 查询 `merchant_payout_address` 表，确认 `target_address_enc` 非空

## 6. 数据库 & Redis

```bash
POSTGRES_DSN=postgres://user:pass@prod-db:5432/nexus?sslmode=require
REDIS_ADDR=prod-redis:6379
```

## 7. OpenTelemetry 配置

```bash
OTEL_COLLECTOR_URL=otel-collector:4317
OTEL_SAMPLE_RATIO=0.1
```

**验证方法**：
1. 启动后检查 OTel Collector 是否收到 span
2. 调用 `/internal/payout/{id}/freeze`，确认资金路径有 span 记录

## 8. 托管收银台

收银台页面位于 `web/checkout/index.html`，descriptor 位于 `web/checkout/descriptor.json`。

**部署方式**：
1. 将 `web/checkout/` 目录部署到静态文件服务器
2. 配置反向代理将 `/checkout` 指向静态文件
3. 配置 `/api/checkout/*` 指向 API 服务

## 9. 切换后验证 Checklist

- [ ] `/healthz` 和 `/readyz` 正常响应
- [ ] 创建出金地址 → 加密落库
- [ ] Freeze → FXLock → LP Payout → Confirm 全链路
- [ ] Webhook captured → ledger 入账
- [ ] Sweep Evaluate → 银行转账
- [ ] MAE 熔断 → business_day_lock_until 写入
- [ ] Token 创建 → 收银台兑换
- [ ] OpenTelemetry span 正常上报
- [ ] 收银台页面正常加载

## 10. 回滚方法

1. 将 `EXTERNAL_MODE` 改回 `mock`
2. 将 `KMS_MODE` 改回 `mock`
3. 重启 api 和 worker 服务
4. 确认 `/healthz` 正常

---

> **重要**：切换前务必在 staging 环境完整验证所有链路。
