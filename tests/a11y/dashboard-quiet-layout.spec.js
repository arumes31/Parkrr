const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');
const captureScreenshots = process.env.PARKRR_QA_NO_SCREENSHOTS !== '1';

// Synthetic, fully intercepted scenarios: never contact the running application.
test.use({ serviceWorkers: 'block' });
test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date('2026-10-12T12:00:00Z'));
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.addInitScript(() => {
    localStorage.setItem('parkrr_portal_lang', 'de');
    localStorage.setItem('parkrr-veh', '0');
  });
});

const actual = [...Array(9).fill(100), 40, 0, 0];
const forecastReply = {
  year: 2026, current_year: 2026, as_of: '2026-10-12',
  available_years: [2025, 2026], forecast_years: [2025, 2026, 2027], has_year_data: true,
  actual_by_month: actual, forecast_by_month: [...Array(9).fill(0), 60, 100, 100],
  reservations_by_month: Array(12).fill(0), previous_by_month: Array(12).fill(80),
  years: [
    { year: 2025, actual: 960, forecast: 0, reservations: 0, has_data: true },
    { year: 2026, actual: 940, forecast: 260, reservations: 0, has_data: true },
    { year: 2027, actual: 0, forecast: 1200, reservations: 0, has_data: true },
  ],
  assumptions: ['Bestehende Mietpreise bleiben unverändert.', 'Keine neuen Buchungen angenommen.'],
  warnings: [],
};

const setupItems = [
  { id: 'billing', label: 'Rechnungsdaten', state: 'blocked', critical: true,
    reason: 'Rechnungsdaten sind noch nicht eingerichtet.', href: '#/billing' },
  { id: 'smtp', label: 'E-Mail-Versand', state: 'warning',
    reason: 'E-Mail-Versand ist optional noch nicht eingerichtet.', href: '#/settings' },
  { id: '2fa', label: 'Zwei-Faktor-Schutz', state: 'warning', critical: true,
    reason: 'Zusätzlichen Anmeldeschutz einrichten.', href: '#/users' },
  { id: 'auto_invoice', label: 'Automatische Rechnungen', state: 'blocked',
    reason: 'Automatischen Rechnungslauf prüfen.', href: '#/billing' },
];
const futureEndings = Array.from({ length: 11 }, (_, index) => ({
  id: 20 + index, label: 'Späteres Mietende ' + (index + 1), person_name: 'Demo Person',
  days_left: 8 + index, end_date: '2026-10-' + (20 + index),
}));

async function openQuietDashboard(page, { urgent = true, portalRequests, blockFonts = false } = {}) {
  const requests = await mockUI(page, { role: 'admin', overrides: {
    ...overrides,
    '/overview': { year: 2026, current_year: 2026, available_years: [2025, 2026],
      has_year_data: true, previous_year_has_data: true, total_persons: 4,
      active_vehicles: 5, total_vehicles: 6, total_categories: 1, outstanding_total: 200,
      accrued_this_year: 940, accrued_prev_year: 800, accrued_prev_full: 960,
      paid_total: 740, payments_this_year: 740, payments_total: 740,
      revenue_by_month: actual, charges_by_month: Array(12).fill(0),
      status_counts: { stored: 5, collected: 1 }, top_outstanding: [] },
    '/overview/forecast': forecastReply,
    '/occupancy': { active: 5, placed: 2, halls: [], trend: [] },
    '/invoices/overdue': urgent ? [{ id: 71, person_id: 1, person_name: 'Demo Rechnung',
      number: 'TEST-71', days_overdue: 12, open_amount: 200 }] : [],
    '/portal-requests': portalRequests ?? (urgent ? [{ id: 81, person_id: 2, person_name: 'Demo Abholwunsch',
      kind: 'pickup', status: 'offen', created_at: '2026-10-12T08:00:00Z',
      payload: { date: '2026-10-14', note: 'Synthetischer Abholwunsch' } }] : []),
    '/vehicles/ending-soon': [
      ...(urgent ? [
        { id: 11, label: 'Mietende heute', person_name: 'Demo Heute', days_left: 0, end_date: '2026-10-12' },
        { id: 12, label: 'Mietende in sieben Tagen', person_name: 'Demo Sieben', days_left: 7, end_date: '2026-10-19' },
      ] : []),
      ...futureEndings,
    ],
    '/admin/readiness': { state: 'blocked', checked_at: '2026-10-12T12:00:00Z', items: [
      ...setupItems,
      { id: 'backup', label: 'Datensicherung', state: urgent ? 'blocked' : 'warning', critical: true,
        reason: urgent ? 'Sicherung ist blockiert: Ziel fehlt.' : 'Sicherungszeitplan noch prüfen.', href: '#/backup' },
    ] },
    '/backup/status': { enabled: false, targets: {} },
  } });
  if (blockFonts) await page.route(/\.woff2(?:\?|$)/, route => route.abort());
  await page.goto(origin + '/#/dashboard');
  await expect(page.locator('#page')).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator('.today-card')).toBeVisible();
  return requests;
}

