'use strict';

// Offline report verification only: no Parkrr instance, database, or Docker access.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { fileURLToPath, pathToFileURL } = require('node:url');
const { chromium } = require('playwright');
const AxeBuilder = require('@axe-core/playwright').default;

const root = path.resolve(__dirname, '../..');
const reportDir = path.join(root, 'docs/anwendungsreview');
const reportUrl = pathToFileURL(path.join(reportDir, 'index.html')).href;
const evidenceName = process.env.PARKRR_UI_REVIEW_EVIDENCE || 'ui-review-overview-20261012';
if (!/^[a-z0-9-]+$/.test(evidenceName)) throw Error('Invalid evidence directory name');
const outputDir = path.join(reportDir, 'evidence', evidenceName);
const functionalOnly = process.argv.includes('--functional-only');
const expectedIds = ['UI01', 'UI02', 'UI03', 'UI04', 'UI05', 'UI06', 'UI07', 'DONE01', 'DONE02', 'DONE03'];
const sourceInventory = JSON.parse(fs.readFileSync(path.join(reportDir, 'implementation-status.json'), 'utf8')).items;
const checks = [], failures = [], pageErrors = [], httpRequests = [], screenshots = [];
let browser;

async function check(name, fn) {
    try { await fn(); checks.push(name); }
    catch (error) { failures.push({ name, message: error.message }); }
}
async function downloadJson(page) {
    const pending = page.waitForEvent('download');
    await page.locator('#ui-review-export').click();
    const download = await pending;
    assert.match(download.suggestedFilename(), /\.json$/i);
    return JSON.parse(fs.readFileSync(await download.path(), 'utf8'));
}
function assertExport(payload, expectedItems) {
    assert.equal(payload.schemaVersion, 1);
    assert.equal(payload.type, 'parkrr-ui-review');
    assert.equal(payload.date, '2026-10-12');
    assert.ok(Array.isArray(payload.items));
    assert.deepEqual(payload.items.map(item => [item.id, item.state]), expectedItems);
    function visit(value) {
        if (!value || typeof value !== 'object') return;
        for (const [key, child] of Object.entries(value)) {
            assert.doesNotMatch(key, /draft|answers/i, 'Export must not include answer drafts');
            visit(child);
        }
    }
    visit(payload);
    assert.doesNotMatch(JSON.stringify(payload), /SYNTHETIC-PRIVATE-ANSWER-DRAFT/);
}

