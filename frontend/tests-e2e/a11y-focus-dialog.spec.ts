import { test, expect, type Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

const CREATED_KEY = 'cudly_e2e_key_123';

async function describeFocus(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement;
    if (!el) return 'none';
    const h = el as HTMLElement;
    return JSON.stringify({
      tag: el.tagName.toLowerCase(),
      id: el.id,
      ariaLabel: el.getAttribute('aria-label'),
      dataAction: h.dataset['action'] ?? null,
      dataId: h.dataset['id'] ?? null,
    });
  });
}

async function attachFocus(page: Page, name: string): Promise<void> {
  await test.info().attach(name, { body: await describeFocus(page), contentType: 'text/plain' });
}

async function mockPlansPage(page: Page, purchases: unknown[]): Promise<void> {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.fallback() : route.abort());
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/plans**', route => route.fulfill({
    json: new URL(route.request().url()).pathname.endsWith('/accounts') ? [] : { plans: [] },
  }));
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
}

test('#1648 removing an account chip with the keyboard keeps focus on a neighbouring chip, then the search box', async ({ page }) => {
  await mockPlansPage(page, []);
  await page.goto('/plans');
  await page.locator('#new-plan-btn').click();
  const modal = page.locator('#plan-modal');
  await expect(modal).toBeVisible();
  await modal.locator('#plan-name').fill('Chip focus');

  const search = modal.locator('#plan-account-search');
  const removes = modal.locator('.account-chip-remove');
  for (let i = 1; i <= 3; i++) {
    await search.fill('a');
    // Already-selected accounts are filtered out, so the first suggestion is always a new one.
    await modal.locator('.account-suggestion-item').first().click();
    await expect(removes).toHaveCount(i);
  }
  const labels = await removes.evaluateAll(els => els.map(e => e.getAttribute('aria-label')));

  await removes.nth(1).focus();
  await attachFocus(page, 'focus-before-middle-removal');
  await page.keyboard.press('Enter');
  await expect(removes).toHaveCount(2);
  await attachFocus(page, 'focus-after-middle-removal');
  await expect(removes.nth(1)).toBeFocused();
  await expect(removes.nth(1)).toHaveAttribute('aria-label', labels[2]!);

  // Last chip removed: focus falls back to the previous chip.
  await page.keyboard.press('Enter');
  await expect(removes).toHaveCount(1);
  await attachFocus(page, 'focus-after-last-chip-removal');
  await expect(removes.nth(0)).toBeFocused();
  await expect(removes.nth(0)).toHaveAttribute('aria-label', labels[0]!);

  await page.keyboard.press('Enter');
  await expect(removes).toHaveCount(0);
  await attachFocus(page, 'focus-after-all-chips-removed');
  await expect(search).toBeFocused();
});

test('#1666 declining the disable-plan confirm returns focus to the same row action, not body', async ({ page }) => {
  const purchase = {
    id: 'pp-focus', plan_id: 'plan-1', plan_name: 'Ramp', scheduled_date: '2027-01-01T00:00:00Z',
    provider: 'aws', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1', count: 3, term: 1,
    payment: 'all-upfront', estimated_savings: 1234, upfront_cost: 25000, has_recommendations: true,
    status: 'pending', step_number: 1, total_steps: 2, created_by_user_id: 'user-smoke',
  };
  await mockPlansPage(page, [purchase]);
  await page.goto('/plans');

  const disable = page.locator('#planned-purchases-list button[data-action="disable"][data-id="pp-focus"]');
  await expect(disable).toBeVisible();

  for (const decline of ['escape', 'keep-it'] as const) {
    await disable.focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Cancel this scheduled purchase?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel purchase and disable plan' })).toBeVisible();
    await test.info().attach(`confirm-dialog-aria-${decline}`, { body: await dialog.ariaSnapshot(), contentType: 'text/plain' });

    if (decline === 'escape') await page.keyboard.press('Escape');
    else await dialog.getByRole('button', { name: 'Keep it' }).click();
    await expect(dialog).toHaveCount(0);

    // The list reloads after the decline and replaces the row; wait for focus to settle on the new button.
    await expect(disable).toBeFocused();
    await attachFocus(page, `focus-after-decline-${decline}`);
    const active = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null;
      return { tag: el?.tagName, action: el?.dataset['action'], id: el?.dataset['id'] };
    });
    expect(active).toEqual({ tag: 'BUTTON', action: 'disable', id: 'pp-focus' });
  }
});

test('#1666 API key created modal is a labelled modal dialog and holds focus', async ({ page }) => {
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/users*', route => route.fulfill({ json: { users: [] } }));
  await page.route('**/api/groups*', route => route.fulfill({ json: { groups: [] } }));
  await page.route('**/api/api-keys/usage-stats', route => route.fulfill({ json: {
    total_active: 0, total_requests_window: 0, total_requests_lifetime: 0, lifetime_partial: false, top_keys: [],
  } }));
  await page.route('**/api/api-keys', route => {
    if (route.request().method() === 'POST') {
      return route.fulfill({ json: { api_key: CREATED_KEY, key_id: 'k1', info: { id: 'k1', name: 'CI key' } } });
    }
    return route.fulfill({ json: { api_keys: [] } });
  });
  await page.route('**/api/auth/me/permissions', route => route.fulfill({ json: {
    permissions: [{ action: 'view', resource: 'recommendations' }], is_admin: true,
  } }));

  await page.goto('/admin/users');
  await page.locator('#create-apikey-btn').click();
  await page.locator('#apikey-name').fill('CI key');
  await page.locator('#apikey-password').fill('correct horse battery');
  await page.locator('#apikey-permissions-list input[type="checkbox"]').first().check();
  await page.locator('#create-apikey-submit-btn').click();

  const modal = page.locator('#apikey-created-modal');
  await expect(modal).toBeVisible();
  await expect(modal).toHaveAttribute('role', 'dialog');
  await expect(modal).toHaveAttribute('aria-modal', 'true');
  const labelledBy = await modal.getAttribute('aria-labelledby');
  expect(labelledBy).toBeTruthy();
  await expect(page.locator(`[id="${labelledBy}"]`)).toHaveText('API Key Created Successfully');
  await expect(page.getByRole('dialog', { name: 'API Key Created Successfully' })).toBeVisible();

  await attachFocus(page, 'focus-in-apikey-modal');
  await test.info().attach('apikey-modal-aria', { body: await modal.ariaSnapshot(), contentType: 'text/plain' });
  const inside = await page.evaluate(() => document.getElementById('apikey-created-modal')?.contains(document.activeElement) ?? false);
  expect(inside).toBe(true);
});
