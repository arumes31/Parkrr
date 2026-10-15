const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const path = require('node:path');
const { mockUI, origin } = require('./helpers/ui-fixture');

// Every request is mocked. These scenarios never read or mutate operator data.
test.use({ serviceWorkers: 'block' });
test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date('2026-10-09T12:00:00Z'));
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

const available = [2022, 2024, 2025, 2026];
const actual = [...Array(9).fill(100), 40, 0, 0];
const remaining = [...Array(9).fill(0), 60, 100, 100];
const reserved = [...Array(10).fill(0), 20, 20];
const forecastReply = {
  year: 2026, current_year: 2026, as_of: '2026-10-09', available_years: available,
  forecast_years: [...available, 2027], has_year_data: true,
  actual_by_month: actual, forecast_by_month: remaining, reservations_by_month: reserved,
  previous_by_month: Array(12).fill(80),
  years: [
    { year: 2022, actual: 0, forecast: 0, reservations: 0, has_data: true },
    { year: 2024, actual: 700, forecast: 0, reservations: 0, has_data: true },
    { year: 2025, actual: 960, forecast: 0, reservations: 0, has_data: true },
    { year: 2026, actual: 940, forecast: 260, reservations: 40, has_data: true },
    { year: 2027, actual: 0, forecast: 1200, reservations: 120, has_data: false },
  ],
  assumptions: ['Offene Mietenden werden im Planungszeitraum fortgeschrieben.', 'Keine neuen Kunden oder unbekannten Zusatzleistungen angenommen.'],
  warnings: ['Synthetischer Datenhinweis: ein Mietende fehlt.'],
};

function overview(year = 2026, changes = {}) {
  return { year, current_year: 2026, available_years: available, has_year_data: true, previous_year_has_data: year === 2026,
    total_persons: 4, active_vehicles: 3, total_vehicles: 5, total_categories: 1,
    outstanding_total: 200, accrued_this_year: year === 2026 ? 940 : year === 2022 ? 0 : 960,
    accrued_prev_year: 800, accrued_prev_full: 960, paid_total: 740, payments_this_year: 740, payments_total: 740,
    revenue_by_month: year === 2026 ? actual : Array(12).fill(year === 2022 ? 0 : 80),
    charges_by_month: Array(12).fill(0), status_counts: { stored: 3, collected: 2 }, top_outstanding: [], ...changes };
}

async function openDashboard(page, { data = overview(), forecastHandler, overviewHandler, role = 'reader' } = {}) {
  const forecastCalls = [], overviewCalls = [];
  const requests = await mockUI(page, { role, overrides: {
    '/overview': async route => {
      const value = new URL(route.request().url()).searchParams.get('year');
      overviewCalls.push(value);
      if (overviewHandler) return overviewHandler(route, value);
      return route.fulfill({ json: value ? overview(Number(value)) : data });
    },
    '/overview/forecast': async route => {
      const year = Number(new URL(route.request().url()).searchParams.get('year'));
      forecastCalls.push(year);
      if (forecastHandler) return forecastHandler(route, year);
      return route.fulfill({ json: year === 2027 ? { ...forecastReply, year,
        actual_by_month: Array(12).fill(0), forecast_by_month: Array(12).fill(100),
        reservations_by_month: Array(12).fill(10), previous_by_month: null } : forecastReply });
    },
  } });
  await page.goto(origin + '/#/dashboard');
  await expect(page.locator('#page')).toHaveAttribute('aria-busy', 'false');
  return { chart: page.locator('.revenue-chart'), requests, forecastCalls, overviewCalls };
}

function expectReadonly(requests) {
  expect(requests.filter(request => request.method !== 'GET')).toEqual([]);
}

async function enableForecast(chart) {
  await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
  await expect(chart.getByRole('definition').filter({ hasText: /1\.200,00/ })).toHaveCount(1);
}

