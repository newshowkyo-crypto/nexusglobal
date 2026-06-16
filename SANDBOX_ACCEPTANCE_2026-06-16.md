# NexusGlobal 3.0 Sandbox 验收报告

**日期**: 2026-06-16  
**环境**: Windows 11 / Go 1.22  
**测试命令**: `go test ./test/integration -count=1 -v`

---

## 收单 Provider 验收状态

### Stripe

| 测试用例 | 状态 | 原因 |
|----------|------|------|
| TestStripeSandbox_AuthorizeCaptureRefund | SKIPPED_MISSING_CREDENTIAL | STRIPE_SANDBOX_SECRET 未设置 |
| TestStripeSandbox_WebhookSignature | SKIPPED_MISSING_CREDENTIAL | STRIPE_SANDBOX_SECRET 或 STRIPE_SANDBOX_WEBHOOK_SECRET 未设置 |
| TestCredentialNotExposed | SKIPPED_MISSING_CREDENTIAL | STRIPE_SANDBOX_SECRET 未设置 |

**验收说明**: 需要设置环境变量后重新运行测试：
```bash
export STRIPE_SANDBOX_SECRET=sk_test_your_secret_key
export STRIPE_SANDBOX_WEBHOOK_SECRET=whsec_your_webhook_secret
go test ./test/integration -run TestStripe -v
```

### Nxtapay

| 测试用例 | 状态 | 原因 |
|----------|------|------|
| TestNxtapaySandbox_AuthorizeCaptureRefund | SKIPPED_MISSING_CREDENTIAL | NXTAPAY_SANDBOX_API_KEY / NXTAPAY_SANDBOX_API_SECRET / NXTAPAY_SANDBOX_MERCHANT_ID 未设置 |
| TestNxtapaySandbox_WebhookSignature | SKIPPED_MISSING_CREDENTIAL | NXTAPAY_SANDBOX_WEBHOOK_SECRET 未设置 |

**验收说明**: 需要设置环境变量后重新运行测试：
```bash
export NXTAPAY_SANDBOX_API_KEY=your_api_key
export NXTAPAY_SANDBOX_API_SECRET=your_api_secret
export NXTAPAY_SANDBOX_MERCHANT_ID=your_merchant_id
export NXTAPAY_SANDBOX_WEBHOOK_SECRET=your_webhook_secret
go test ./test/integration -run TestNxtapay -v
```

---

## 发卡 Provider 验收状态

### VCC Sandbox

| 测试用例 | 状态 | 原因 |
|----------|------|------|
| CreateCard | SKIPPED_MISSING_CREDENTIAL | VCC_SANDBOX_PROVIDER / VCC_SANDBOX_API_KEY 未设置 |
| FreezeCard | SKIPPED_MISSING_CREDENTIAL | VCC_SANDBOX_PROVIDER / VCC_SANDBOX_API_KEY 未设置 |
| TerminateCard | SKIPPED_MISSING_CREDENTIAL | VCC_SANDBOX_PROVIDER / VCC_SANDBOX_API_KEY 未设置 |
| ProcessAuthWebhook | SKIPPED_MISSING_CREDENTIAL | VCC_SANDBOX_WEBHOOK_SECRET 未设置 |

**验收说明**: 当前无真实发卡 sandbox 凭证，已实现 contract test + disabled fail-closed 模式。设置凭证后需要补充 integration test。

---

## 验收标准说明

### SKIPPED_MISSING_CREDENTIAL 定义
- 测试用例使用 `t.Skip()` 跳过，不影响测试套件整体状态
- 仅当环境变量中存在对应 sandbox 凭证时才执行真实联调
- 凭证不存在时输出明确的跳过原因

### PASSED 定义（当凭证存在时）
- Authorize → Capture → Refund 完整流程通过
- Webhook 签名校验通过（空 payload、无效签名被正确拒绝）
- Provider timeout/retry/fail-closed 逻辑通过
- 凭证不回显、不入库、不进日志

### FAILED 定义
- 任一核心流程步骤失败
- Webhook 校验异常
- 凭证泄露

---

## 环境变量清单

### 收单 Sandbox 凭证
| 环境变量 | 必填 | 说明 |
|----------|------|------|
| STRIPE_SANDBOX_SECRET | 是 | Stripe Sandbox Secret Key |
| STRIPE_SANDBOX_WEBHOOK_SECRET | 是 | Stripe Webhook Secret |
| NXTAPAY_SANDBOX_API_KEY | 是 | Nxtapay API Key |
| NXTAPAY_SANDBOX_API_SECRET | 是 | Nxtapay API Secret |
| NXTAPAY_SANDBOX_MERCHANT_ID | 是 | Nxtapay Merchant ID |
| NXTAPAY_SANDBOX_WEBHOOK_SECRET | 是 | Nxtapay Webhook Secret |

### 发卡 Sandbox 凭证
| 环境变量 | 必填 | 说明 |
|----------|------|------|
| VCC_SANDBOX_PROVIDER | 是 | VCC 发卡 Provider 名称 |
| VCC_SANDBOX_API_KEY | 是 | VCC Sandbox API Key |
| VCC_SANDBOX_WEBHOOK_SECRET | 是 | VCC Webhook Secret |

---

## 后续操作

1. **获取 Sandbox 凭证**: 联系各 Provider 获取 sandbox 测试凭证
2. **设置环境变量**: 将凭证填入 `.env` 文件或系统环境变量
3. **重新运行测试**: `go test ./test/integration -count=1 -v`
4. **更新验收状态**: 根据实际测试结果更新本报告

---

**验收人**: Auto-generated  
**验收时间**: 2026-06-16
