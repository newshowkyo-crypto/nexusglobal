# MEMORY.md — NexusGlobal MAMR-System

> 项目记忆文件，记录关键决策、踩坑、未决项。

---

## 关键决策

1. **Mock/Real 工厂模式**：所有外部依赖（LP/Bank/MAE/KMS）通过 `internal/platform/factories` 统一创建，cmd/api 和 cmd/worker 只调工厂，禁止硬编码 mock。切换只需改 `EXTERNAL_MODE=real` + 对应 base_url/api_key。

2. **Outbox 生命周期**：成功→Ack(Completed)、失败→Fail(+retry_count+指数退避)、超限→UpdateOutboxStatus(DeadLetter)。DeadLetter 直接设置状态，不先 Ack 再 Update。

3. **Reserve 计提批次**：使用 `reserve_batch` 表记录计提批次，worker 1h tick 扫描到期批次自动调用 ReserveRelease。

4. **Webhook 验签**：无 proxy 行拒绝请求（不再跳过验签）；按 `signature_algorithm` 字段分支 HMAC_SHA256/RSA_SHA256。

5. **熔断 business_day_lock_until**：EXPOSURE_THRESHOLD 时写入当日 23:59:59，保证当日不回 Active。

---

## 踩坑记录

1. **DeadLetter 逻辑顺序**：先 Ack(Completed) 再 UpdateStatus(DeadLetter) 导致状态不一致。改为直接 UpdateOutboxStatus(DeadLetter)。

2. **手写 contains 函数**：递归实现可疑且冗余，改为 `strings.Contains`。

3. **JWTSecret 为空时**：不能静默用空密钥校验，必须拒绝所有请求。

4. **Webhook event_type 不匹配**：handler 发 "webhook" 但 consumer 期望 "captured"/"settled"。改为从 payload 解析真实 event_type。

---

## 未决项

1. **P2-1 CI**：需要 GitHub Actions 配置
2. **P2-2 容器化**：需要 Dockerfile + docker-compose.yml
3. **P2-3 可观测**：需要 Prometheus /metrics + OpenTelemetry
4. **P2-5 负载测试**：需要 k6/vegeta 脚本
5. **P2-6 迁移验证**：需要 golang-migrate 集成
6. **P1-8 托管收银台**：PCI-scoped checkout 页面未实现
7. **P1-6 Webhook**：IP allowlist/rate_limit 强制执行、证书到期监控未实现