test('dashboard arrows skip gaps and retain a real zero-euro year', async ({ page }) => {
  const { overviewCalls, forecastCalls, requests } = await openDashboard(page);
  const select = page.getByRole('combobox', { name: 'Dashboard-Jahr' });
  await expect(select.locator('option')).toHaveText(['2022', '2024', '2025', '2026']);
  await expect(page.getByRole('button', { name: 'Nächstes Jahr', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Vorheriges Jahr', exact: true }).click();
  await expect(select).toHaveValue('2025');
  await expect(page.getByRole('button', { name: 'Vorheriges Jahr', exact: true })).toBeFocused();
  await page.getByRole('button', { name: 'Vorheriges Jahr', exact: true }).click();
  await expect(select).toHaveValue('2024');
  await page.getByRole('button', { name: 'Vorheriges Jahr', exact: true }).click();
  await expect(select).toHaveValue('2022');
  await expect(page.getByRole('button', { name: 'Vorheriges Jahr', exact: true })).toBeDisabled();
  await expect(select).toBeFocused();
  await expect(page.locator('.dashboard-year-empty')).toHaveCount(0);
  await select.selectOption('2026');
  await expect(select).toHaveValue('2026');
  await expect(select).toBeFocused();
  expect(overviewCalls).toEqual([null, '2025', '2024', '2022', '2026']);
  expect(forecastCalls).toEqual([]);
  expectReadonly(requests);
});

test('the current empty year remains selected and missing prior history is not a growth claim', async ({ page }) => {
  const data = overview(2026, { available_years: [2026], has_year_data: false, previous_year_has_data: false,
    accrued_this_year: 0, revenue_by_month: Array(12).fill(0) });
  const { requests } = await openDashboard(page, { data });
  await expect(page.getByRole('combobox', { name: 'Dashboard-Jahr' })).toHaveValue('2026');
  await expect(page.getByRole('button', { name: 'Vorheriges Jahr', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Nächstes Jahr', exact: true })).toBeDisabled();
  await expect(page.locator('.dashboard-year-empty')).toContainText('noch keine umsatzrelevanten Vorgänge');
  await expect(page.locator('.dash-money .yoy')).toHaveCount(0);
  await expect(page.getByRole('group', { name: 'Vergleichsmodus Umsatz' })).toHaveCount(0);
  expectReadonly(requests);
});

test('a disappeared requested year follows the canonical server year with an explanation', async ({ page }) => {
  const { requests } = await openDashboard(page, { overviewHandler: (route, year) => route.fulfill({
    json: year ? overview(2026, { available_years: [2026] }) : overview(),
  }) });
  await page.getByRole('combobox', { name: 'Dashboard-Jahr' }).selectOption('2022');
  await expect(page.getByRole('combobox', { name: 'Dashboard-Jahr' })).toHaveValue('2026');
  await expect(page.getByText('Für 2022 sind keine auswählbaren Jahresdaten verfügbar. Angezeigt wird 2026.')).toBeVisible();
  expectReadonly(requests);
});

test('existing linear extrapolation is explicitly named and is separate from the chart forecast', async ({ page }) => {
  const { chart, forecastCalls } = await openDashboard(page);
  await expect(page.getByRole('button', { name: 'Lineare Hochrechnung', exact: true })).toBeVisible();
  await expect(chart.getByLabel('Prognose anzeigen', { exact: true })).not.toBeChecked();
  expect(forecastCalls).toEqual([]);
});

for (const width of [320, 390, 1440]) {
  test(`forecast is accessible in light/dark at ${width}px without changing existing totals`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const { chart, requests } = await openDashboard(page);
    const original = await page.locator('.dash-money, .dash-hero, .dash-counts').allTextContents();
    await enableForecast(chart);
    await expect(chart.getByLabel('Prognose anzeigen', { exact: true })).toBeFocused();
    await chart.locator('.revenue-details > summary').click();
    await chart.getByLabel('Reservierungen zusätzlich berücksichtigen').check();
    await chart.getByLabel('Vorjahr als Monatslinie').check();
    await expect(chart.getByLabel('Vorjahr als Monatslinie')).toBeFocused();
    await expect(chart.locator('.revenue-totals')).toContainText(/1\.240,00\s*€/);
    const plot = chart.getByRole('group', { name: /Pfeiltasten wechseln/ });
    await plot.focus();
    await plot.press('Home');
    for (let month = 1; month < 10; month++) await plot.press('ArrowRight');
    await expect(chart.locator('.revenue-readout')).toContainText('Oktober');
    await expect(chart.locator('.revenue-readout')).toContainText(/Aufgelaufen: 40,00\s*€/);
    await expect(chart.locator('.revenue-readout')).toContainText(/Bestand · Restprognose: 60,00\s*€/);
    await expect(chart.locator('.revenue-readout')).toContainText(/Gesamt mit Szenario: 100,00\s*€/);
    await expect(chart.locator('.revenue-plot [aria-live]')).toContainText('Oktober');
    await chart.locator('.revenue-details > summary').click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
      const result = await new AxeBuilder({ page }).include('.revenue-chart').include('.dashboard-year-nav')
        .withTags(['wcag22a', 'wcag22aa']).analyze();
      expect(result.violations.filter(item => ['serious', 'critical'].includes(item.impact))).toEqual([]);
      if (process.env.PARKRR_CAPTURE_DIR) await chart.screenshot({
        path: path.join(process.env.PARKRR_CAPTURE_DIR, `dashboard-forecast-month-${theme}-${width}.png`),
        style: '.app-header, .tabbar { visibility: hidden !important; }',
      });
    }
    await chart.getByRole('button', { name: 'Jahre', exact: true }).click();
    await expect(chart.getByRole('button', { name: 'Jahre', exact: true })).toBeFocused();
    await expect(chart.locator('.revenue-bar-forecast')).toHaveCount(1);
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
      const result = await new AxeBuilder({ page }).include('.revenue-chart').withTags(['wcag22a', 'wcag22aa']).analyze();
      expect(result.violations.filter(item => ['serious', 'critical'].includes(item.impact))).toEqual([]);
      if (process.env.PARKRR_CAPTURE_DIR) await chart.screenshot({
        path: path.join(process.env.PARKRR_CAPTURE_DIR, `dashboard-forecast-year-${theme}-${width}.png`),
        style: '.app-header, .tabbar { visibility: hidden !important; }',
      });
    }
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.fontSize = '32px');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
    expect(await page.locator('.dash-money, .dash-hero, .dash-counts').allTextContents()).toEqual(original);
    expect(errors).toEqual([]);
    expectReadonly(requests);
  });
}

test('next-year planning stays inside the chart and does not fabricate a completed prior year', async ({ page }) => {
  const { chart, overviewCalls, forecastCalls, requests } = await openDashboard(page);
  const original = await page.locator('.dash-money').allTextContents();
  await enableForecast(chart);
  await chart.getByRole('combobox', { name: 'Planungsjahr der Umsatzprognose' }).selectOption('2027');
  await expect(chart.getByRole('heading')).toHaveText('Umsatz pro Monat · 2027');
  await expect(chart.locator('.revenue-totals')).toContainText('Aufgelaufen 2027');
  await chart.locator('.revenue-details > summary').click();
  await chart.getByLabel('Vorjahr als Monatslinie').check();
  await expect(chart.getByText('Keine Vergleichsdaten für 2026 – das ist kein Nullumsatz.')).toBeVisible();
  await expect(chart.locator('path.revenue-series-previous')).toHaveCount(0);
  await chart.locator('.revenue-values > summary').click();
  await expect(chart.locator('tbody tr').first()).toContainText('—');
  await expect(page.getByRole('combobox', { name: 'Dashboard-Jahr' })).toHaveValue('2026');
  await expect(page.getByRole('combobox', { name: 'Dashboard-Jahr' }).locator('option[value="2027"]')).toHaveCount(0);
  expect(await page.locator('.dash-money').allTextContents()).toEqual(original);
  expect(overviewCalls).toEqual([null]);
  expect(forecastCalls).toEqual([2026, 2027]);
  await chart.getByRole('button', { name: 'Jahre', exact: true }).click();
  await expect(chart.locator('.revenue-axis').last()).toHaveText('2027');
  await chart.getByLabel('Prognose anzeigen', { exact: true }).uncheck();
  await expect(chart.getByRole('heading')).toHaveText('Umsatz im Jahresvergleich · 2026');
  await expect(chart.locator('.revenue-axis').last()).toHaveText('2026');
  await expect(chart.locator('.revenue-totals')).toHaveCount(0);
  expectReadonly(requests);
});

for (const previous of [null, Array(12).fill(0)]) {
  test(`prior-year reference distinguishes ${previous === null ? 'unknown' : 'known zero'} without enabling forecasting`, async ({ page }) => {
    const { chart, requests } = await openDashboard(page, { forecastHandler: route => route.fulfill({ json: { ...forecastReply, previous_by_month: previous } }) });
    await chart.locator('.revenue-details > summary').click();
    await chart.getByLabel('Vorjahr als Monatslinie').check();
    await chart.locator('.revenue-values > summary').click();
    await expect(chart.getByLabel('Prognose anzeigen', { exact: true })).not.toBeChecked();
    await expect(chart.locator('.revenue-totals')).toHaveCount(0);
    const row = chart.locator('tbody tr').first();
    await expect(row.locator('td').last()).toHaveText(previous === null ? '—' : /0,00\s*€/);
    await expect(chart.locator('path.revenue-series-previous')).toHaveCount(previous === null ? 0 : 1);
    expectReadonly(requests);
  });
}

test('past-year view cannot fabricate a retrospective forecast', async ({ page }) => {
  const { chart, requests } = await openDashboard(page, { data: overview(2024), forecastHandler: route => route.fulfill({ json: {
    ...forecastReply, year: 2024, actual_by_month: Array(12).fill(80), previous_by_month: null,
  } }) });
  await expect(chart.getByLabel('Prognose anzeigen', { exact: true })).toBeDisabled();
  await expect(chart).toContainText('keine rückwirkende Prognose');
  await chart.getByRole('button', { name: 'Jahre', exact: true }).click();
  await expect(chart.locator('.revenue-axis').last()).toHaveText('2024');
  await expect(chart.locator('.revenue-bar-forecast')).toHaveCount(0);
  expectReadonly(requests);
});

test('forecast errors keep actual values and permit retry, never substitute zero estimates', async ({ page }) => {
  let calls = 0;
  const { chart, requests } = await openDashboard(page, { forecastHandler: route => route.fulfill({
    status: ++calls === 1 ? 503 : 200, json: calls === 1 ? { error: 'Vorschau vorübergehend nicht verfügbar.' } : forecastReply,
  }) });
  await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
  await expect(chart.getByRole('alert')).toContainText('Es werden keine Ersatzwerte geschätzt.');
  await expect(chart.locator('.revenue-totals')).toHaveCount(0);
  await expect(chart.locator('.chart')).toBeVisible();
  await chart.getByRole('button', { name: 'Erneut versuchen', exact: true }).click();
  await expect(chart.locator('.revenue-totals')).toContainText(/1\.200,00\s*€/);
  await expect(chart.getByRole('alert')).toBeHidden();
  expectReadonly(requests);
});

test('a late forecast response cannot overwrite the newly selected planning year', async ({ page }) => {
  let release, start;
  const blocked = new Promise(resolve => { release = resolve; });
  const started = new Promise(resolve => { start = resolve; });
  const { chart, requests } = await openDashboard(page, { forecastHandler: async (route, year) => {
    if (year === 2026) { start(); await blocked; }
    await route.fulfill({ json: year === 2026 ? forecastReply : { ...forecastReply, year: 2027,
      actual_by_month: Array(12).fill(0), forecast_by_month: Array(12).fill(100), previous_by_month: null } });
  } });
  await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
  await started;
  await chart.getByRole('combobox', { name: 'Planungsjahr der Umsatzprognose' }).selectOption('2027');
  await expect(chart.locator('.revenue-totals')).toContainText('Aufgelaufen 2027');
  const oldResponse = page.waitForResponse(response => response.url().endsWith('/overview/forecast?year=2026'));
  release();
  await oldResponse;
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(chart.getByRole('heading')).toHaveText('Umsatz pro Monat · 2027');
  await expect(chart.locator('.revenue-totals')).toContainText('Aufgelaufen 2027');
  expectReadonly(requests);
});

test('malformed monetary data is an error rather than a misleading chart', async ({ page }) => {
  const { chart, requests } = await openDashboard(page, { forecastHandler: route => route.fulfill({ json: {
    ...forecastReply, forecast_by_month: [1, 2],
  } }) });
  await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
  await expect(chart.getByRole('alert')).toContainText('unvollständig');
  await expect(chart.locator('.revenue-totals')).toHaveCount(0);
  expectReadonly(requests);
});
