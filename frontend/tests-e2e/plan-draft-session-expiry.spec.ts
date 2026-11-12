import { test, expect } from '@playwright/test';
import { ACCOUNTS, mockApi, seedAuth } from './fixtures/recs';

// #1735: a session that expires while a plan is being edited must not cost
// the user their typing. Sign-in reloads the page, so the form comes back
// from the session copy.
test('plan draft returns after the session expires mid-edit and the user signs in again', async ({ page }) => {
  await seedAuth(page);
  await mockApi(page);
  const plan = {
    id: 'plan-1', name: 'Saved plan', enabled: true, auto_purchase: false, notification_days_before: 3,
    services: { 'azure:compute': { provider: 'azure', service: 'compute', enabled: true, term: 1, payment: 'monthly', coverage: 80 } },
    ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 },
  };
  let sessionExpired = false;
  let saveAttempts = 0;
  await page.route('**/api/config', route => route.fulfill({ json: { global: { auto_purchase_enabled: false }, services: {} } }));
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/plans**', async route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith('/accounts')) {
      await route.fulfill({ json: request.method() === 'GET' ? [ACCOUNTS[2]] : {} });
    } else if (request.method() === 'PUT') {
      saveAttempts++;
      await route.fulfill({ status: 401, json: { error: 'Unauthorized' } });
    } else {
      await route.fulfill({ json: path.endsWith('/plan-1') ? plan : { plans: [plan] } });
    }
  });
  await page.route('**/api/auth/me', route => sessionExpired
    ? route.fulfill({ status: 401, json: { error: 'Unauthorized' } })
    : route.fulfill({ json: { id: 'user-smoke', email: 'smoke@example.com', groups: ['00000000-0000-5000-8000-000000000001'] } }));
  await page.route('**/api/auth/login', route => {
    sessionExpired = false;
    return route.fulfill({ json: { token: 'fresh-token', csrf_token: 'fresh-csrf' } });
  });

  await page.goto('/plans');
  await page.locator('[data-action="edit-plan"]').click();
  await page.locator('#plan-name').fill('Draft typed before expiry');
  await page.locator('#plan-coverage').fill('55');
  await page.locator('#plan-advanced-settings summary').click();
  await page.locator('#plan-notify-days').fill('9');
  // Past the draft debounce, so the copy exists before the session ends.
  await page.waitForFunction(() => sessionStorage.getItem('cudly:plan-form-draft') !== null);

  sessionExpired = true;
  await page.getByRole('button', { name: 'Save Plan', exact: true }).click();
  await expect.poll(() => saveAttempts).toBe(1);
  await expect(page.locator('#plan-modal')).toBeVisible();

  await page.reload();
  await expect(page.locator('#login-form')).toBeVisible();
  await page.locator('#login-email').fill('smoke@example.com');
  await page.locator('#login-password').fill('correct horse battery');
  await page.locator('#login-form button[type="submit"]').click();

  await expect(page.locator('#plan-modal')).toBeVisible();
  await expect(page.locator('#plan-id')).toHaveValue('plan-1');
  await expect(page.locator('#plan-name')).toHaveValue('Draft typed before expiry');
  await expect(page.locator('#plan-coverage')).toHaveValue('55');
  await expect(page.locator('#plan-notify-days')).toHaveValue('9');

  await page.locator('#close-plan-modal-btn').click();
  expect(await page.evaluate(() => sessionStorage.getItem('cudly:plan-form-draft'))).toBeNull();
});