const writes = requests => requests.filter(request => request.method !== 'GET')
  .map(({ path, method }) => ({ path, method }));

async function expectNoOverflow(page, target) {
  const viewport = await page.evaluate(() => ({ width: innerWidth,
    scrollWidth: document.documentElement.scrollWidth, fonts: document.fonts.status,
    theme: document.documentElement.dataset.theme }));
  expect(viewport.scrollWidth, JSON.stringify(viewport)).toBeLessThanOrEqual(viewport.width);
  if (target) expect(await target.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
}

async function expectRequestRowsContained(page) {
  const rows = page.locator('.pr-row');
  for (const row of await rows.all()) {
    await expectNoOverflow(page, row);
    const bounds = await row.boundingBox();
    for (const action of await row.getByRole('button').all()) {
      await expect(action).toBeVisible();
      const box = await action.boundingBox();
      expect(box.x, 'request action starts inside its row').toBeGreaterThanOrEqual(bounds.x - 1);
      expect(box.x + box.width, 'request action ends inside its row').toBeLessThanOrEqual(bounds.x + bounds.width + 1);
    }
    expect(await row.locator('.pay-main').evaluate(node => node.scrollWidth <= node.clientWidth + 1),
      'request text reflows inside the row without clipping').toBe(true);
  }
}

async function expectAccessible(page, selectors) {
  let builder = new AxeBuilder({ page }).withTags(['wcag22a', 'wcag22aa']);
  for (const selector of selectors) builder = builder.include(selector);
  const result = await builder.analyze();
  expect(result.violations.filter(item => ['serious', 'critical'].includes(item.impact))).toEqual([]);
}

function secondaryLinks(today) {
  return [
    { locator: today.getByRole('link', { name: /3 aktive Gefährte ohne Platz/ }), href: '#/vehicles?unplaced=1' },
    ...futureEndings.map(item => ({
      locator: today.getByRole('link', { name: new RegExp('^' + item.label + ' ·') }), href: '#/vehicle/' + item.id,
    })),
    ...setupItems.map(item => ({
      locator: today.getByRole('link', { name: new RegExp('^' + item.label + ' ') }), href: item.href,
    })),
  ];
}

for (const width of [1440, 390, 320]) {
  test(`quiet dashboard keeps urgent actions visible and secondary hints reachable at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const requests = await openQuietDashboard(page);
    const today = page.locator('.today-card');
    const more = today.locator('details.today-more');
    await expect(more).toHaveCount(1);
    await expect(more).not.toHaveAttribute('open', '');
    await expect(more.locator(':scope > summary')).toHaveText(/^Weitere Hinweise/);
    await expect(more.locator(':scope > summary')).toContainText('davon 2 dringend');
    await expect(today.getByRole('link')).toHaveCount(3);
    await expect(today.locator('.today-count')).toHaveText('5 dringend');

    for (const [name, href] of [
      [/Demo Rechnung · Rechnung TEST-71/, '#/invoices/71'],
      [/Demo Abholwunsch · Kundenwunsch/, '#/person/2'],
      [/^Datensicherung /, '#/backup'],
    ]) {
      const link = today.getByRole('link', { name });
      await expect(link).toBeVisible();
      await expect(link).toHaveAttribute('href', href);
    }
    // Include more than ten nonurgent items: folding must not silently truncate them.
    for (const item of secondaryLinks(today)) await expect(item.locator).toBeHidden();
    const deferredUrgent = [
      { locator: more.getByRole('link', { name: /Mietende heute · Vertrag endet heute/ }), href: '#/vehicle/11' },
      { locator: more.getByRole('link', { name: /Mietende in sieben Tagen · Vertrag endet in 7 Tagen/ }), href: '#/vehicle/12' },
    ];
    for (const item of deferredUrgent) await expect(item.locator).toBeHidden();

    const chart = page.locator('.revenue-chart');
    const chartDetails = chart.locator('details.revenue-details');
    await expect(chartDetails).not.toHaveAttribute('open', '');
    await expect(chartDetails.locator(':scope > summary')).toHaveText('Werte & Vergleich');
    await expect(chart.locator('.revenue-content > .revenue-values')).toHaveCount(0);
    const chartGeometry = await chart.boundingBox();
    console.info(JSON.stringify({ viewport: width, chartHeight: chartGeometry.height,
      todayHeight: (await today.boundingBox()).height }));
    await testInfo.attach('dashboard-geometry', { body: JSON.stringify({ width, chart: chartGeometry,
      today: await today.boundingBox() }, null, 2), contentType: 'application/json' });
    // The internal 380px target allows at most 1px for subpixel font/layout rounding.
    if (width === 1440) expect(chartGeometry.height).toBeLessThanOrEqual(381);
    const originalMoney = await page.locator('.dash-hero, .dash-money').allTextContents();
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await expectNoOverflow(page, today);
      // A card can spill into the page gutter without overflowing the viewport on Windows.
      await expectRequestRowsContained(page);
      await expectAccessible(page, ['.today-card', '.revenue-chart']);
      if (captureScreenshots) await page.screenshot({ path: testInfo.outputPath(`dashboard-quiet-${theme}-${width}.png`), fullPage: true });
    }

    const summary = more.locator(':scope > summary');
    await summary.focus();
    await summary.press('Enter');
    await expect(more).toHaveAttribute('open', '');
    await expect(summary).toBeFocused();
    for (const item of [...deferredUrgent, ...secondaryLinks(today)]) {
      await expect(item.locator).toBeVisible();
      await expect(item.locator).toHaveAttribute('href', item.href);
    }
    await expectNoOverflow(page, today);
    await summary.press('Enter');
    await expect(more).not.toHaveAttribute('open', '');

    await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
    await expect(chart.locator('.revenue-totals')).toContainText(/1\.200,00\s*€/);
    await expect(chart.locator('.revenue-content > .revenue-values')).toHaveCount(0);
    await chartDetails.locator(':scope > summary').click();
    await expect(chartDetails).toHaveAttribute('open', '');
    await expect(chartDetails).toContainText('Bestehende Mietpreise bleiben unverändert.');
    const nestedTableDetails = chartDetails.locator('details.revenue-values');
    if (await nestedTableDetails.count()) {
      if (!await nestedTableDetails.evaluate(node => node.open)) await nestedTableDetails.locator(':scope > summary').click();
    }
    await expect(chartDetails.getByRole('table')).toBeVisible();
    await expect(chartDetails.locator('tbody tr')).toHaveCount(12);
    await expect(chart.locator('.revenue-content table')).toHaveCount(0);
    await expectNoOverflow(page, chart);
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await expectAccessible(page, ['.today-card', '.revenue-chart']);
    }
    expect(await page.locator('.dash-hero, .dash-money').allTextContents()).toEqual(originalMoney);
    expect(writes(requests)).toEqual([]);
    expect(errors).toEqual([]);
  });

  test(`compact vehicle booking keeps geometry, live costs and save payload at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const payloads = [], errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const requests = await mockUI(page, { overrides: {
      ...overrides,
      '/vehicles': async route => {
        if (route.request().method() === 'GET') return route.fulfill({ json: overrides['/vehicles'] });
        payloads.push(route.request().postDataJSON());
        return route.fulfill({ json: { id: 99 } });
      },
    } });
    await page.goto(origin + '/#/vehicles');
    await page.getByRole('button', { name: '+ Gefährt', exact: true }).click();
    const modal = page.locator('#modal');
    await expect(modal).toBeVisible();
    await expect(modal.getByLabel('Kennzeichen', { exact: true })).toBeVisible();
    await expect(modal.getByLabel('Bezeichnung', { exact: true })).toBeHidden();
    await expect(modal.getByLabel('Notizen', { exact: true })).toBeHidden();
    const geometry = await modal.evaluate(node => {
      const rect = target => {
        const { x, y, width, height, bottom } = target.getBoundingClientRect();
        return { x, y, width, height, bottom };
      };
      const field = id => rect(node.querySelector(id).closest('.form-field'));
      const grid = node.querySelector('.vehicle-booking-grid');
      return { start: field('#f_start_date'), end: field('#f_end_date'), period: field('#f_billing_period'),
        rate: field('#f_rate'), total: rect(node.querySelector('.vehicle-cost-summary')), grid: rect(grid),
        columns: getComputedStyle(grid).gridTemplateColumns.split(' ').length };
    });
    await testInfo.attach('booking-geometry', { body: JSON.stringify({ width, ...geometry }, null, 2),
      contentType: 'application/json' });
    console.info(JSON.stringify({ viewport: width, bookingColumns: geometry.columns,
      costHeight: geometry.total.height, costWidth: geometry.total.width, gridWidth: geometry.grid.width }));
    expect(geometry.columns).toBe(width === 1440 ? 2 : 1);
    expect(Math.abs(geometry.total.x - geometry.grid.x)).toBeLessThanOrEqual(2);
    expect(Math.abs(geometry.total.width - geometry.grid.width)).toBeLessThanOrEqual(2);
    expect(geometry.total.y).toBeGreaterThanOrEqual(Math.max(geometry.start.bottom, geometry.end.bottom,
      geometry.period.bottom, geometry.rate.bottom) - 1);
    if (width === 1440) {
      expect(Math.abs(geometry.start.y - geometry.end.y)).toBeLessThanOrEqual(1);
      expect(Math.abs(geometry.period.y - geometry.rate.y)).toBeLessThanOrEqual(1);
      expect(geometry.end.x).toBeGreaterThan(geometry.start.x);
      expect(geometry.total.height).toBeLessThanOrEqual(110);
    } else {
      expect(geometry.end.y).toBeGreaterThanOrEqual(geometry.start.bottom - 1);
      expect(geometry.rate.y).toBeGreaterThanOrEqual(geometry.period.bottom - 1);
    }

    const start = modal.locator('#f_start_date');
    const end = modal.getByLabel('Abholdatum (optional)', { exact: true });
    const rate = modal.locator('#f_rate');
    const total = modal.locator('#vehicle-rental-total');
    await expect(start).toHaveValue('2026-10-12');
    await expect(total).toHaveText(/85,00\s*€/);
    await start.fill('2026-01-01');
    await modal.getByRole('button', { name: 'Ab heute', exact: true }).click();
    await expect(start).toHaveValue('2026-10-12');
    await modal.getByRole('button', { name: 'Ab nächstem Ersten', exact: true }).click();
    await expect(start).toHaveValue('2026-11-01');
    await modal.getByRole('button', { name: '1 Monat', exact: true }).click();
    await expect(end).toHaveValue('2026-11-30');
    await expect(total).toHaveText(/85,00\s*€/);
    await modal.getByRole('button', { name: '1 Jahr', exact: true }).click();
    await expect(end).toHaveValue('2027-10-31');
    await expect(total).toHaveText(/1\.020,00\s*€/);
    await modal.getByRole('button', { name: '1 Monat', exact: true }).click();
    await rate.fill('90');
    await expect(total).toHaveText(/90,00\s*€/);
    await end.fill('2026-11-15');
    await expect(total).toHaveText(/45,00\s*€/);
    await start.fill('2026-11-16');
    await expect(modal.locator('.vehicle-cost-label')).toHaveText('Zeitraum prüfen');
    await expect(total).toHaveText('–');
    await start.fill('2026-11-01');
    await end.fill('2026-11-30');
    await rate.fill('');
    await expect(modal.locator('.vehicle-cost-label')).toHaveText('Preis ergänzen');
    await rate.fill('0');
    await expect(total).toHaveText(/0,00\s*€/);
    await rate.fill('90');
    await expect(total).toHaveText(/90,00\s*€/);

    await modal.getByText('Fahrzeugdetails', { exact: true }).click();
    await modal.getByLabel('Bezeichnung', { exact: true }).fill('Test Wohnmobil');
    await modal.getByLabel('Kennzeichen', { exact: true }).fill('TEST-01');
    await modal.getByText('Fahrzeugdetails', { exact: true }).click();
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await expectNoOverflow(page, modal);
      await expectAccessible(page, ['#modal']);
      if (captureScreenshots) await modal.screenshot({ path: testInfo.outputPath(`vehicle-compact-${theme}-${width}.png`) });
    }
    expect(writes(requests)).toEqual([]);
    await modal.locator('#modal-submit').click();
    await expect(modal).toBeHidden();
    expect(payloads).toEqual([{ person_id: 1, category_id: 1, label: 'Test Wohnmobil', license_plate: 'TEST-01',
      notes: '', billing_period: 'monthly', rate: 90, start_date: '2026-11-01', status: 'stored',
      end_date: '2026-11-30', reserved_from: null, reserved_until: null, needs_power: false, planner_symbol: null }]);
    expect(writes(requests)).toEqual([{ path: '/vehicles', method: 'POST' }]);
    expect(errors).toEqual([]);
  });
}