(async () => {
    fs.mkdirSync(outputDir, { recursive: true });
    browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true, serviceWorkers: 'block' });
    await context.route(/^https?:/, route => { httpRequests.push(route.request().url()); return route.abort(); });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.goto(reportUrl);
    await page.locator('#ui-review').waitFor();
    const items = page.locator('#ui-review details.ui-review-item');
    const originalStates = await items.evaluateAll(nodes => nodes.map(node => [node.id.replace(/^ui-/, ''), node.dataset.state]));
    const originalInventory = await page.locator('.current-item').evaluateAll(nodes => nodes.map(node => [node.id.replace(/^current-/, ''), node.dataset.status]));

    await check('Versioned UI inventory: seven implemented corrections and three earlier improvements', async () => {
        assert.deepEqual(originalStates.map(item => item[0]), expectedIds);
        assert.equal(originalStates.filter(item => item[1] === 'open').length, 0);
        assert.equal(originalStates.filter(item => item[1] === 'done').length, 10);
        assert.equal(await items.locator(':scope > summary').count(), 10);
        for (const id of expectedIds.slice(0, 7)) {
            assert.match(await page.locator(`#ui-${id}`).textContent(), /Bereits umgesetzt/);
            assert.equal(await page.locator(`#ui-${id} a[href="evidence/ui-polish-20261012-validation.txt"]`).count(), 1);
            assert.match(await page.locator(`#ui-${id} .ui-review-evidence`).textContent(), /Vorher/);
        }
        assert.equal(await page.locator('#ui-review-status').inputValue(), 'all');
        assert.equal(await page.locator('#ui-review-area').inputValue(), 'all');
        assert.equal(await items.locator('input[type="checkbox"], input[type="radio"], select, textarea, [contenteditable="true"]').count(), 0);
        assert.match(await page.locator('#ui-review-count').textContent(), /10 von 10/);
        assert.ok(await page.locator('#ui-review-count').getAttribute('role') === 'status' || await page.locator('#ui-review-count').getAttribute('aria-live'));
    });
    await check('Confirmed layout decisions remain visible alongside implementation and validation evidence', async () => {
        assert.equal(await page.locator('#ui-UI01').getAttribute('data-state'), 'done');
        assert.equal(await page.locator('#ui-UI05').getAttribute('data-state'), 'done');
        assert.match(await page.locator('#ui-UI01').textContent(), /Status\s*&\s*Zahlung/);
        assert.match(await page.locator('#ui-UI01').textContent(), /bestätigt|entschieden|Entscheidung|gewählt|freigegeben/i);
        assert.match(await page.locator('#ui-UI05').textContent(), /acht|8/);
        assert.match(await page.locator('#ui-UI05').textContent(), /sichtbar/);
        assert.match(await page.locator('#ui-UI05').textContent(), /bestätigt|entschieden|Entscheidung|gewählt|freigegeben/i);
    });
    await check('Status filters are exact and reset restores the complete inventory', async () => {
        for (const [state, count] of [['open', 0], ['done', 10], ['all', 10]]) {
            await page.locator('#ui-review-status').selectOption(state);
            assert.equal(await items.filter({ visible: true }).count(), count);
            if (state !== 'all') assert.ok((await items.filter({ visible: true }).evaluateAll(nodes => nodes.map(node => node.dataset.state))).every(value => value === state));
            assert.equal(await page.locator('#ui-review-empty').isVisible(), count === 0);
            assert.equal(await page.locator('#ui-review-export').isDisabled(), count === 0);
        }
        await page.locator('#ui-review-reset').click();
        assert.equal(await items.filter({ visible: true }).count(), 10);
    });
    await check('Area filters include multiarea rows and combine with status filters', async () => {
        const areaMap = await items.evaluateAll(nodes => nodes.map(node => ({ id: node.id, areas: node.dataset.area.split(/\s+/), state: node.dataset.state })));
        for (const area of ['vehicle', 'invoice', 'navigation', 'tax', 'dashboard', 'portal']) {
            await page.locator('#ui-review-area').selectOption(area);
            assert.deepEqual(await items.filter({ visible: true }).evaluateAll(nodes => nodes.map(node => node.id)), areaMap.filter(item => item.areas.includes(area)).map(item => item.id));
        }
        await page.locator('#ui-review-area').selectOption('vehicle');
        await page.locator('#ui-review-status').selectOption('open');
        assert.deepEqual(await items.filter({ visible: true }).evaluateAll(nodes => nodes.map(node => node.id)), []);
        await page.locator('#ui-review-status').selectOption('done');
        await page.locator('#ui-review-area').selectOption('portal');
        assert.deepEqual(await items.filter({ visible: true }).evaluateAll(nodes => nodes.map(node => node.id)), ['ui-UI06']);
        await page.locator('#ui-review-reset').click();
    });
    await check('Search is case-insensitive; unmatched search exposes an accessible empty state', async () => {
        await page.locator('#ui-review-search').fill('ui02');
        assert.deepEqual(await items.filter({ visible: true }).evaluateAll(nodes => nodes.map(node => node.id)), ['ui-UI02']);
        await page.locator('#ui-review-search').fill('nothing-matches-synthetic-qa');
        assert.equal(await items.filter({ visible: true }).count(), 0);
        assert.equal(await page.locator('#ui-review-empty').isVisible(), true);
        assert.match(await page.locator('#ui-review-count').textContent(), /0/);
        await page.locator('#ui-review-reset').click();
        assert.equal(await page.locator('#ui-review-search').inputValue(), '');
        assert.equal(await page.locator('#ui-review-status').inputValue(), 'all');
        assert.equal(await page.locator('#ui-review-area').inputValue(), 'all');
        assert.equal(await page.locator('#ui-review-empty').isVisible(), false);
        assert.equal(await items.filter({ visible: true }).count(), 10);
    });
    await check('Deep links clear filters, open their item and focus its summary', async () => {
        for (const id of ['UI01', 'DONE01']) {
            await page.locator('#ui-review-status').selectOption('open');
            await page.locator('#ui-review-area').selectOption('tax');
            await page.locator('#ui-review-search').fill('hidden-target');
            await page.evaluate(id => { location.hash = 'ui-' + id; }, id);
            const target = page.locator(`#ui-${id}`);
            await target.waitFor({ state: 'visible' });
            assert.equal(await target.evaluate(node => node.open), true);
            assert.equal(await page.locator('#ui-review-status').inputValue(), 'all');
            assert.equal(await page.locator('#ui-review-area').inputValue(), 'all');
            assert.equal(await page.locator('#ui-review-search').inputValue(), '');
            assert.equal(await target.locator(':scope > summary').evaluate(node => node === document.activeElement), true);
        }
        await page.goto(reportUrl + '#ui-UI02');
        await page.locator('#ui-UI02[open]').waitFor();
        assert.equal(await page.locator('#ui-UI02 > summary').evaluate(node => node === document.activeElement), true);
    });
    await check('Native item disclosures support keyboard use', async () => {
        const summary = page.locator('#ui-UI03 > summary');
        const before = await summary.evaluate(node => node.parentElement.open);
        await summary.focus();
        await page.keyboard.press('Enter');
        assert.equal(await summary.evaluate(node => node.parentElement.open), !before);
        await page.keyboard.press('Space');
        assert.equal(await summary.evaluate(node => node.parentElement.open), before);
    });
    await check('Downloads export only filtered authoritative statuses and no answer drafts', async () => {
        await page.evaluate(() => localStorage.setItem('parkrr.review.answers.v1', JSON.stringify({ Q01: 'SYNTHETIC-PRIVATE-ANSWER-DRAFT' })));
        await page.locator('#ui-review-reset').click();
        assertExport(await downloadJson(page), originalStates);
        await page.locator('#ui-review-status').selectOption('open');
        await page.locator('#ui-review-area').selectOption('vehicle');
        assert.equal(await page.locator('#ui-review-export').isDisabled(), true);
        await page.locator('#ui-review-status').selectOption('done');
        await page.locator('#ui-review-search').fill('UI01');
        assertExport(await downloadJson(page), [['UI01', 'done']]);
        await page.locator('#ui-review-reset').click();
        await page.locator('#ui-review-search').fill('DONE01');
        assertExport(await downloadJson(page), [['DONE01', 'done']]);
        assert.deepEqual(await items.evaluateAll(nodes => nodes.map(node => [node.id.replace(/^ui-/, ''), node.dataset.state])), originalStates);
        await page.locator('#ui-review-reset').click();
    });
    await check('Screenshot and source links are local, existing files; hash links resolve', async () => {
        const links = await page.locator('#ui-review a[href]').evaluateAll(nodes => nodes.map(node => node.getAttribute('href')));
        const images = await page.locator('#ui-review img[src]').evaluateAll(nodes => nodes.map(node => node.getAttribute('src')));
        let screenshotsFound = 0;
        for (const href of [...links, ...images]) {
            if (href.startsWith('#')) {
                assert.equal(await page.locator(`[id="${decodeURIComponent(href.slice(1))}"]`).count(), 1, href);
                continue;
            }
            const url = new URL(href, reportUrl);
            assert.equal(url.protocol, 'file:', 'Overview evidence must remain offline: ' + href);
            assert.equal(fs.existsSync(fileURLToPath(url)), true, 'Missing local evidence: ' + href);
            if (/\.png$/i.test(url.pathname)) screenshotsFound++;
        }
        assert.ok(screenshotsFound >= 5, 'Expected linked image evidence for the visually observed findings');
    });
    await check('Print temporarily expands and reveals all UI findings, then restores state and filters', async () => {
        await page.locator('#ui-review-status').selectOption('open');
        await page.locator('#ui-review-search').fill('UI02');
        const before = await items.evaluateAll(nodes => nodes.map(node => ({ id: node.id, open: node.open, hidden: node.hidden })));
        await page.evaluate(() => dispatchEvent(new Event('beforeprint')));
        assert.equal(await page.locator('#ui-review details.ui-review-item:not([open])').count(), 0);
        assert.equal(await page.locator('#ui-review details.ui-review-item[hidden]').count(), 0);
        await page.evaluate(() => dispatchEvent(new Event('afterprint')));
        assert.deepEqual(await items.evaluateAll(nodes => nodes.map(node => ({ id: node.id, open: node.open, hidden: node.hidden }))), before);
        assert.equal(await page.locator('#ui-review-status').inputValue(), 'open');
        assert.equal(await page.locator('#ui-review-search').inputValue(), 'UI02');
        await page.locator('#ui-review-reset').click();
    });
    await check('Historical 25-item inventory is unchanged by new overview interactions', async () => {
        assert.equal(originalInventory.length, 25);
        assert.deepEqual(originalInventory, sourceInventory.map(item => [item[0], item[1]]));
        assert.deepEqual(await page.locator('.current-item').evaluateAll(nodes => nodes.map(node => [node.id.replace(/^current-/, ''), node.dataset.status])), originalInventory);
        const overview = await page.locator('#ui-review').textContent();
        assert.match(overview, /Ursprüngliche Impeccable-Kritik[^]*28 von 40/);
        assert.match(overview, /Keine neue Gesamtbewertung/);
        assert.match(overview, /Behoben: Prognose-Rundung/);
        assert.equal(await page.locator('#ui-review a[href="evidence/comprehensive-20261011-forecast-rounding.txt"]').count(), 1);
        assert.match(overview, /Historischer Fehlernachweis vom 11\.10\./);
        assert.match(overview, /Kein Deployment dieser UI-Korrekturen/);
    });

    await page.evaluate(() => { localStorage.clear(); history.replaceState(null, '', location.pathname); });
    await page.reload();
    // One bounded visual batch; functional-only reruns intentionally skip screenshots.
    if (!functionalOnly) for (const [name, width, height, light] of [
        ['desktop-dark', 1440, 1000, false], ['mobile-light', 390, 844, true], ['narrow-dark', 320, 800, false]
    ]) await check(name + ': responsive reflow and scoped accessibility', async () => {
        await page.setViewportSize({ width, height });
        await page.evaluate(light => document.documentElement.classList.toggle('light', light), light);
        await page.locator('#ui-review-reset').click();
        await items.evaluateAll(nodes => nodes.forEach(node => { node.open = node.id === 'ui-UI01'; }));
        await page.locator('#ui-review').scrollIntoViewIfNeeded();
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Horizontal page overflow');
        const results = await new AxeBuilder({ page }).include('#ui-review').analyze();
        const violations = results.violations.filter(violation => ['serious', 'critical'].includes(violation.impact));
        assert.deepEqual(violations.map(violation => ({ id: violation.id, nodes: violation.nodes.map(node => node.target) })), []);
        const screenshot = name + '.png';
        await page.locator('#ui-review').screenshot({ path: path.join(outputDir, screenshot) });
        screenshots.push(screenshot);
    });
    await check('200% text at mobile width preserves overview reflow', async () => {
        await page.setViewportSize({ width: 390, height: 844 });
        await page.evaluate(() => { document.documentElement.style.fontSize = '32px'; });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Text zoom horizontal overflow');
        await page.evaluate(() => { document.documentElement.style.fontSize = ''; });
    });
    await check('No JavaScript errors, HTTP requests or duplicate document IDs', async () => {
        assert.deepEqual(pageErrors, []);
        assert.deepEqual(httpRequests, []);
        assert.deepEqual(await page.locator('[id]').evaluateAll(nodes => { const ids = nodes.map(node => node.id); return ids.filter((id, i) => ids.indexOf(id) !== i); }), []);
    });
    await browser.close();
    browser = null;
    const result = {
        checkedAt: new Date().toISOString(),
        scope: 'Offline interactive UI review only; Chromium file://; no Parkrr, Docker, financial data or production access',
        mode: functionalOnly ? 'functional-only; no visual batch repeated' : 'functional and single three-viewport visual batch',
        checks, failures, screenshots, httpRequests
    };
    fs.writeFileSync(path.join(outputDir, functionalOnly ? 'functional-checks.json' : 'checks.json'), JSON.stringify(result, null, 2) + '\n');
    console.log(JSON.stringify(result, null, 2));
    if (failures.length) process.exitCode = 1;
})().catch(async error => {
    console.error(error);
    if (browser) await browser.close();
    process.exitCode = 1;
});
