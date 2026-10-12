const { test, expect } = require('@playwright/test');
const { mockUI, origin } = require('./helpers/ui-fixture');

test.use({ serviceWorkers: 'block' });

async function setup(page, { stale = false, error, passkeyOnly = false } = {}) {
  // The HTTP-only fixture has no WebAuthn constructor; deletion never invokes it.
  await page.addInitScript(() => { window.PublicKeyCredential = class {}; });
  let credentials = [{ id: 12, name: 'Synthetischer Testschlüssel', created_at: '2026-10-01T12:00:00Z' }];
  const bodies = [];
  await mockUI(page, { role: 'admin', overrides: {
    '/auth/capabilities': { passkeys: true, passkey_only: passkeyOnly },
    '/passkeys': route => route.fulfill({ json: credentials }),
    '/passkeys/12': async route => {
      expect(route.request().method()).toBe('DELETE');
      const body = route.request().postDataJSON();
      bodies.push(body);
      if (stale && !body.password) return route.fulfill({ status: 403, json: { error: 'reauth_required' } });
      if (error) return route.fulfill({ status: error.status, json: { error: error.message } });
      credentials = [];
      return route.fulfill({ json: { status: 'deleted' } });
    },
  } });
  await page.goto(origin + '/#/settings');
  const row = page.locator('.balance', { hasText: 'Synthetischer Testschlüssel' });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'Entfernen', exact: true }).click();
  return { bodies, row, confirm: page.getByRole('dialog', { name: 'Passkey entfernen?', exact: true }) };
}

test('recent session removes passkey with the initial empty-password request', async ({ page }) => {
  const { bodies, row, confirm } = await setup(page);
  await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
  await expect(row).toHaveCount(0);
  expect(bodies).toEqual([{ password: '' }]);
  await expect(page.getByRole('dialog', { name: 'Bestätigung erforderlich' })).toHaveCount(0);
});

for (const [width, theme] of [[1440, 'light'], [390, 'dark']]) {
  test(`stale session retries once after password confirmation at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const { bodies, row, confirm } = await setup(page, { stale: true });
    await page.evaluate(theme => document.documentElement.setAttribute('data-theme', theme), theme);
    await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Bestätigung erforderlich' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel('Kontopasswort')).toBeFocused();
    expect(bodies).toEqual([{ password: '' }]);
    await page.screenshot({ path: test.info().outputPath(`passkey-stepup-${theme}-${width}.png`) });
    await dialog.getByLabel('Kontopasswort').fill('synthetic-password');
    await dialog.getByRole('button', { name: 'Bestätigen', exact: true }).click();
    await expect(row).toHaveCount(0);
    expect(bodies).toEqual([{ password: '' }, { password: 'synthetic-password' }]);
  });
}

test('canceling step-up keeps the passkey and never retries', async ({ page }) => {
  const { bodies, row, confirm } = await setup(page, { stale: true });
  await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Bestätigung erforderlich' });
  await dialog.getByRole('button', { name: 'Abbrechen', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(row).toBeVisible();
  expect(bodies).toEqual([{ password: '' }]);
  await expect(page.locator('.toast', { hasText: 'Passkey entfernt' })).toHaveCount(0);
});

test('canceling deletion confirmation sends no request', async ({ page }) => {
  const { bodies, row, confirm } = await setup(page);
  await confirm.getByRole('button', { name: 'Abbrechen', exact: true }).click();
  await expect(row).toBeVisible();
  expect(bodies).toEqual([]);
});

for (const error of [
  { status: 429, message: 'Zu viele Versuche – bitte später erneut versuchen' },
  { status: 409, message: 'Im Passkey-only-Modus muss mindestens ein Passkey erhalten bleiben' },
  { status: 500, message: 'could not delete passkey' },
]) test(`server rejection ${error.status} leaves the credential visible`, async ({ page }) => {
  const { bodies, row, confirm } = await setup(page, { error });
  await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
  await expect(page.getByText(error.message, { exact: true })).toBeVisible();
  await expect(row).toBeVisible();
  expect(bodies).toHaveLength(1);
  await expect(page.getByRole('dialog', { name: 'Bestätigung erforderlich' })).toHaveCount(0);
});

test('wrong password does not claim success or retry repeatedly', async ({ page }) => {
  const { bodies, row, confirm } = await setup(page, { stale: true, error: { status: 403, message: 'Passwort ist falsch' } });
  await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Bestätigung erforderlich' });
  await dialog.getByLabel('Kontopasswort').fill('synthetic-wrong-password');
  await dialog.getByRole('button', { name: 'Bestätigen', exact: true }).click();
  await expect(page.getByText('Passwort ist falsch', { exact: true })).toBeVisible();
  await expect(row).toBeVisible();
  expect(bodies).toHaveLength(2);
  await expect(page.locator('.toast', { hasText: 'Passkey entfernt' })).toHaveCount(0);
});

test('passkey-only stale sessions receive re-login guidance without a password prompt', async ({ page }) => {
  const { bodies, row, confirm } = await setup(page, { stale: true, passkeyOnly: true });
  await expect(confirm).not.toContainText('dein Passwort bleiben gültig');
  await confirm.getByRole('button', { name: 'Entfernen', exact: true }).click();
  await expect(page.getByText('Bitte erneut mit einem Passkey anmelden und die Aktion danach wiederholen.', { exact: true })).toBeVisible();
  await expect(page.getByRole('dialog', { name: 'Bestätigung erforderlich' })).toHaveCount(0);
  await expect(row).toBeVisible();
  expect(bodies).toHaveLength(1);
});