for (const width of [320, 390, 1440]) {
  for (const blockFonts of [false, true]) {
    test(`customer request actions stay contained at ${width}px with ${blockFonts ? 'fallback' : 'loaded'} fonts`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      const personName = 'Kundenname' + 'Lang'.repeat(16);
      const email = 'kontakt.' + 'lang'.repeat(18) + '@example.invalid';
      const requests = await openQuietDashboard(page, { blockFonts, portalRequests: [
        { id: 81, person_id: 2, person_name: personName, kind: 'pickup', status: 'offen',
          created_at: '2026-10-12T08:00:00Z', payload: { date: '2026-10-14', note: 'Notiz' + 'Lang'.repeat(24) } },
        { id: 82, person_id: 3, person_name: 'Demo Kontakt', kind: 'contact_update', status: 'offen',
          created_at: '2026-10-12T08:00:00Z', payload: { email } },
      ] });
      await page.evaluate(() => document.fonts.ready);
      const availableFonts = await page.evaluate(() => [
        document.fonts.check('14px "Hanken Grotesk"'),
        document.fonts.check('700 14px "Bricolage Grotesque"'),
      ]);
      expect(availableFonts, 'verify webfont and fallback scenarios really differ').toEqual([!blockFonts, !blockFonts]);
      const rows = page.locator('.pr-row');
      await expect(rows).toHaveCount(2);
      await expect(rows.first().getByRole('link', { name: personName, exact: true })).toHaveAttribute('href', '#/person/2');
      await expect(rows.last()).toContainText(email);
      await expect(rows.first().getByRole('button', { name: 'Erledigt', exact: true })).toBeVisible();
      await expect(rows.last().getByRole('button', { name: 'Übernehmen', exact: true })).toBeVisible();
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
        await expectRequestRowsContained(page);
        await expectAccessible(page, ['.pr-row']);
      }
      await rows.first().getByRole('button', { name: 'Ablehnen', exact: true }).focus();
      await expect(rows.first().getByRole('button', { name: 'Ablehnen', exact: true })).toBeFocused();
      expect(writes(requests)).toEqual([]);
    });
  }
}

