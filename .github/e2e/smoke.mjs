// NexusGlobal Browser E2E Smoke Test
// Real assertions against live API endpoints — no 404-as-pass, no missing-route-as-pass.
// Each test asserts a specific HTTP status code and, where applicable, response body structure.

import { chromium } from 'playwright';

const BASE = process.env.API_BASE || 'http://localhost:18080';

const results = [];
function record(name, ok, detail = '') {
  results.push({ name, ok, detail });
  const tag = ok ? 'PASS' : 'FAIL';
  console.log(`[${tag}] ${name}${detail ? ' — ' + detail : ''}`);
}

// Helper: assert specific HTTP status code
function assertStatus(actual, expected, testName) {
  if (actual !== expected) {
    record(testName, false, `expected status ${expected}, got ${actual}`);
    return false;
  }
  return true;
}

// Helper: parse JSON body safely
async function parseJson(resp) {
  if (!resp) return null;
  const text = await resp.text();
  try {
    return JSON.parse(text);
  } catch (_) {
    return null;
  }
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();

  // ------------------------------------------------------------
  // 1. healthz 检查 — GET /healthz 必须返回 200 + JSON { "status": "ok" }
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/healthz`, { waitUntil: 'networkidle', timeout: 10000 });
    const status = resp ? resp.status() : 0;
    if (!assertStatus(status, 200, 'healthz-status-200')) {
      // already recorded failure
    } else {
      const body = await parseJson(resp);
      if (body && body.status === 'ok') {
        record('healthz-body-status-ok', true, `status=ok`);
      } else {
        record('healthz-body-status-ok', false, `body=${JSON.stringify(body)}`);
      }
    }
  } catch (e) {
    record('healthz-status-200', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 2. Admin 401 检查 — GET /admin/dashboard 无 token 必须返回 401 或 403
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/admin/dashboard`, { waitUntil: 'domcontentloaded', timeout: 10000 });
    const status = resp ? resp.status() : 0;
    if (status === 401 || status === 403) {
      record('admin-dashboard-401-403', true, `status=${status} (route exists, auth required)`);
    } else {
      record('admin-dashboard-401-403', false, `expected 401 or 403, got ${status}`);
    }
  } catch (e) {
    record('admin-dashboard-401-403', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 3. Payment Link resolve — GET /pay/{invalid-token} 必须返回 404 + { "error": "PAYMENT_LINK_NOT_AVAILABLE" }
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/pay/invalid-token-xyz123`, { waitUntil: 'domcontentloaded', timeout: 10000 });
    const status = resp ? resp.status() : 0;
    if (!assertStatus(status, 404, 'payment-link-resolve-404')) {
      // failure already recorded
    } else {
      const body = await parseJson(resp);
      if (body && body.error === 'PAYMENT_LINK_NOT_AVAILABLE') {
        record('payment-link-resolve-error-field', true, `error=PAYMENT_LINK_NOT_AVAILABLE`);
      } else {
        record('payment-link-resolve-error-field', false, `body=${JSON.stringify(body)}`);
      }
    }
  } catch (e) {
    record('payment-link-resolve-404', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 4. Payment Link confirm — POST /pay/{invalid-token}/confirm 必须返回 404
  // ------------------------------------------------------------
  try {
    const resp = await context.request.post(`${BASE}/pay/invalid-token-xyz123/confirm`, {
      data: {},
      timeout: 10000,
    });
    const status = resp ? resp.status() : 0;
    assertStatus(status, 404, 'payment-link-confirm-404');
  } catch (e) {
    record('payment-link-confirm-404', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 5. Checkout v3 resolve — GET /checkout/v3/{invalid-session} 必须返回 404 + { "error": "SESSION_NOT_AVAILABLE" }
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/checkout/v3/invalid-session-xyz123`, { waitUntil: 'domcontentloaded', timeout: 10000 });
    const status = resp ? resp.status() : 0;
    if (!assertStatus(status, 404, 'checkout-v3-resolve-404')) {
      // failure already recorded
    } else {
      const body = await parseJson(resp);
      if (body && body.error === 'SESSION_NOT_AVAILABLE') {
        record('checkout-v3-resolve-error-field', true, `error=SESSION_NOT_AVAILABLE`);
      } else {
        record('checkout-v3-resolve-error-field', false, `body=${JSON.stringify(body)}`);
      }
    }
  } catch (e) {
    record('checkout-v3-resolve-404', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 6. 安全响应头检查 — CSP, X-Frame-Options, X-Content-Type-Options
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/healthz`, { waitUntil: 'networkidle', timeout: 10000 });
    const headers = resp ? await resp.allHeaders() : {};
    const hasCsp = !!headers['content-security-policy'];
    const hasXfo = !!headers['x-frame-options'];
    const hasXcto = !!headers['x-content-type-options'];
    record('security-headers-csp', hasCsp, hasCsp ? 'CSP present' : 'CSP missing');
    record('security-headers-xfo', hasXfo, hasXfo ? 'X-Frame-Options present' : 'missing');
    record('security-headers-xcto', hasXcto, hasXcto ? 'X-Content-Type-Options present' : 'missing');
  } catch (e) {
    record('security-headers', false, String(e.message || e));
  }

  // ------------------------------------------------------------
  // 7. 未知路由 404 — GET /api/v3/nonexistent-endpoint-xyz 必须返回 404
  // ------------------------------------------------------------
  try {
    const resp = await page.goto(`${BASE}/api/v3/nonexistent-endpoint-xyz`, { waitUntil: 'domcontentloaded', timeout: 10000 });
    const status = resp ? resp.status() : 0;
    assertStatus(status, 404, 'unknown-route-404');
  } catch (e) {
    record('unknown-route-404', false, String(e.message || e));
  }

  await browser.close();

  // ------------------------------------------------------------
  // 汇总
  // ------------------------------------------------------------
  const passed = results.filter(r => r.ok).length;
  const failed = results.filter(r => !r.ok).length;
  console.log(`\n=== E2E Smoke Summary: ${passed} passed, ${failed} failed ===`);

  if (failed > 0) {
    console.error('E2E smoke test FAILED');
    process.exit(1);
  }
  console.log('E2E smoke test PASSED');
})().catch((e) => {
  console.error('E2E smoke test crashed:', e);
  process.exit(1);
});
