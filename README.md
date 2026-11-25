# NexusGlobal MAMR System

Enterprise-grade cross-border treasury / acquiring / clearing / disbursement system.

## Quick Start

```bash
cp .env.example .env
make up
make migrate
make build
make test-short
```

## Mock / Local Demo Mode

The system supports a complete mock/development mode for local demonstration without external dependencies.

### Setup and Run

```bash
# 1. Start services
make up

# 2. Run database migrations
make migrate

# 3. Start API server
make run-api

# 4. Start worker (in another terminal)
make run-worker
```

### Creating Demo Data

Use the admin endpoints to create demo data:

```bash
# Create a merchant
curl -X POST http://localhost:8080/admin/merchant-create \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"merchant_id": "550e8400-e29b-41d4-a716-446655440000", "currency": "USD"}'

# Create a bank account
curl -X POST http://localhost:8080/admin/bank-account \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"bank_account_id": "550e8400-e29b-41d4-a716-446655440001", "currency": "USD", "accumulated_minor": 1000000}'

# Create MAE config
curl -X POST http://localhost:8080/admin/mae-config \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"mae_id": "mae_demo", "display_name": "Demo MAE", "mae_type": "CARD_GATEWAY", "settlement_bank_account_id": "550e8400-e29b-41d4-a716-446655440001", "api_key": "sk_test_demo", "webhook_secret": "whsec_demo"}'

# Create webhook proxy
curl -X POST http://localhost:8080/admin/webhook-proxy \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"provider_type": "MAE", "provider_id": "mae_demo", "webhook_url": "https://webhook.site/example", "signature_algo": "HMAC-SHA256", "secret": "webhook_secret_demo", "ip_allowlist": [], "rate_limit": 100}'

# Create payout address
curl -X POST http://localhost:8080/admin/payout-address \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"merchant_id": "550e8400-e29b-41d4-a716-446655440000", "target_asset": "BTC", "target_network": "bitcoin", "address_format_type": "bech32", "target_address": "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", "address_label": "BTC Wallet"}'
```

### Checkout Flow Demo

```bash
# 1. Create a checkout token
curl -X POST http://localhost:8080/api/checkout/token \
  -H "Content-Type: application/json" \
  -d '{"order_id": "order_123", "amount": "99.99", "currency": "USD"}'

# 2. Open checkout page in browser
# http://localhost:8080/checkout?token=<token_value>

# 3. Validate token via API
curl http://localhost:8080/api/checkout/validate?token=<token_value>

# 4. Process payment (PAY-001: raw card data is NOT accepted — the checkout
#    page never collects PAN/CVV. Card data must be tokenized by the PSP's
#    Hosted Fields / SDK and submitted as payment_method_token.)
curl -X POST http://localhost:8080/api/checkout/charge \
  -H "Content-Type: application/json" \
  -d '{"token": "<token_value>", "payment_method_token": "<psp_token>"}'
```

> **Note (PAY-002):** `success_url` / `cancel_url` used by Checkout Sessions and
> Payment Links must be HTTPS and their hosts must be registered in the
> per-merchant allowlist (`merchant_return_url_allowlist`, migration 000053).
> Unregistered hosts are rejected with `RETURN_URL_NOT_ALLOWED` (fail-closed).
>
> **Note (PAY-003):** `GET /checkout/v3/{session_id}` and `GET /pay/{token}`
> return a real HTML status page (active/success/declined/expired/used/unknown)
> to browsers (`Accept: text/html`); API clients still receive JSON.

### Mock Payment Gateway Scenarios

The mock payment gateway supports three scenarios:

- **Success**: Default behavior, returns successful payment
- **Failure**: Returns payment failure (402)
- **Timeout**: Simulates timeout (504)

To trigger different scenarios, use specific order IDs:
- `order_success` or `order_123` → Success (default)
- `order_fail` → Failure (402)
- `order_timeout` → Timeout (504)

Example:
```bash
# Success scenario
curl -X POST http://localhost:8080/api/checkout/charge \
  -H "Content-Type: application/json" \
  -d '{"token": "<token>", "payment_method_token": "<psp_token>"}'

# Failure scenario (create token with order_id=order_fail first)
curl -X POST http://localhost:8080/api/checkout/token \
  -H "Content-Type: application/json" \
  -d '{"order_id": "order_fail", "amount": "99.99", "currency": "USD"}'

# Timeout scenario (create token with order_id=order_timeout first)
curl -X POST http://localhost:8080/api/checkout/token \
  -H "Content-Type: application/json" \
  -d '{"order_id": "order_timeout", "amount": "99.99", "currency": "USD"}'
```

