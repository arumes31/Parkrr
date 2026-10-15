const { test, expect } = require('@playwright/test');
const { mockUI, origin } = require('./helpers/ui-fixture');

test.use({ serviceWorkers: 'block' });

for (const width of [390, 768, 1440]) {
  test(`chart pointer selects the rendered value with height-constrained SVGs at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    const actual = [...Array.from({ length: 10 }, (_, index) => (index + 1) * 10), 0, 0];
    await mockUI(page, { overrides: {
      '/overview': { year: 2026, current_year: 2026, available_years: [2025, 2026],
        revenue_by_month: actual, charges_by_month: actual, status_counts: {}, top_outstanding: [] },
      '/overview/forecast': { year: 2026, current_year: 2026, as_of: '2026-10-12',
        actual_by_month: actual, forecast_by_month: Array(12).fill(0), reservations_by_month: Array(12).fill(0),
        previous_by_month: null, assumptions: [], warnings: [],
        years: [{ year: 2025, actual: 400, forecast: 0, reservations: 0 },
          { year: 2026, actual: 550, forecast: 0, reservations: 0 }],
      },
    } });
    await page.goto(origin + '/#/dashboard');
    const chart = page.locator('.revenue-chart');
    const hover = async marker => {
      await marker.evaluate(node => node.scrollIntoView({ behavior: 'instant', block: 'center' }));
      const rect = await marker.boundingBox();
      expect(rect).not.toBeNull();
      await page.mouse.move(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return rect;
    };
    const last = await hover(chart.locator('.c-enddot'));
    await expect(chart.locator('.c-tip .k')).toHaveText('Oktober');
    const anchor = await chart.locator('.c-tip').evaluate(node =>
      node.parentElement.getBoundingClientRect().left + parseFloat(node.style.left));
    expect(Math.abs(anchor - (last.x + last.width / 2))).toBeLessThan(1);

    await chart.getByLabel('Prognose anzeigen', { exact: true }).check();
    await expect(chart.locator('.revenue-totals')).toBeVisible();
    for (const [index, name] of [[0, 'Jänner'], [9, 'Oktober']]) {
      await hover(chart.locator('.revenue-point.revenue-series-actual').nth(index));
      await expect(chart.locator('.revenue-readout strong')).toHaveText(name);
    }
    await chart.getByRole('button', { name: 'Jahre', exact: true }).click();
    await hover(chart.locator('.revenue-bar-actual').first());
    await expect(chart.locator('.revenue-readout strong')).toHaveText('2025');
  });
}
