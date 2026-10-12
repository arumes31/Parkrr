'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');

(async () => {
    const browser = await chromium.launch({ headless: true });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let logoutFails = true;
    const requests = await mockUI(page, { role: 'admin', overrides: { ...overrides,
        '/auth/logout': route => route.fulfill({ status: logoutFails ? 500 : 200,
            contentType: 'application/json', body: JSON.stringify(logoutFails ? { error: 'synthetic failure' } : {}) }),
    } });
    await page.goto(origin + '/#/taxyear');
    await page.getByText('Nicht geprüft', { exact: true }).waitFor();
    assert.equal(await page.locator('.tax-limit progress').count(), 0);
    for (const [label, width, height] of [['desktop', 1440, 1000], ['mobile', 390, 844]]) {
        await page.setViewportSize({ width, height });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), label + ' overflow');
        await page.screenshot({ path: path.resolve(__dirname, '../../docs/anwendungsreview/evidence/after-taxyear-' + label + '.png'), fullPage: true });
    }
    // Simulate the persisted pending marker after a failed request, then exercise
    // the actual startup/retry path. All auth requests remain intercepted.
    await page.evaluate(() => sessionStorage.setItem('parkrr.logoutPending', '1'));
    const start = requests.length;
    await page.reload();
    await page.locator('#logout-pending').waitFor();
    assert.equal(await page.locator('#app-view').isVisible(), false);
    assert.equal(await page.locator('#login-form').isVisible(), false);
    assert.equal(requests.slice(start).filter(r => r.path === '/auth/me').length, 0);
    await page.screenshot({ path: path.resolve(__dirname, '../../docs/anwendungsreview/evidence/after-logout-mobile.png'), fullPage: true });
    logoutFails = false;
    await page.getByRole('button', { name: 'Abmeldung erneut versuchen' }).click();
    await page.locator('#logout-pending').waitFor({ state: 'detached' });
    assert.equal(await page.locator('#login-form').isVisible(), true);
    assert.equal(await page.evaluate(() => sessionStorage.getItem('parkrr.logoutPending')), null);
    assert.deepEqual(errors, []);
    await browser.close();
    console.log('PASS: synthetic desktop/mobile tax display; failed logout reload stays locked; retry succeeds; no page errors.');
})().catch(error => { console.error(error); process.exit(1); });
