import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

// #34: the session is an httpOnly cookie, so wiping web storage in another tab
// (#317) must not sign this tab out; only a real logout does.
test('localStorage.clear() in another tab leaves this tab signed in', async ({ page, context }) => {
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/info', route => route.fulfill({ json: { version: 'test', admin_exists: true } }));
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible();

  const other = await context.newPage();
  await mockApi(other);
  await other.goto('/');
  await other.evaluate(() => localStorage.clear());
  await other.close();
  await page.evaluate(() => new Promise<void>(resolve => setTimeout(resolve, 200)));

  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'CUDly Login' })).toHaveCount(0);
});
