const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const { mockUI, origin } = require('./helpers/ui-fixture');
const { overrides } = require('./helpers/overhaul-fixture');

// The fixture intercepts every request, including assets; no live server is used.
test.use({ serviceWorkers: 'block' });

for (const width of [1440, 390]) {
  for (const theme of ['light', 'dark']) {
    test(`vehicle cost preview retains brand contrast and live pricing at ${width}px ${theme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await page.clock.setFixedTime(new Date('2026-10-12T12:00:00Z'));
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      const existing = { ...overrides['/vehicles'][0], label: 'Demo Wohnmobil 1', license_plate: 'DEMO-01',
        status: 'stored', rate: 85, start_date: '2026-11-01', end_date: '2026-11-30',
        reserved_from: null, reserved_until: null };
      const requests = await mockUI(page, { overrides: { ...overrides, '/vehicles': [existing] } });
      await page.goto(origin + '/#/vehicle/1');
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const modal = page.locator('#modal');
      const summary = modal.locator('.vehicle-cost-summary');
      const total = summary.locator('output');
      await expect(total).toHaveText(/85,00\s*€/);
      await modal.locator('#f_rate').fill('90');
      await expect(total).toHaveText(/90,00\s*€/);
      await modal.locator('#f_end_date').fill('2026-11-15');
      await expect(total).toHaveText(/45,00\s*€/);
      await modal.locator('#f_end_date').fill('2026-11-30');
      await expect(total).toHaveText(/90,00\s*€/);
      await summary.scrollIntoViewIfNeeded();

      const measurements = await summary.evaluate(node => {
        const canvas = document.createElement('canvas');
        canvas.width = canvas.height = 1;
        const context = canvas.getContext('2d', { willReadFrequently: true });
        // Canvas resolves rgb(), hex and color-mix() to the same sRGB space.
        const rgba = css => {
          context.clearRect(0, 0, 1, 1);
          context.fillStyle = css;
          context.fillRect(0, 0, 1, 1);
          return Array.from(context.getImageData(0, 0, 1, 1).data);
        };
        const over = (foreground, background) => {
          const alpha = foreground[3] / 255;
          return foreground.slice(0, 3).map((value, index) => value * alpha + background[index] * (1 - alpha));
        };
        const luminance = rgb => rgb.reduce((sum, value, index) => {
          const channel = value / 255;
          return sum + [0.2126, 0.7152, 0.0722][index] *
            (channel <= 0.04045 ? channel / 12.92 : Math.pow((channel + 0.055) / 1.055, 2.4));
        }, 0);
        const layers = [];
        for (let parent = node; parent; parent = parent.parentElement) {
          const style = getComputedStyle(parent);
          const background = rgba(style.backgroundColor);
          layers.push({ background, image: style.backgroundImage, opacity: Number(style.opacity) });
          if (background[3] === 255) break;
        }
        // Primary-soft is translucent: contrast must include the opaque dialog below it.
        const surface = layers.toReversed().reduce((background, layer) => over(layer.background, background), [255, 255, 255]);
        const style = getComputedStyle(node);
        const text = selector => {
          const target = node.querySelector(selector);
          const color = rgba(getComputedStyle(target).color);
          const foreground = luminance(over(color, surface));
          const background = luminance(surface);
          return { color, contrast: (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05) };
        };
        const rect = target => {
          const { x, y, width, height, bottom } = target.getBoundingClientRect();
          return { x, y, width, height, bottom };
        };
        const grid = node.closest('.vehicle-booking-grid');
        const fieldBottoms = ['#f_start_date', '#f_end_date', '#f_billing_period', '#f_rate']
          .map(selector => grid.querySelector(selector).closest('.form-field').getBoundingClientRect().bottom);
        return {
          background: rgba(style.backgroundColor), expectedBackground: rgba(style.getPropertyValue('--primary-soft')),
          expectedInk: rgba(style.getPropertyValue('--primary-ink')), surface, layers,
          label: text('.vehicle-cost-label'), amount: text('output'), metadata: text('.card-meta'),
          borderWidth: style.borderTopWidth, borderStyle: style.borderTopStyle,
          radius: parseFloat(style.borderTopLeftRadius),
          summary: rect(node), grid: rect(grid), fieldsBottom: Math.max(...fieldBottoms),
          columns: getComputedStyle(grid).gridTemplateColumns.split(' ').length,
          pageOverflow: document.documentElement.scrollWidth > innerWidth,
          summaryOverflow: node.scrollWidth > node.clientWidth + 1,
          modalOverflow: document.querySelector('#modal').scrollWidth > document.querySelector('#modal').clientWidth + 1,
        };
      });

      expect(measurements.background).toEqual(measurements.expectedBackground);
      expect(measurements.background[3]).toBeGreaterThan(0);
      expect(measurements.label.color).toEqual(measurements.expectedInk);
      expect(measurements.amount.color).toEqual(measurements.expectedInk);
      expect(measurements.borderWidth).toBe('1px');
      expect(measurements.borderStyle).toBe('solid');
      expect(measurements.radius).toBeGreaterThan(0);
      expect(measurements.layers.at(-1).background[3]).toBe(255);
      for (const layer of measurements.layers) {
        expect(layer.image).toBe('none');
        expect(layer.opacity).toBe(1);
      }
      for (const text of [measurements.label, measurements.amount, measurements.metadata]) {
        expect(text.contrast).toBeGreaterThanOrEqual(4.5);
      }
      expect(measurements.columns).toBe(width === 1440 ? 2 : 1);
      expect(Math.abs(measurements.summary.x - measurements.grid.x)).toBeLessThanOrEqual(1);
      expect(Math.abs(measurements.summary.width - measurements.grid.width)).toBeLessThanOrEqual(1);
      expect(measurements.summary.y).toBeGreaterThanOrEqual(measurements.fieldsBottom - 1);
      expect(measurements.summary.height).toBeLessThanOrEqual(110);
      expect(measurements.pageOverflow || measurements.summaryOverflow || measurements.modalOverflow).toBe(false);
      const axe = await new AxeBuilder({ page }).include('.vehicle-cost-summary')
        .withTags(['wcag22a', 'wcag22aa']).analyze();
      expect(axe.violations.filter(item => ['serious', 'critical'].includes(item.impact))).toEqual([]);
      expect(requests.filter(request => request.method !== 'GET')).toEqual([]);
      expect(errors).toEqual([]);
      console.info(JSON.stringify({ width, theme, height: measurements.summary.height,
        contrastLabel: measurements.label.contrast, contrastAmount: measurements.amount.contrast,
        contrastMetadata: measurements.metadata.contrast, surface: measurements.surface }));
      await testInfo.attach('cost-preview-measurements', {
        body: JSON.stringify({ width, theme, ...measurements }, null, 2), contentType: 'application/json',
      });
      await modal.screenshot({ path: testInfo.outputPath(`cost-preview-${theme}-${width}.png`) });
    });
  }
}
