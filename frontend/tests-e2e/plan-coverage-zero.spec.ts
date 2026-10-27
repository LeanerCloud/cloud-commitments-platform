import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

// Issue #523: a 0% target is a real value, so it is sent as 0, and an emptied
// coverage field is refused instead of becoming 0 (Number('') is 0).
test('Create plan sends a 0% target as 0 and refuses an empty coverage field', async ({ page }) => {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.fallback() : route.abort());
  await seedAuth(page);
  await mockApi(page);
  const posts: unknown[] = [];
  await page.route('**/api/plans**', route => {
    if (route.request().method() === 'POST') {
      posts.push(route.request().postDataJSON());
      return route.fulfill({ json: { id: 'new-plan', name: 'Zero target', enabled: true, services: {} } });
    }
    return route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/accounts') ? [] : { plans: [] } });
  });
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.goto('/plans');

  await page.locator('#new-plan-btn').click();
  const modal = page.locator('#plan-modal');
  await expect(modal).toBeVisible();
  await modal.locator('#plan-name').fill('Zero target');
  await modal.locator('#plan-account-search').fill('AWS');
  await modal.locator('.account-suggestion-item').first().click();
  const save = page.getByRole('button', { name: 'Save Plan' });
  await expect(save).toBeEnabled();

  await modal.locator('#plan-coverage').fill('');
  await save.click();
  await expect(page.locator('.toast, [role="status"]').filter({ hasText: 'Target Coverage must be a whole number' }).first()).toBeVisible();
  expect(posts).toHaveLength(0);

  await modal.locator('#plan-coverage').fill('0');
  await save.click();
  await expect.poll(() => posts.length).toBe(1);
  expect(posts[0]).toMatchObject({ target_coverage: 0 });
});
