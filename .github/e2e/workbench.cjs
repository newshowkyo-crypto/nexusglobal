const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

async function run() {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1672, height: 941 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.stack));
    const navigation = page.getByRole('navigation', { name: '后台导航' });
    const screenshot = async name => {
      const directory = process.env.WORKBENCH_SCREENSHOTS;
      if (directory) await page.screenshot({ path: path.join(directory, name), animations: 'disabled' });
    };

    await page.goto(process.env.WORKBENCH_BASE + '/admin/login');
    await page.getByLabel('管理员登录凭证').fill(process.env.WORKBENCH_TOKEN);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.waitForURL('**/admin/dashboard');
    await page.getByText('93.33%', { exact: true }).waitFor();
    await page.getByText('跨境测试站', { exact: true }).waitFor();
    await page.getByText('DEMO-ORDER-30', { exact: true }).waitFor();
    await screenshot('nexusglobal-workbench.png');

    await page.getByRole('button', { name: '排查支付异常' }).click();
    await page.getByText('第 1 页 · 1 条记录').waitFor();
    await page.getByRole('button', { name: '查看', exact: true }).click();
    await page.getByRole('dialog').getByText(/请用订单号核对机构记录/).waitFor();
    await page.keyboard.press('Escape');
    await page.getByRole('dialog').waitFor({ state: 'hidden' });
    await page.getByRole('combobox', { name: '筛选状态' }).selectOption('');
    await page.getByRole('button', { name: '查询', exact: true }).click();
    await page.getByText('第 1 页 · 25 条记录').waitFor();
    await page.getByRole('button', { name: '下一页' }).click();
    await page.getByText('第 2 页 · 5 条记录').waitFor();

    await navigation.getByRole('button', { name: '商户与站点' }).click();
    await page.getByText('跨境测试站', { exact: true }).filter({ visible: true }).waitFor();
    const siteDraft = page.locator('#tab-merchant-onboarding details').filter({ has: page.getByText('登记外部站点', { exact: true }) });
    await siteDraft.locator('summary').click();
    const siteMerchant = siteDraft.getByLabel('所属商户');
    const siteMerchantOption = siteMerchant.locator('option').filter({ hasText: '海岸运营测试商户' });
    await siteMerchantOption.waitFor({ state: 'attached' });
    await siteMerchant.selectOption(await siteMerchantOption.getAttribute('value'));
    await siteDraft.getByLabel('站点名称').fill('浏览器测试站');
    await siteDraft.getByLabel('站点代码').fill('store-browser');
    await siteDraft.getByLabel('实际销售站点域名').fill('https://browser.example.test');
    await siteDraft.getByRole('button', { name: '保存站点资料' }).click();
    await siteDraft.getByRole('status').getByText(/状态为待开通/).waitFor();
    await page.getByRole('cell', { name: '浏览器测试站' }).waitFor();
    await navigation.getByRole('button', { name: '收单通道' }).click();
    await page.locator('#tab-acquirer-channels summary').filter({ hasText: '新增收款通道' }).click();
    const merchant = page.locator('#new-acq-merchant');
    const merchantOption = merchant.locator('option').filter({ hasText: '海岸运营测试商户' });
    await merchantOption.waitFor({ state: 'attached' });
    await merchant.selectOption(await merchantOption.getAttribute('value'));
    await page.locator('#new-acq-provider-code').fill('stripe');
    await page.locator('#new-acq-channel-code').fill('stripe_browser_demo');
    await page.locator('#new-acq-currencies').fill('USD, EUR');
    const created = page.waitForResponse(response => response.request().method() === 'POST' &&
      response.url().endsWith('/admin/api/v3/channels'));
    await page.getByRole('button', { name: '创建通道' }).click();
    const createResponse = await created;
    assert.ok(createResponse.ok(), await createResponse.text());
    await page.getByRole('status').filter({ hasText: /已创建停用通道/ }).waitFor();
    await page.locator('#acquirer-channels-table').getByRole('button', { name: '凭证管理' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByText(/录入进度 0 \/ 2/).waitFor();
    for (const [index, field] of ['Secret Key / API Secret', 'Webhook 签名密钥'].entries()) {
      const item = dialog.locator('.credential-item').filter({ hasText: field });
      await item.locator('input').fill('workbench-test-only-' + index);
      await item.getByRole('button', { name: '加密保存' }).click();
      await item.getByText(/已保存 · 版本 1/).waitFor();
      assert.equal(await item.locator('input').inputValue(), '');
    }
    await dialog.getByText(/录入进度 2 \/ 2/).waitFor();
    assert.ok(!(await dialog.innerText()).includes('workbench-test-only-'));
    await screenshot('nexusglobal-credentials.png');
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'hidden' });

    await page.setViewportSize({ width: 390, height: 844 });
    const recentOrder = page.waitForResponse(response => response.url().endsWith('/admin/operations/records/orders'));
    await page.getByRole('combobox', { name: '切换管理功能' }).selectOption('overview');
    await page.getByText('93.33%', { exact: true }).waitFor();
    assert.ok((await recentOrder).ok());
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 390);
    assert.deepEqual(errors, []);
    console.log('Browser PASS: login, dashboard, tenant authorization, filtering, keyboard dialog, pagination, credential flow and mobile navigation.');
  } finally {
    await browser.close();
  }
}

run().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
