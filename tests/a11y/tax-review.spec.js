const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const path = require('node:path');
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');

test.use({ serviceWorkers: 'block' });
const reply = {
  preview: true, persisted: false, year: 2025,
  turnover: { status: 'within_tolerance', verified: false, notes: ['Das Folgejahr gesondert prüfen.'] },
  year_shift: { status: 'candidate', verified: false, suggested_year: 2025, notes: ['Fälligkeit fachlich bestätigen.'] },
};

async function openReview(page, handler) {
  const requests = await mockUI(page, { role: 'admin', overrides: {
    ...overrides, '/reports/tax-review/preview': handler || reply,
  } });
  await page.goto(origin + '/#/taxyear');
  const review = page.locator('.tax-review');
  await expect(review).not.toHaveAttribute('open', '');
  await review.locator('summary').first().click();
  return { review, requests };
}

async function fillTurnover(page) {
  await page.getByLabel('Maßgeblicher Umsatz 2025 (EUR)', { exact: false }).fill('55000,01');
  await page.getByLabel('Maßgeblicher Umsatz 2024 (EUR)', { exact: false }).fill('0');
  await page.getByRole('combobox', { name: 'Umsatzsteuer-Option', exact: true }).selectOption('false');
  await page.getByLabel('Quelle der Umsatzwerte').fill('Synthetischer Testfall');
  await page.getByLabel('Prüferangabe zu den Umsatzwerten').fill('Test');
}

