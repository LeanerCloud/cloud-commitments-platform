import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

// #317: localStorage.clear() in another tab fires a storage event with
// key === null. The cross-tab sign-out listener used to ignore that shape, so
// this tab kept rendering a signed-in view after every key was wiped.
test('localStorage.clear() in another tab signs this tab out', async ({ page, context }) => {
  let signedOut = false;
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/info', route => route.fulfill({ json: { version: 'test', admin_exists: true } }));
  await page.route('**/api/auth/me', route => signedOut
    ? route.fulfill({ status: 401, json: { error: 'unauthorized' } })
    : route.fulfill({ json: { id: 'user-smoke', email: 'smoke@example.com', groups: [], csrf_token: 'smoke-csrf' } }));
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'CUDly Login' })).toHaveCount(0);

  const other = await context.newPage();
  await mockApi(other);
  await other.goto('/');
  signedOut = true;
  await other.evaluate(() => localStorage.clear());
  await other.close();

  await expect(page.getByRole('heading', { name: 'CUDly Login' })).toBeVisible();
});
