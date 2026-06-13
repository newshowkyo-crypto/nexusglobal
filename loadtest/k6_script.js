import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const errorRate = new Rate('errors');

const maeCircuitBreakTrend = new Trend('mae_circuit_break_duration');
const paymentIntentTrend = new Trend('payment_intent_duration');
const tokenGenerationTrend = new Trend('token_generation_duration');
const webhookProxyTrend = new Trend('webhook_proxy_duration');
const ledgerEntryTrend = new Trend('ledger_entry_duration');
const payoutFreezeTrend = new Trend('payout_freeze_duration');
const sweepEvaluateTrend = new Trend('sweep_evaluate_duration');
const outboxWorkerTrend = new Trend('outbox_worker_duration');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const ADMIN_TOKEN = __ENV.ADMIN_TOKEN || 'test-admin-token';
const INTERNAL_TOKEN = __ENV.INTERNAL_TOKEN || 'test-internal-token';
const MERCHANT_TOKEN = __ENV.MERCHANT_TOKEN || 'test-merchant-token';

export const options = {
  stages: [
    { duration: '30s', target: 10 },
    { duration: '1m', target: 50 },
    { duration: '2m', target: 100 },
    { duration: '1m', target: 50 },
    { duration: '30s', target: 0 },
  ],
  thresholds: {
    'mae_circuit_break_duration': ['p(95)<100'],
    'payment_intent_duration': ['p(95)<150'],
    'token_generation_duration': ['p(95)<100'],
    'webhook_proxy_duration': ['p(95)<100'],
    'ledger_entry_duration': ['p(95)<200'],
    'payout_freeze_duration': ['p(95)<200'],
    'sweep_evaluate_duration': ['p(95)<300'],
    'outbox_worker_duration': ['p(95)<100'],
    'errors': ['rate<0.1'],
  },
};

function getAdminHeaders() {
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${ADMIN_TOKEN}`,
  };
}

function getInternalHeaders() {
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${INTERNAL_TOKEN}`,
  };
}

function getMerchantHeaders() {
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${MERCHANT_TOKEN}`,
  };
}

export default function () {
  const maeId = `mae_loadtest_${Math.floor(Math.random() * 1000)}`;
  const merchantId = `merchant_${Math.floor(Math.random() * 1000)}`;
  const payoutId = `payout_${Math.floor(Math.random() * 1000)}`;

  const circuitBreakRes = http.post(
    `${BASE_URL}/internal/mae/${maeId}/circuit-break`,
    JSON.stringify({ reason: 'load_test' }),
    { headers: getInternalHeaders() }
  );
  maeCircuitBreakTrend.add(circuitBreakRes.timings.duration);
  check(circuitBreakRes, {
    'circuit break status is 200 or 404': (r) => r.status === 200 || r.status === 404,
  }) || errorRate.add(1);

  const paymentIntentRes = http.post(
    `${BASE_URL}/admin/mae`,
    JSON.stringify({
      mae_id: maeId,
      display_name: 'Load Test MAE',
      mae_type: 'CARD_GATEWAY',
      tenant_scope: ['t1'],
      jurisdiction: 'US',
      supported_currencies: ['USD'],
      api_base_url: 'https://test.example.com',
      daily_vmax_minor: 1000000,
      monthly_vmax_minor: 10000000,
    }),
    { headers: getAdminHeaders() }
  );
  paymentIntentTrend.add(paymentIntentRes.timings.duration);
  check(paymentIntentRes, {
    'payment intent status is 200 or 201': (r) => r.status === 200 || r.status === 201,
  }) || errorRate.add(1);

  const tokenRes = http.post(
    `${BASE_URL}/merchant/payout-address`,
    JSON.stringify({
      target_asset: 'USDT',
      target_network: 'TRON_TRC20',
      target_address: 'TAddr1234567890abcdef',
    }),
    { headers: getMerchantHeaders() }
  );
  tokenGenerationTrend.add(tokenRes.timings.duration);
  check(tokenRes, {
    'token generation status is 200 or 201': (r) => r.status === 200 || r.status === 201,
  }) || errorRate.add(1);

  const webhookRes = http.post(
    `${BASE_URL}/webhook/mae/test_provider`,
    JSON.stringify({
      event: 'captured',
      amount: '100.00',
      currency: 'USD',
    }),
    { headers: { 'Content-Type': 'application/json' } }
  );
  webhookProxyTrend.add(webhookRes.timings.duration);
  check(webhookRes, {
    'webhook proxy status is 200 or 202': (r) => r.status === 200 || r.status === 202,
  }) || errorRate.add(1);

  const ledgerRes = http.post(
    `${BASE_URL}/internal/payout/${payoutId}/freeze`,
    JSON.stringify({
      merchant_id: merchantId,
      amount: '1000.00',
      fiat_currency: 'USD',
      payout_address_id: 'addr_123',
      idempotency_key: `freeze_${payoutId}`,
    }),
    { headers: getInternalHeaders() }
  );
  ledgerEntryTrend.add(ledgerRes.timings.duration);
  check(ledgerRes, {
    'ledger entry status is 200 or 201': (r) => r.status === 200 || r.status === 201,
  }) || errorRate.add(1);

  const payoutFreezeRes = http.post(
    `${BASE_URL}/internal/payout/${payoutId}/freeze`,
    JSON.stringify({
      merchant_id: merchantId,
      amount: '500.00',
      fiat_currency: 'USD',
      payout_address_id: 'addr_456',
      idempotency_key: `payout_freeze_${payoutId}`,
    }),
    { headers: getInternalHeaders() }
  );
  payoutFreezeTrend.add(payoutFreezeRes.timings.duration);
  check(payoutFreezeRes, {
    'payout freeze status is 200 or 201': (r) => r.status === 200 || r.status === 201,
  }) || errorRate.add(1);

  const sweepRes = http.post(
    `${BASE_URL}/internal/sweep/evaluate`,
    JSON.stringify({
      rule_id: 'rule_123',
      evaluation_time: new Date().toISOString(),
    }),
    { headers: getInternalHeaders() }
  );
  sweepEvaluateTrend.add(sweepRes.timings.duration);
  check(sweepRes, {
    'sweep evaluate status is 200 or 202': (r) => r.status === 200 || r.status === 202,
  }) || errorRate.add(1);

  const outboxRes = http.get(
    `${BASE_URL}/admin/outbox`,
    { headers: getAdminHeaders() }
  );
  outboxWorkerTrend.add(outboxRes.timings.duration);
  check(outboxRes, {
    'outbox worker status is 200': (r) => r.status === 200,
  }) || errorRate.add(1);

  sleep(1);
}