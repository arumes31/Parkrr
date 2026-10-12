'use strict';

// Opt-in runner fixture: preserve earlier review evidence even when a legacy
// spec supplies a hard-coded screenshot path. Product code is not modified.
const path = require('node:path');
const playwright = require('@playwright/test');

if (process.env.PARKRR_COMPREHENSIVE_EVIDENCE === '1') {
  playwright.test = playwright.test.extend({
    comprehensiveEvidence: [async ({ page }, use, testInfo) => {
      const originalScreenshot = page.screenshot.bind(page);
      let capture = 0;
      page.screenshot = options => {
        if (!options?.path) return originalScreenshot(options);
        const filename = `${++capture}-${path.basename(options.path)}`;
        return originalScreenshot({ ...options, path: testInfo.outputPath(filename) });
      };
      await use();
    }, { auto: true }],
  });
}