for (const width of [320, 390, 1440]) {
  test(`optional tax preview is accessible at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const { review, requests } = await openReview(page);
    await fillTurnover(page);
    const form = page.getByRole('form', { name: 'Jahresumsätze vergleichen' });
    await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
    await expect(review.getByText('Angegebener Jahresumsatz im Toleranzbereich')).toBeVisible();
    await review.getByText('Einzelnen Jahreswechsel prüfen', { exact: true }).click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
      const result = await new AxeBuilder({ page }).include('.tax-review').withTags(['wcag22a', 'wcag22aa']).analyze();
      expect(result.violations.filter(v => ['serious', 'critical'].includes(v.impact))).toEqual([]);
      if (process.env.PARKRR_CAPTURE_DIR) {
        // Hide fixed chrome only for the component capture; layout/axe use the real UI.
        await review.screenshot({ path: path.join(process.env.PARKRR_CAPTURE_DIR, `tax-review-${theme}-${width}.png`),
          style: '.app-header, .tabbar { visibility: hidden !important; }' });
      }
    }
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.fontSize = '32px');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
    expect(requests.filter(r => r.method !== 'GET').map(r => r.path)).toEqual(['/reports/tax-review/preview']);
    expect(errors).toEqual([]);
  });
}

test('preview keeps unknown values distinct from zero and invalidates changed results', async ({ page }) => {
  const payloads = [];
  await openReview(page, async route => {
    payloads.push(route.request().postDataJSON());
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(reply) });
  });
  const form = page.getByRole('form', { name: 'Jahresumsätze vergleichen' });
  await page.getByLabel('Quelle der Umsatzwerte').fill('Test');
  await page.getByLabel('Prüferangabe zu den Umsatzwerten').fill('Test');
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await expect(form.getByRole('button', { name: 'Prüfentwurf herunterladen' })).toBeEnabled();
  expect(payloads[0].turnover).not.toHaveProperty('current_eur');
  expect(payloads[0].turnover).not.toHaveProperty('previous_eur');
  expect(payloads[0].turnover).not.toHaveProperty('opted_out');
  await fillTurnover(page);
  await expect(form.getByRole('button', { name: 'Prüfentwurf herunterladen' })).toBeDisabled();
  await expect(form.getByRole('status', { includeHidden: true })).toBeEmpty();
  await expect(form.getByRole('status', { includeHidden: true })).toBeHidden();
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await expect(form.getByRole('button', { name: 'Prüfentwurf herunterladen' })).toBeEnabled();
  expect(payloads[1].turnover).toMatchObject({ current_eur: '55000.01', previous_eur: '0', opted_out: false });
  const downloaded = page.waitForEvent('download');
  await form.getByRole('button', { name: 'Prüfentwurf herunterladen' }).click();
  expect((await downloaded).suggestedFilename()).toBe('parkrr-pruefentwurf-2025-turnover.json');
});

test('server failure preserves input and permits retry without writes', async ({ page }) => {
  let count = 0;
  await openReview(page, async route => {
    count++;
    await route.fulfill({ status: count === 1 ? 400 : 200, contentType: 'application/json',
      body: JSON.stringify(count === 1 ? { error: 'Quelle prüfen.' } : reply) });
  });
  await fillTurnover(page);
  const form = page.getByRole('form', { name: 'Jahresumsätze vergleichen' });
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await expect(form.getByRole('alert')).toContainText('Quelle prüfen. Deine Eingaben bleiben erhalten.');
  await expect(page.getByLabel('Maßgeblicher Umsatz 2025 (EUR)', { exact: false })).toHaveValue('55000,01');
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await expect(form.getByRole('status')).toContainText('nicht gespeichert');
});

test('a late response cannot validate an edited draft', async ({ page }) => {
  let release, started;
  const pending = new Promise(resolve => { release = resolve; });
  const received = new Promise(resolve => { started = resolve; });
  await openReview(page, async route => {
    started();
    await pending;
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(reply) });
  });
  await fillTurnover(page);
  const form = page.getByRole('form', { name: 'Jahresumsätze vergleichen' });
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await received;
  await page.getByLabel('Maßgeblicher Umsatz 2025 (EUR)', { exact: false }).fill('60000');
  release();
  await expect(form.getByRole('button', { name: 'Vorschau berechnen' })).toBeEnabled();
  await expect(form.getByRole('button', { name: 'Prüfentwurf herunterladen' })).toBeDisabled();
  await expect(form.getByRole('status', { includeHidden: true })).toBeEmpty();
});

test('income and expense date scenarios are explicit and do not alter year totals', async ({ page }) => {
  let payload;
  const { review, requests } = await openReview(page, async route => {
    payload = route.request().postDataJSON();
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify(reply) });
  });
  await review.getByText('Einzelnen Jahreswechsel prüfen', { exact: true }).click();
  const form = page.getByRole('form', { name: 'Jahreswechsel prüfen', exact: true });
  await form.getByRole('combobox', { name: 'Vorgangsart', exact: true }).selectOption('expense');
  await form.getByLabel('Tatsächliches Zahlungsdatum').fill('2026-01-10');
  await form.getByLabel('Vertragliche Fälligkeit').fill('2025-12-31');
  await form.getByRole('combobox', { name: 'Regelmäßig wiederkehrend', exact: true }).selectOption('true');
  await form.getByLabel('Quelle der Fälligkeit').fill('Vertrag (Test)');
  await form.getByLabel('Prüferangabe zur Fälligkeit').fill('Test');
  await form.getByRole('button', { name: 'Vorschau berechnen' }).click();
  await expect(form.getByRole('status')).toContainText('Vorgeschlagenes Jahr: 2025');
  expect(payload).toMatchObject({ year: 2025, year_shift: { kind: 'expense', economic_year: 2025,
    paid_on: '2026-01-10', due_on: '2025-12-31', regularly_recurring: true } });
  await expect(page.getByText('Überschuss 2025', { exact: true })).toBeVisible();
  expect(requests.filter(r => r.method !== 'GET').map(r => r.path)).toEqual(['/reports/tax-review/preview']);
});

test('unsupported year does not offer an invented ruleset', async ({ page }) => {
  await mockUI(page, { role: 'admin', overrides: { ...overrides,
    '/reports/tax-year': { ...overrides['/reports/tax-year'], year: 2024 } } });
  await page.goto(origin + '/#/taxyear');
  await page.locator('.tax-review summary').click();
  await expect(page.locator('.tax-review')).toContainText('Für dieses Jahr ist kein Regelstand für die Vorschau hinterlegt');
  await expect(page.locator('.tax-review form')).toHaveCount(0);
});
