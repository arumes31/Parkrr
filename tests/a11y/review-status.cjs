'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { chromium } = require('playwright');
const AxeBuilder = require('@axe-core/playwright').default;

const root = path.resolve(__dirname, '../..');
const reportDir = path.join(root, 'docs/anwendungsreview');
const reportUrl = pathToFileURL(path.join(reportDir, 'index.html')).href;
const output = path.join(reportDir, 'evidence');
const followup = JSON.parse(fs.readFileSync(path.join(reportDir, 'review-followup.json'), 'utf8'));
// Keep the previous report's evidence intact when verifying this follow-up.
const evidencePrefix = 'implementation-completion';
const answerKey = 'parkrr.review.answers.v1';
// Allows a test-harness-only retry without repeating the bounded visual QA pass.
const functionalOnly = process.argv.includes('--functional-only');
const checks = [], failures = [], pageErrors = [], httpRequests = [];
let browser;
async function check(name, fn) {
    try { await fn(); checks.push(name); }
    catch (error) { failures.push(name + ': ' + error.message); }
}
async function downloadText(download) {
    return fs.readFileSync(await download.path(), 'utf8');
}
async function importAnswers(page, value) {
    await page.locator('#answers-file').setInputFiles({ name: 'answers.json', mimeType: 'application/json', buffer: Buffer.from(typeof value === 'string' ? value : JSON.stringify(value)) });
}