### Acquirer Channel Management Demo

The system includes a visual channel management dashboard for managing payment providers and channels.

#### Seed Mock Acquirer Data

```bash
# Run seed command to create mock provider and channel
go run ./cmd/seed

# Or with explicit database URL
DATABASE_URL="postgres://postgres:postgres@localhost:5432/mamr?sslmode=disable" go run ./cmd/seed
```

This creates:
- Provider: `mock` (Mock Provider)
- Channel: `mock_default` (Mock Default Channel)
- Credential vault entry for mock provider

#### Dashboard Access

1. Open dashboard: http://localhost:8080/admin/dashboard
2. Sign in using your existing administrator account if prompted.
3. Open "全部业务功能" in the sidebar to find additional business pages.

The Funds Overview shows the current tenant's daily captured payments, confirmed payouts,
available balance, and follow-up counts in Beijing business time (UTC+8). Amounts are
grouped by currency; the CSV keeps exact amounts in minor units. Settlement Management
lists posted batches, captured payments awaiting settlement, and bank reconciliation
exceptions as separate views. The Component Specification tab previews the visual states
without calling any business mutation endpoints.

#### Channel Operations via API

```bash
# List providers
curl -X GET http://localhost:8080/admin/acquirer/providers \
  -H "Authorization: Bearer <admin_token>"

# Create provider
curl -X POST http://localhost:8080/admin/acquirer/providers \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"provider_code": "mock", "name": "Mock Provider", "provider_type": "acquirer"}'

# List channels
curl -X GET http://localhost:8080/admin/acquirer/channels \
  -H "Authorization: Bearer <admin_token>"

# Create channel
curl -X POST http://localhost:8080/admin/acquirer/channels \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{"tenant_id": "00000000-0000-0000-0000-000000000001", "merchant_id": "00000000-0000-0000-0000-000000000001", "provider_id": "<provider_id>", "channel_code": "mock_default", "channel_name": "Mock Default Channel"}'

# Enable channel
curl -X POST http://localhost:8080/admin/acquirer/channels/<channel_id>/enable \
  -H "Authorization: Bearer <admin_token>"

# Disable channel
curl -X POST http://localhost:8080/admin/acquirer/channels/<channel_id>/disable \
  -H "Authorization: Bearer <admin_token>"

# Reload channel (refresh from database)
curl -X POST http://localhost:8080/admin/acquirer/channels/<channel_id>/reload \
  -H "Authorization: Bearer <admin_token>"
```

#### Risk Control Integration

The checkout flow includes pre-acquirer risk control checks:

- **IP threshold**: Blocks after 5 failures from same IP (60s window)
- **BIN threshold**: Blocks after 3 failures from same BIN (5m window)
- **App failure rate**: Blocks when app failure rate exceeds 30% (1h window)
- **Merchant cooldown**: Blocks during cooldown period

When blocked, the API returns:
- 429 Too Many Requests for threshold exceeded
- 403 Forbidden for merchant cooldown

```bash
# Risk blocked response example
# Response: {"error": "risk_blocked", "reason": "ip_threshold_exceeded", "blocked_by": "buyer_ip", "retry_after": 60}
```

#### Real Mode Boundaries

When `EXTERNAL_MODE=real` or `ACQUIRER_MODE=real`:

- Raw card data (PAN/CVV) is **rejected** - must use `payment_method_token`
- Missing real provider/channel/credential causes **fail-closed**
- No silent fallback to mock
- Provider factory validates supported providers only

```bash
# Real mode rejection example
# Request with raw card data:
curl -X POST http://localhost:8080/api/checkout/charge \
  -H "Content-Type: application/json" \
  -d '{"token": "<token>", "card_number": "4242424242424242", ...}'
# Response: {"error": "raw card not allowed in real mode"}
```

## Development

```bash
make build        # Compile
make test-short   # Unit tests
make test         # Full integration tests (needs make up)
make lint         # Static analysis
make ci           # Full CI check
make redlines     # CONSTRAINTS.md redline verification
```

## Architecture

- Go 1.25.0 modular monolith
- PostgreSQL 15 (pgx/v5 + sqlc)
- Redis 7 (Streams MQ)
- Zero external network dependencies in dev/CI (all mocked)

## License

Proprietary