for (const width of [1440, 390]) {
  test(`existing vehicle details are visible in the compact edit dialog at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const existing = { ...overrides['/vehicles'][0], label: 'Demo Wohnmobil 1', license_plate: 'DEMO-01',
      status: 'stored', rate: 85, start_date: '2026-10-01', end_date: '2026-10-31',
      reserved_from: null, reserved_until: null };
    const requests = await mockUI(page, { overrides: { ...overrides, '/vehicles': [existing] } });
    await page.goto(origin + '/#/vehicle/1');
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
    const modal = page.locator('#modal');
    await expect(modal).toBeVisible();
    await expect(modal.getByRole('heading', { name: 'Gefährt bearbeiten', exact: true })).toBeVisible();
    await expect(modal.getByLabel('Bezeichnung', { exact: true })).toBeVisible();
    await expect(modal.getByLabel('Bezeichnung', { exact: true })).toHaveValue('Demo Wohnmobil 1');
    await expect(modal.getByLabel('Kennzeichen', { exact: true })).toBeVisible();
    await expect(modal.getByLabel('Kennzeichen', { exact: true })).toHaveValue('DEMO-01');
    await expect(modal.locator('#vehicle-rental-total')).toHaveText(/85,00\s*€/);
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await expectNoOverflow(page, modal);
      await expectAccessible(page, ['#modal']);
      if (captureScreenshots) await modal.screenshot({ path: testInfo.outputPath(`vehicle-edit-${theme}-${width}.png`) });
      if (captureScreenshots && width === 390) {
        await modal.getByLabel('Kennzeichen', { exact: true }).scrollIntoViewIfNeeded();
        await modal.screenshot({ path: testInfo.outputPath(`vehicle-edit-details-${theme}-${width}.png`) });
        await modal.locator('#f_person_id').scrollIntoViewIfNeeded();
      }
    }
    expect(writes(requests)).toEqual([]);
  });
}

test('with no urgent tasks setup and future ends remain quiet behind Hinweise', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const requests = await openQuietDashboard(page, { urgent: false });
  const today = page.locator('.today-card');
  const more = today.locator('details.today-more');
  await expect(more).not.toHaveAttribute('open', '');
  await expect(more.locator(':scope > summary')).toHaveText(/^Hinweise/);
  await expect(today.getByRole('link')).toHaveCount(0);
  await more.locator(':scope > summary').click();
  for (const item of secondaryLinks(today)) {
    await expect(item.locator).toBeVisible();
    await expect(item.locator).toHaveAttribute('href', item.href);
  }
  await expect(today.getByRole('link', { name: /^Datensicherung / })).toBeVisible();
  await expectNoOverflow(page, today);
  expect(writes(requests)).toEqual([]);
});