(async () => {
    browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true, serviceWorkers: 'block' });
    await context.route(/^https?:/, route => { httpRequests.push(route.request().url()); return route.abort(); });
    const page = await context.newPage();
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.goto(reportUrl);
    await page.locator('#progress-title').waitFor();
    const originalStatuses = await page.locator('.current-item').evaluateAll(nodes => nodes.map(n => [n.id, n.dataset.status]));
    await check('Current inventory: 25 measures, 16 implemented / 6 partial / 3 open', async () => {
        assert.equal(originalStatuses.length, 25);
        for (const [status, n] of [['Umgesetzt', 16], ['Teilweise', 6], ['Offen', 3]]) assert.equal(originalStatuses.filter(i => i[1] === status).length, n);
        assert.equal(await page.locator('.current-item:visible').count(), 9);
        assert.equal(await page.locator('#historical-audit').getAttribute('open'), null);
        assert.equal(await page.locator('h1').count(), 1);
    });
    await check('Received answers and recommendations are distinct from unshared drafts', async () => {
        assert.match(await page.locator('#answer-state-Q03').textContent(), /Aktuell geklärt/);
        assert.match(await page.locator('#answer-state-Q04').textContent(), /Bisher kein Bedarf/);
        assert.match(await page.locator('#answer-state-Q01').textContent(), /Testprofil geklärt/);
        assert.match(await page.locator('#answer-state-Q02').textContent(), /Leistungsumfang geklärt/);
        assert.match(await page.locator('#answer-state-Q05').textContent(), /Preisregel freigegeben/);
        assert.match(await page.locator('#answer-state-Q06').textContent(), /Optionaler Lesemodus gewählt/);
        assert.match(await page.locator('#question-Q03 .received-answer').textContent(), /ein steuerobjekt aktuell/);
        assert.match(await page.locator('#question-Q05 .received-answer').textContent(), /lokal implementiert/);
        assert.equal(await page.locator('#answer-Q03').inputValue(), '');
        assert.match(await page.locator('#answer-count').textContent(), /Ergänzungsentwurf/);
        assert.match(await page.locator('#current-R08').textContent(), /Exportformat 3/);
        assert.match(await page.locator('#current-R08').textContent(), /gesamte Paket/);
        assert.match(await page.locator('#current-R08').textContent(), /24 MiB/);
        assert.match(await page.locator('#current-R03').textContent(), /HTTP 409/);
        assert.doesNotMatch(await page.locator('#current-R03').textContent(), /Lösch-\/Ersetzpfad bestehen/);
        assert.match(await page.locator('#current-R05').textContent(), /2025\/2026/);
        assert.match(await page.locator('#current-R10').textContent(), /keine Speicherung/);
    });
    await check('Test profile stays unverified; completed tariff rule stays linked to its decision', async () => {
        assert.equal(await page.locator('#profile-summary').textContent(), followup.profileSummary);
        const profile = await page.locator('#profile-summary').textContent();
        for (const fact of [/Testbetrieb/, /eigenen Namen/, /späteren Echtbetrieb vorgesehen/, /2025/, /Strom als Pauschale/]) assert.match(profile, fact);
        const q01 = await page.locator('#question-Q01 .received-answer').textContent();
        assert.match(q01, /weder ein bestätigter aktueller Umsatzsteuerstatus noch ein tatsächlicher Vermietungsbeginn 2025/);
        assert.match(await page.locator('#question-Q02 .received-answer').textContent(), /auch nicht null Euro/);
        assert.equal(await page.locator('.answer-history').count(), 4);
        assert.equal(await page.locator('.answer-history[open]').count(), 0);
        assert.equal(await page.locator('#current-R24').getAttribute('data-work'), '');
        assert.equal(await page.locator('#current-R24').getAttribute('data-status'), 'Umgesetzt');
        assert.match(await page.locator('#current-R24').textContent(), /Entscheidung und Grenzen/);
        assert.doesNotMatch(await page.locator('#current-R24').textContent(), /Was noch fehlt/);
        assert.match(await page.locator('#question-Q05 .received-answer').textContent(), /auch bei künftigem Mietbeginn/);
        assert.doesNotMatch(await page.locator('#question-Q05 .received-answer').textContent(), /noch nicht freigegeben/);
        assert.doesNotMatch(await page.locator('#question-Q06').textContent(), /Empfehlung · noch nicht freigegeben/);
        assert.match(await page.locator('#optional-tax').textContent(), /PARKRR_TAX_WRITES_ENABLED=true/);
        assert.match(await page.locator('#optional-tax').textContent(), /direkte API-Aufrufe/);
        assert.match(await page.locator('#optional-tax').textContent(), /fehlen weiterhin/);
    });
    await check('Open work groups cover all 9 unfinished measures without counting deferred work as implemented', async () => {
        assert.equal(await page.locator('.work-group').count(), 3);
        assert.match(await page.locator('#open-work').textContent(), /7 relevante Restpunkte/);
        const grouped = await page.locator('.work-group a[href^="#current-"]').evaluateAll(nodes => nodes.map(n => n.hash.slice(1)));
        assert.deepEqual([...grouped].sort(), originalStatuses.filter(i => i[1] !== 'Umgesetzt').map(i => i[0]).sort());
        for (const [group, count] of [['technical', 4], ['decision', 3], ['deferred', 2]]) {
            await page.locator('#progress-search').fill('should be cleared');
            await page.locator(`#work-${group} summary`).click();
            await page.locator(`[data-work-filter="${group}"]`).click();
            assert.equal(await page.locator('.current-item:visible').count(), count);
            assert.equal(await page.locator('#progress-search').inputValue(), '');
            assert.equal(await page.locator('#measures-title').evaluate(n => n === document.activeElement), true);
            const work = await page.locator('.current-item:visible').evaluateAll(nodes => nodes.map(n => n.dataset.work));
            assert.ok(work.every(value => value === group));
            await page.locator(`#work-${group} summary`).click();
        }
        assert.deepEqual(await page.locator('.current-item:visible').evaluateAll(nodes => nodes.map(n => n.id)), ['current-R09', 'current-R11']);
        assert.equal(await page.locator('.current-item:visible[data-status="Offen"]').count(), 2);
        await page.locator('#progress-reset').click();
        assert.equal(await page.locator('.current-item:visible').count(), 25);
    });
    await check('Status filters, search, empty state and reset', async () => {
        for (const [filter, count] of [['Umgesetzt', 16], ['Teilweise', 6], ['Offen', 3], ['unfinished', 9], ['all', 25]]) {
            await page.locator(`[data-progress-filter="${filter}"]`).click();
            assert.equal(await page.locator('.current-item:visible').count(), count);
            assert.equal(await page.locator('[data-progress-filter][aria-pressed="true"]').count(), 1);
        }
        await page.locator('#progress-search').fill('R24');
        assert.equal(await page.locator('.current-item:visible').count(), 1);
        await page.locator('#progress-search').fill('nothing-matches-this');
        assert.equal(await page.locator('#progress-empty').isVisible(), true);
        await page.locator('#progress-reset').click();
        assert.equal(await page.locator('.current-item:visible').count(), 25);
    });
    await check('Question links reveal related measures and hidden implemented items', async () => {
        await page.locator('[data-progress-filter="Offen"]').click();
        assert.equal(await page.locator('#current-R24').isVisible(), false);
        await page.evaluate(() => location.hash = 'question-Q05');
        await page.locator('#question-Q05[open]').waitFor();
        assert.equal(await page.locator('#question-Q05').getAttribute('open'), '');
        await page.locator('#question-Q05 a[href="#current-R24"]').click();
        await page.locator('#current-R24[open]').waitFor();
        assert.equal(await page.locator('#current-R24').getAttribute('open'), '');
        assert.equal(await page.locator('#current-R24').isVisible(), true);
        await page.locator('#current-R24 a[href="#question-Q05"]').click();
        assert.equal(await page.locator('#question-Q05').isVisible(), true);
        await page.evaluate(() => location.hash = 'current-R01');
        await page.locator('#current-R01[open]').waitFor();
        assert.equal(await page.locator('#current-R01').isVisible(), true);
    });
    await check('Keyboard operates native disclosures', async () => {
        await page.locator('#question-Q01 > summary').focus();
        await page.keyboard.press('Enter');
        assert.equal(await page.locator('#answer-Q01').isVisible(), true);
        await page.keyboard.press('Tab');
        assert.equal(await page.locator('#answer-Q01').evaluate(n => n === document.activeElement), true);
    });
    await check('Answers persist separately and never change implementation status', async () => {
        await page.locator('#answer-Q01').fill('Synthetischer QA-Entwurf: 2025 und 2026');
        await page.locator('#answer-Q05').fill('Synthetisch: nur neue Buchungen');
        assert.match(await page.locator('#answer-count').textContent(), /^2 von 6/);
        await page.reload();
        assert.equal(await page.locator('#answer-Q01').inputValue(), 'Synthetischer QA-Entwurf: 2025 und 2026');
        assert.deepEqual(await page.locator('.current-item').evaluateAll(nodes => nodes.map(n => [n.id, n.dataset.status])), originalStatuses);
        assert.match(await page.locator('#answer-state-Q01').textContent(), /Entwurf/);
        assert.match(await page.locator('#answer-state-Q05').textContent(), /Preisregel freigegeben · Entwurf/);
    });
    let exported;
    await check('JSON and text exports contain answer drafts and provenance', async () => {
        const jsonDownload = page.waitForEvent('download');
        await page.locator('#answers-export').click();
        exported = JSON.parse(await downloadText(await jsonDownload));
        assert.equal(exported.answers.length, 6);
        assert.equal(exported.codeStatus.items.length, 25);
        assert.equal(exported.codeStatus.commit, followup.codeCommit);
        assert.deepEqual(exported.receivedAnswers.items, followup.receivedAnswers);
        assert.equal(exported.receivedAnswers.profile, followup.profileSummary);
        assert.equal(exported.answers[0].text, 'Synthetischer QA-Entwurf: 2025 und 2026');
        const textDownload = page.waitForEvent('download');
        await page.locator('#answers-text').click();
        const text = await downloadText(await textDownload);
        assert.match(text, /Q06/);
        assert.match(text, /kein Rechnungsablauf/);
        assert.match(text, /Synthetischer QA-Entwurf/);
        assert.match(text, /Bereits übermittelt: ein steuerobjekt/);
        assert.doesNotMatch(text, /Empfehlung \(nicht freigegeben\)/);
        assert.match(text, /Lesemodus/);
        assert.ok(text.includes('Aktuelles Profil: ' + followup.profileSummary));
        assert.match(text, /Überholte Angabe \(nicht aktuell\)/);
        assert.doesNotMatch(text, /Bereits bekannt: aktuell Kleinunternehmerregelung/);
    });
    await check('Invalid imports are rejected atomically', async () => {
        for (const bad of [
            { ...exported, reportId: 'wrong' },
            { ...exported, answers: [exported.answers[0]] },
            { ...exported, answers: exported.answers.map((q, i) => i === 1 ? exported.answers[0] : q) },
            { ...exported, answers: exported.answers.map((q, i) => i === 0 ? { ...q, text: 'a'.repeat(5001) } : q) },
            '{invalid-json', 'x'.repeat(250001)
        ]) {
            await page.locator('#answers-flash').evaluate(n => n.textContent = '');
            await importAnswers(page, bad);
            await page.waitForFunction(() => document.getElementById('answers-flash').textContent.startsWith('Import abgelehnt:'));
            assert.equal(await page.locator('#answer-Q01').inputValue(), exported.answers[0].text);
        }
    });
    await check('Valid import: cancel preserves answers, confirm imports text safely', async () => {
        const imported = structuredClone(exported);
        imported.answers[1].text = '</textarea><img src=x onerror="window.reportXSS=1">';
        imported.receivedAnswers.items.Q03.text = 'Forged confirmed answer';
        page.once('dialog', dialog => dialog.dismiss());
        await importAnswers(page, imported);
        await page.waitForFunction(() => document.getElementById('answers-file').value === '');
        assert.equal(await page.locator('#answer-Q02').inputValue(), '');
        page.once('dialog', dialog => dialog.accept());
        await importAnswers(page, imported);
        await page.waitForFunction(() => document.getElementById('answers-flash').textContent.startsWith('Antwortentwürfe importiert'));
        assert.equal(await page.locator('#answer-Q02').inputValue(), imported.answers[1].text);
        assert.equal(await page.evaluate(() => window.reportXSS), undefined);
        assert.match(await page.locator('#question-Q03 .received-answer').textContent(), /ein steuerobjekt aktuell/);
        assert.deepEqual(await page.locator('.current-item').evaluateAll(nodes => nodes.map(n => [n.id, n.dataset.status])), originalStatuses);
    });
    await check('Historic notes survive reload; historical deep links open archive', async () => {
        await page.evaluate(() => location.hash = 'R01');
        await page.locator('#R01[open]').waitFor();
        assert.equal(await page.locator('#historical-audit').getAttribute('open'), '');
        await page.locator('#comment-R01').fill('Historische QA-Notiz');
        await page.locator('#decision-R01').selectOption('Zurückgestellt');
        await page.reload();
        assert.equal(await page.locator('#comment-R01').inputValue(), 'Historische QA-Notiz');
        assert.equal(await page.locator('#decision-R01').inputValue(), 'Zurückgestellt');
        assert.equal(await page.locator('#answer-Q01').inputValue(), exported.answers[0].text);
        await page.evaluate(() => location.hash = 'tax');
        await page.locator('#tax').waitFor();
        await page.evaluate(() => location.hash = '%E0%A4%A'); // malformed URI must not throw
    });
    await check('Print expands all details and restores filters and disclosure states', async () => {
        await page.locator('#historical-audit > summary').click();
        await page.locator('[data-progress-filter="Offen"]').click();
        await page.locator('#progress-search').fill('R12');
        const prior = await page.locator('details').evaluateAll(nodes => nodes.map(n => [n.open, n.hidden]));
        await page.evaluate(() => dispatchEvent(new Event('beforeprint')));
        assert.equal(await page.locator('details:not([open])').count(), 0);
        assert.equal(await page.locator('.current-item[hidden]').count(), 0);
        await page.evaluate(() => dispatchEvent(new Event('afterprint')));
        assert.deepEqual(await page.locator('details').evaluateAll(nodes => nodes.map(n => [n.open, n.hidden])), prior);
        assert.equal(await page.locator('.current-item:visible').count(), 1);
    });
    // Visual and accessibility checks use a fresh presentation, without QA answers.
    await page.evaluate(() => { localStorage.clear(); history.replaceState(null, '', location.pathname); });
    await page.reload();
    for (const [name, width, height] of functionalOnly ? [] : [['desktop', 1440, 1000], ['mobile', 390, 844], ['narrow', 320, 800]]) {
        await page.setViewportSize({ width, height });
        await check(name + ': no horizontal page overflow; dark/light accessibility', async () => {
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'initial overflow');
            for (const light of [false, true]) {
                await page.evaluate(value => document.documentElement.classList.toggle('light', value), light);
                const results = await new AxeBuilder({ page }).analyze();
                const serious = results.violations.filter(v => ['serious', 'critical'].includes(v.impact));
                assert.deepEqual(serious.map(v => ({ id: v.id, nodes: v.nodes.map(n => n.target) })), []);
            }
            await page.evaluate(() => document.documentElement.classList.remove('light'));
            await page.screenshot({ path: path.join(output, `${evidencePrefix}-report-${name}.png`) });
            await page.locator('#optional-tax > details > summary').click();
            await page.locator('#optional-tax').scrollIntoViewIfNeeded();
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'activation instructions overflow');
            await page.screenshot({ path: path.join(output, `${evidencePrefix}-mode-${name}.png`) });
            await page.locator('#optional-tax > details > summary').click();
            await page.locator('#work-technical summary').click();
            await page.locator('#open-work').scrollIntoViewIfNeeded();
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'open work overflow');
            await page.screenshot({ path: path.join(output, `${evidencePrefix}-overview-${name}.png`) });
            await page.locator('#work-technical summary').click();
            await page.locator('#question-Q01 > summary').click();
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'expanded question overflow');
            await page.locator('#question-Q01').scrollIntoViewIfNeeded();
            await page.screenshot({ path: path.join(output, `${evidencePrefix}-questions-${name}.png`) });
            await page.locator('#question-Q01 > summary').click();
            await page.locator('#question-Q05 > summary').click();
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'recommendation overflow');
            await page.locator('#question-Q05').scrollIntoViewIfNeeded();
            await page.screenshot({ path: path.join(output, `${evidencePrefix}-approved-rule-${name}.png`) });
            await page.locator('#question-Q05 > summary').click();
            await page.evaluate(() => scrollTo(0, 0));
        });
    }
    if (!functionalOnly) await check('200% text size at mobile width preserves page reflow', async () => {
        await page.setViewportSize({ width: 390, height: 844 });
        await page.evaluate(() => document.documentElement.style.fontSize = '32px');
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        await page.evaluate(() => document.documentElement.style.fontSize = '');
    });
    await check('All local hash links resolve; no duplicate IDs or template markers', async () => {
        const errors = await page.evaluate(() => {
            const ids = [...document.querySelectorAll('[id]')].map(n => n.id);
            return {
                duplicate: ids.filter((id, i) => ids.indexOf(id) !== i),
                missing: [...document.querySelectorAll('a[href^="#"]')].map(a => a.hash.slice(1)).filter(id => !document.getElementById(id)),
                marker: /__(?:PROGRESS|IMPLEMENTATION|REPORT_DATA|FONT|BEFORE_TAX|CODE_COMMIT|REVIEW_DATE)[A-Z_]*__/.test(document.documentElement.innerHTML)
            };
        });
        assert.deepEqual(errors, { duplicate: [], missing: [], marker: false });
    });
    for (const mode of ['blocked', 'corrupt', 'quota']) {
        await check(mode + ' storage retains usable in-memory answers and export', async () => {
            const isolated = await browser.newContext({ acceptDownloads: true });
            await isolated.route(/^https?:/, route => { httpRequests.push(route.request().url()); return route.abort(); });
            await isolated.addInitScript(({ mode, key }) => {
                if (mode === 'blocked') Object.defineProperty(window, 'localStorage', { get() { throw Error('blocked'); } });
                if (mode === 'corrupt') localStorage.setItem(key, '{bad-json');
                if (mode === 'quota') Storage.prototype.setItem = function () { throw Error('quota'); };
            }, { mode, key: answerKey });
            const p = await isolated.newPage();
            p.on('pageerror', e => pageErrors.push(e.message));
            await p.goto(reportUrl + '#question-Q01');
            await p.locator('#answer-Q01').fill('Only this page');
            assert.equal(await p.locator('#answers-storage').isVisible(), true);
            const pending = p.waitForEvent('download');
            await p.locator('#answers-export').click();
            assert.equal(JSON.parse(await downloadText(await pending)).answers[0].text, 'Only this page');
            if (mode === 'corrupt') assert.equal(await p.evaluate(key => localStorage.getItem(key), answerKey), '{bad-json');
            await isolated.close();
        });
    }
    await check('No JavaScript errors or HTTP requests', async () => { assert.deepEqual(pageErrors, []); assert.deepEqual(httpRequests, []); });
    await browser.close();
    browser = null;
    const result = { checkedAt: new Date().toISOString(), scope: 'Offline status report only; Chromium file://, synthetic answer drafts, no Parkrr or Docker access', mode: functionalOnly ? 'functional-only; visual QA not repeated' : 'full', checks, failures };
    fs.writeFileSync(path.join(output, `${evidencePrefix}-${functionalOnly ? 'functional-checks' : 'checks'}.json`), JSON.stringify(result, null, 2) + '\n');
    console.log(JSON.stringify(result, null, 2));
    if (failures.length) process.exitCode = 1;
})().catch(async error => { console.error(error); if (browser) await browser.close(); process.exitCode = 1; });
