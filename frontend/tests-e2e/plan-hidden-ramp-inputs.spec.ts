import { test, expect, Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

// Issue #1536: a preset ramp wrote an out-of-range value into the hidden custom-ramp inputs, so native validation
// blocked Save on an unfocusable control with no POST and no message.
async function openCreateForm(page: Page, posts: unknown[]) {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.fallback() : route.abort());
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/plans**', route => {
    if (route.request().method() === 'POST') {
      posts.push(route.request().postDataJSON());
      return route.fulfill({ json: { id: 'new-plan', name: 'Hidden ramp', enabled: true, services: {} } });
    }
    return route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/accounts') ? [] : { plans: [] } });
  });
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.goto('/plans');
  await page.locator('#new-plan-btn').click();
  const modal = page.locator('#plan-modal');
  await expect(modal).toBeVisible();
  await modal.locator('#plan-name').fill('Hidden ramp');
  await modal.locator('#plan-account-search').fill('AWS');
  await modal.locator('.account-suggestion-item').first().click();
  return modal;
}

const labels: Record<string, string> = { custom: 'Custom', 'weekly-25pct': 'Weekly 25%', immediate: 'Immediate' };
for (const path of [['custom', 'immediate'], ['weekly-25pct', 'immediate']]) {
  test(`Create plan saves the Immediate ramp after ${path[0]}`, async ({ page }) => {
    const posts: unknown[] = [];
    const modal = await openCreateForm(page, posts);
    await modal.locator('.ramp-option').filter({ hasText: labels[path[0]!]! }).click();
    await expect(modal.locator(`input[name="ramp-schedule"][value="${path[0]}"]`)).toBeChecked();
    await modal.locator('.ramp-option').filter({ hasText: labels.immediate! }).click();
    await expect(modal.locator('input[name="ramp-schedule"][value="immediate"]')).toBeChecked();
    await expect(modal.locator('#custom-ramp-config')).toBeHidden();

    const save = page.getByRole('button', { name: 'Save Plan' });
    await expect(save).toBeEnabled();
    await save.click();
    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0]).toMatchObject({ ramp_schedule: 'immediate' });
  });
}
