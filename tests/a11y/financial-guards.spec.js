const { test, expect } = require('@playwright/test');
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');

// Mock-only regression: every request is intercepted; no operator data or
// running Parkrr instance is read or changed by these financial-error tests.
test.use({ serviceWorkers: 'block' });
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.clock.setFixedTime(new Date('2026-10-08T12:00:00Z'));
});

const historyError = 'Zu diesem Zeitraum besteht bereits Zahlungshistorie. Betrag, Zahlungsstatus oder Quelle können derzeit nicht zurückgesetzt oder ersetzt werden. Die bestehende Zahlung bleibt unverändert.';
const writes = requests => requests.filter(request => request.method !== 'GET')
  .map(({ path, method }) => ({ path, method }));

async function expectConflict(page, message) {
  await expect(page.locator('#toast')).toBeVisible();
  await expect(page.locator('#toast')).toHaveClass(/\berror\b/);
  await expect(page.locator('#toast')).toHaveText(message);
  await expect(page.locator('#toast.success')).toHaveCount(0);
}

test('R24 tariff conflict preserves edited inputs and the original displayed price', async ({ page }) => {
  const message = 'Tarifpreis nicht geändert: 1 bestehende Buchung(en) beziehen ihren Preis noch aus diesem Tarif. Bestätige deren vereinbarten Preis einzeln oder lege einen neuen Tarif für neue Buchungen an.';
  const payloads = [];
  const requests = await mockUI(page, { role: 'admin', overrides: {
    ...overrides,
    '/categories/1': async route => {
      payloads.push(route.request().postDataJSON());
      await route.fulfill({ status: 409, json: { error: message } });
    },
  } });
  await page.goto(origin + '/#/tariffs');
  const head = page.locator('.cfg-head').first();
  await head.click();
  await page.getByLabel('Name', { exact: true }).fill('Wohnmobil neuer Tarifname');
  await page.getByLabel('Preis / Monat (€)', { exact: true }).fill('90');
  await page.getByLabel('Preis / Jahr (€)', { exact: true }).fill('1080');
  const save = page.getByRole('button', { name: 'Speichern', exact: true });
  await save.click();

  await expectConflict(page, message);
  await expect(save).toBeEnabled();
  await expect(head).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Wohnmobil neuer Tarifname');
  await expect(page.getByLabel('Preis / Monat (€)', { exact: true })).toHaveValue('90');
  await expect(page.getByLabel('Preis / Jahr (€)', { exact: true })).toHaveValue('1080');
  await expect(head.locator('.cfg-title')).toHaveText('Wohnmobil');
  await expect(head.locator('.cfg-rate-m')).toContainText(/85,00\s*€/);
  expect(payloads).toHaveLength(1);
  expect(payloads[0]).toMatchObject({ name: 'Wohnmobil neuer Tarifname', default_monthly_cost: 90, default_yearly_cost: 1080 });
  expect(writes(requests)).toEqual([{ path: '/categories/1', method: 'PUT' }]);
});

test('R03 agreement period conflict restores the paid radio and preserves its partial amount', async ({ page }) => {
  const payloads = [];
  const agreement = { id: 7, person_id: 1, amount: 30, accrued: 30, period: 'monthly',
    start_date: '2026-09-01', paid: false, paid_periods: [], paid_fixed: { '2026-09': 10 },
    period_costs: { '2026-09': 30 }, vehicle_ids: [] };
  const requests = await mockUI(page, { role: 'admin', overrides: {
    ...overrides, '/vehicles': [],
    '/persons/1/stats': { ...overrides['/persons/1/stats'], agreements: [agreement], recurring_charges: [] },
    '/agreements/7/period-paid': async route => {
      payloads.push(route.request().postDataJSON());
      await route.fulfill({ status: 409, json: { error: historyError } });
    },
  } });
  await page.goto(origin + '/#/persons/1');
  await page.getByText('Zahlungen verwalten (1)', { exact: true }).click();
  const status = page.getByRole('radiogroup', { name: 'Zahlstatus 2026-09', exact: true });
  await expect(status.getByRole('radio', { name: 'bezahlt', exact: true })).toHaveAttribute('aria-checked', 'true');
  await status.getByRole('radio', { name: 'offen', exact: true }).click();

  await expectConflict(page, historyError);
  await expect(status.getByRole('radio', { name: 'bezahlt', exact: true })).toHaveAttribute('aria-checked', 'true');
  await expect(status.getByRole('radio', { name: 'offen', exact: true })).toHaveAttribute('aria-checked', 'false');
  await expect(page.locator('.period-row')).toContainText(/Teil 10,00\s*€/);
  await expect(page.locator('.period-row')).toContainText(/30,00\s*€/);
  expect(payloads).toEqual([{ period_key: '2026-09', paid: false }]);
  expect(writes(requests)).toEqual([{ path: '/agreements/7/period-paid', method: 'POST' }]);
});

test('R03 recurring master conflict restores the paid state without changing its amount', async ({ page }) => {
  const payloads = [];
  const recurring = { id: 8, person_id: 1, description: 'Versicherung Historientest', amount: 30,
    accrued: 30, period: 'monthly', start_date: '2026-09-01', paid: true,
    paid_periods: ['2026-09'], paid_fixed: {}, period_costs: { '2026-09': 30 } };
  const requests = await mockUI(page, { role: 'admin', overrides: {
    ...overrides, '/vehicles': [],
    '/persons/1/stats': { ...overrides['/persons/1/stats'], agreements: [], recurring_charges: [recurring] },
    '/recurring/8/paid': async route => {
      payloads.push(route.request().postDataJSON());
      await route.fulfill({ status: 409, json: { error: historyError } });
    },
  } });
  await page.goto(origin + '/#/persons/1');
  const card = page.locator('.card').filter({ has: page.getByText(recurring.description, { exact: true }) });
  const status = card.getByRole('radiogroup', { name: 'Zahlstatus wiederkehrende Kosten', exact: true });
  await expect(status.getByRole('radio', { name: 'bezahlt', exact: true })).toHaveAttribute('aria-checked', 'true');
  await status.getByRole('radio', { name: 'offen', exact: true }).click();

  await expectConflict(page, historyError);
  await expect(status.getByRole('radio', { name: 'bezahlt', exact: true })).toHaveAttribute('aria-checked', 'true');
  await expect(status.getByRole('radio', { name: 'offen', exact: true })).toHaveAttribute('aria-checked', 'false');
  await expect(card.locator('.badge')).toHaveText('Bezahlt');
  await expect(card.locator('.ag-accrued')).toContainText(/30,00\s*€/);
  await expect(card.locator('.card-meta')).toContainText(/30,00\s*€\/Monat/);
  expect(payloads).toEqual([{ paid: false }]);
  expect(writes(requests)).toEqual([{ path: '/recurring/8/paid', method: 'POST' }]);
});
