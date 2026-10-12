const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const path = require('node:path');
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');

test.use({ serviceWorkers: 'block' });
const report = overrides['/reports/tax-year'];
const activeOverrides = { ...overrides, '/reports/tax-year': { ...report, write_enabled: true } };

for (const width of [320, 390, 1440]) {
  test(`tax-year workspace is usable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await mockUI(page, { role: 'admin', overrides });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));

    await page.goto(origin + '/#/taxyear');
    await expect(page.locator('#page')).toHaveAttribute('aria-busy', 'false');
    await expect(page.locator('.route-view h2').getByText('Steuerjahr', { exact: true })).toBeVisible();
    await expect(page.getByText('Überschuss 2025')).toBeVisible();
    await expect(page.getByRole('link', { name: /Komplettes Exportpaket/ })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Kleinunternehmer · Umsatzprüfung' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Steuerbereich · Nur lesen' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Jahr abschließen' })).toBeDisabled();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(errors).toEqual([]);

    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
      const results = await new AxeBuilder({ page }).include('.route-view').withTags(['wcag22a', 'wcag22aa']).analyze();
      expect(results.violations.filter(v => ['serious', 'critical'].includes(v.impact))).toEqual([]);
      if (process.env.PARKRR_CAPTURE_DIR) {
        await page.screenshot({ path: path.join(process.env.PARKRR_CAPTURE_DIR, `tax-readonly-${theme}-${width}.png`), fullPage: true });
      }
    }
    await page.getByText('Später aktivieren', { exact: true }).click();
    await expect(page.locator('.tax-mode code')).toHaveText('PARKRR_TAX_WRITES_ENABLED=true');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.fontSize = '32px');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
  });
}

test('tax-year entry workflows expose their required controls', async ({ page }) => {
  await mockUI(page, { role: 'admin', overrides: activeOverrides });
  await page.goto(origin + '/#/taxyear');

  await page.getByText('+ Ausgabe erfassen', { exact: true }).click();
  await expect(page.getByPlaceholder('z. B. Wartung Hallentor')).toBeVisible();
  await expect(page.getByLabel('Beleg (PDF/JPEG/PNG)', { exact: true })).toBeVisible();

  await page.getByText(/Wiederkehrende Ausgaben/).click();
  await expect(page.getByRole('button', { name: 'Vorlage anlegen' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Deaktivieren' })).toBeVisible();

  await page.getByText(/Anlageverzeichnis/).click();
  await expect(page.getByLabel('Nutzungsdauer (Jahre)', { exact: true })).toBeVisible();
  await expect(page.getByText('Halbjahresregel anwenden', { exact: true })).toBeVisible();

  await expect(page.getByText('Kennzahl 9460 · Einnahmen').first()).toBeVisible();
  await page.getByText(/Steuerobjekte verwalten/).click();
  await page.getByRole('button', { name: 'Bearbeiten' }).first().click();
  await expect(page.getByRole('button', { name: 'Steuerobjekt speichern' })).toBeVisible();
  await expect(page.getByLabel('Einheitswert-Aktenzeichen')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Jahr abschließen' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Jahr abschließen' })).toBeEnabled();
  await expect(page.getByRole('heading', { name: 'Steuerbereich · Bearbeitung aktiviert' })).toBeVisible();
  await page.getByText('Zurück zum Lesemodus', { exact: true }).click();
  await expect(page.locator('.tax-mode code')).toHaveText('PARKRR_TAX_WRITES_ENABLED=false');
});

test('expense and receipt retries reuse the original expense', async ({ page }) => {
  const expenseKeys = [];
  let receiptUploads = 0;
  await mockUI(page, { role: 'admin', overrides: {
    ...activeOverrides,
    '/tax/expenses': async route => {
      expenseKeys.push(route.request().headers()['idempotency-key']);
      await route.fulfill({
        status: expenseKeys.length === 1 ? 503 : 201,
        contentType: 'application/json',
        body: JSON.stringify(expenseKeys.length === 1 ? { error: 'Antwort verloren' } : { id: 99 }),
      });
    },
    '/tax/expenses/99/receipts': async route => {
      receiptUploads++;
      await route.fulfill({
        status: receiptUploads === 1 ? 500 : 201,
        contentType: 'application/json',
        body: JSON.stringify(receiptUploads === 1 ? { error: 'Upload fehlgeschlagen' } : { id: 7 }),
      });
    },
  } });
  await page.goto(origin + '/#/taxyear');

  const entry = page.locator('details').filter({ hasText: '+ Ausgabe erfassen' });
  await entry.locator('summary').click();
  await entry.getByPlaceholder('z. B. Wartung Hallentor').fill('Retry-Test');
  await entry.getByLabel('Betrag brutto', { exact: true }).fill('10');
  await entry.getByLabel('Beleg (PDF/JPEG/PNG)', { exact: true }).setInputFiles({
    name: 'rechnung.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4\n%%EOF\n'),
  });

  await entry.getByRole('button', { name: 'Ausgabe buchen' }).click();
  await expect(entry.getByRole('button', { name: 'Ausgabe buchen' })).toBeEnabled();
  expect(expenseKeys).toHaveLength(1);
  expect(expenseKeys[0]).toBeTruthy();

  await entry.getByRole('button', { name: 'Ausgabe buchen' }).click();
  await expect(entry.getByRole('button', { name: 'Beleg erneut hochladen' })).toBeEnabled();
  expect(expenseKeys).toHaveLength(2);
  expect(expenseKeys[1]).toBe(expenseKeys[0]);
  expect(receiptUploads).toBe(1);

  await entry.getByRole('button', { name: 'Beleg erneut hochladen' }).click();
  await expect.poll(() => receiptUploads).toBe(2);
  expect(expenseKeys).toHaveLength(2);
});

for (const capability of [false, undefined]) {
  test(`read-only ${capability === false ? 'explicit' : 'missing capability'} blocks all writes but permits inspection`, async ({ page }) => {
    const requests = await mockUI(page, { role: 'admin', overrides: {
      ...overrides,
      '/reports/tax-year': { ...report, write_enabled: capability },
      '/tax/expenses/1/receipts': [{ id: 7, filename: 'demo-beleg.pdf' }],
    } });
    await page.goto(origin + '/#/taxyear');
    await expect(page.getByRole('heading', { name: 'Steuerbereich · Nur lesen' })).toBeVisible();
    for (const text of ['+ Ausgabe erfassen', /Wiederkehrende Ausgaben/, /Anlageverzeichnis/, /Steuerobjekte verwalten/, 'Steuereinstellungen', /Zuflussdaten & Objekte prüfen/]) {
      await page.getByText(text, { exact: typeof text === 'string' }).click();
    }
    await expect(page.getByPlaceholder('z. B. Wartung Hallentor')).toBeVisible();
    const controls = page.locator('.tax-form-grid input, .tax-form-grid select, .tax-form-grid button, .tax-form-grid textarea');
    expect(await controls.count()).toBeGreaterThan(20);
    expect(await controls.evaluateAll(nodes => nodes.every(n => n.disabled))).toBe(true);
    for (const name of ['Jahr abschließen', '+ Beleg', 'Stornieren', 'Buchen', 'Deaktivieren', 'Speichern']) {
      await expect(page.getByRole('button', { name, exact: true })).toBeDisabled();
    }
    await page.getByRole('button', { name: 'Details ansehen' }).first().click();
    await expect(page.getByLabel('Lageadresse', { exact: true })).toHaveValue('Musterstraße 12');
    await page.getByRole('button', { name: 'Belege 1' }).click();
    await expect(page.getByRole('link', { name: 'demo-beleg.pdf' })).toHaveAttribute('href', '/api/tax/receipts/7');
    await page.getByText(/15-Tage-Regel/).click();
    await page.getByText('Unbestätigte Jahreswechsel-Zuordnung simulieren', { exact: true }).click();
    await expect(page.getByText('Einnahmen · ungeprüfte Simulation', { exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: /Komplettes Exportpaket/ })).toHaveAttribute('href', /rule=1/);
    // Even a synthetic submit event must not reach the normal save listener.
    await page.locator('.tax-form-grid').evaluateAll(forms => forms.forEach(form => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))));
    expect(requests.filter(r => !['GET', 'HEAD'].includes(r.method))).toEqual([]);
  });
}

for (const enabled of [false, true]) {
  test(`existing year lock stays distinct from write capability ${enabled}`, async ({ page }) => {
    await mockUI(page, { role: 'admin', overrides: {
      ...overrides, '/reports/tax-year': { ...report, write_enabled: enabled, locked: true },
    } });
    await page.goto(origin + '/#/taxyear');
    await expect(page.getByText('Abgeschlossen', { exact: true })).toBeVisible();
    if (enabled) await expect(page.getByRole('button', { name: 'Jahr entsperren' })).toBeEnabled();
    else await expect(page.getByRole('button', { name: 'Jahr entsperren' })).toBeDisabled();
    await expect(page.getByText('+ Ausgabe erfassen', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('link', { name: /Komplettes Exportpaket/ })).toBeVisible();
  });
}
