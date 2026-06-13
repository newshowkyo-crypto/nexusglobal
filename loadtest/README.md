# Load Testing

This directory contains k6 load testing scripts for the NexusGlobal MAMR system.

## Prerequisites

- [k6](https://k6.io/docs/getting-started/installation/) installed
- Running API server (default: http://localhost:8080)
- Valid authentication tokens

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BASE_URL` | `http://localhost:8080` | API server base URL |
| `ADMIN_TOKEN` | `test-admin-token` | Admin authentication token |
| `INTERNAL_TOKEN` | `test-internal-token` | Internal service authentication token |
| `MERCHANT_TOKEN` | `test-merchant-token` | Merchant authentication token |

## Running Tests

### Basic run
```bash
k6 run k6_script.js
```

### With custom environment variables
```bash
k6 run -e BASE_URL=http://localhost:8080 \
       -e ADMIN_TOKEN=your_admin_token \
       -e INTERNAL_TOKEN=your_internal_token \
       -e MERCHANT_TOKEN=your_merchant_token \
       k6_script.js
```

### With custom load profile
```bash
k6 run --vus 50 --duration 5m k6_script.js
```

### Output results to JSON
```bash
k6 run --out json=results.json k6_script.js
```

## Performance Targets (PRD §10.4)

| Metric | P95 Target |
|--------|------------|
| MAE L1 熔断生效 | ≤100ms |
| Payment Intent 创建 | ≤150ms |
| Token 生成 | ≤100ms |
| Webhook Proxy 验签入队 | ≤100ms |
| Ledger 入账 | ≤200ms |
| Payout Freeze 本地事务 | ≤200ms |
| Sweep 判断 | ≤300ms |
| Outbox Worker 任务拉取 | ≤100ms |

## Test Scenarios

The script covers all critical endpoints with realistic load patterns:

1. **MAE Circuit Breaker** - Tests internal circuit break endpoint
2. **Payment Intent** - Tests MAE creation flow
3. **Token Generation** - Tests merchant payout address creation
4. **Webhook Proxy** - Tests webhook ingestion
5. **Ledger Entry** - Tests payout freeze flow
6. **Payout Freeze** - Tests balance freezing
7. **Sweep Evaluate** - Tests treasury sweep evaluation
8. **Outbox Worker** - Tests outbox event listing