import { test, expect } from '@playwright/test';
import { ACCOUNTS, mockApi, seedAuth } from './fixtures/recs';

for (const width of [1280, 390]) {
  test(`plan cap round trip and Add Purchases at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const errors: string[] = [];
    const failedResponses: string[] = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    page.on('response', response => { if (response.status() >= 400) failedResponses.push(response.url()); });
    await seedAuth(page);
    await mockApi(page);
    let plan: Record<string, unknown> = {
      id: 'plan-cap', name: 'Cap plan', enabled: true, auto_purchase: false,
      max_step_commitment: 100.10, notification_days_before: 3,
      services: { 'azure:compute': { provider: 'azure', service: 'compute', enabled: true, term: 1, payment: 'monthly', coverage: 80 } },
      ramp_schedule: { type: 'linear', percent_per_step: 25, step_interval_days: 7, current_step: 0, total_steps: 4 },
    };
    const writes: Array<Record<string, unknown>> = [];
    let scheduled = false;
    await page.route('**/api/config', route => route.fulfill({ json: { global: { auto_purchase_enabled: false }, services: {} } }));
    await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: scheduled ? [{
      id: 'step-1', plan_id: 'plan-cap', plan_name: 'Cap plan', provider: 'azure', service: 'compute',
      resource_type: 'Standard_D2_v3', region: 'eastus', count: 20, term: 1, payment: 'monthly',
      upfront_cost: 0, estimated_savings: 50, status: 'pending', has_recommendations: true,
      scheduled_date: '2026-11-01T00:00:00Z', created_by_user_id: 'user-smoke',
    }] : [] } }));
    await page.route('**/api/plans**', async route => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (path.endsWith('/accounts')) {
        await route.fulfill({ json: request.method() === 'GET' ? [ACCOUNTS[2]] : {} });
      } else if (path.endsWith('/purchases')) {
        expect(request.method()).toBe('POST');
        expect(request.postDataJSON().count).toBe(1);
        scheduled = true;
        await route.fulfill({ json: { created: 1 } });
      } else if (request.method() === 'PUT' || request.method() === 'POST') {
        const body = request.postDataJSON() as Record<string, unknown>;
        writes.push(body);
        plan = { ...plan, ...body };
        await route.fulfill({ json: plan });
      } else {
        await route.fulfill({ json: path.endsWith('/plan-cap') ? plan : { plans: [plan] } });
      }
    });
    await page.goto('/plans');
    await page.locator('[data-action="edit-plan"]').click();
    const cap = page.getByLabel('Maximum commitment per step', { exact: true });
    await expect(cap).toHaveValue('100.1');
    await expect(page.locator('#plan-auto-purchase-state')).toContainText('is off');
    await cap.fill('100.10');
    await page.locator('.toggle-label').filter({ has: page.locator('#plan-auto-purchase') }).click();
    await expect(page.locator('#plan-auto-purchase')).toBeChecked();
    await page.getByRole('button', { name: 'Save Plan', exact: true }).click();
    await expect(page.locator('#plan-modal')).toBeHidden();
    expect(writes[0]).toMatchObject({ max_step_commitment: 100.10, auto_purchase: true });

    await page.locator('[data-action="edit-plan"]').click();
    await expect(cap).toHaveValue('100.1');
    await page.locator('.toggle-label').filter({ has: page.locator('#plan-auto-purchase') }).click();
    await expect(page.locator('#plan-auto-purchase')).not.toBeChecked();
    await cap.fill('');
    await page.getByRole('button', { name: 'Save Plan', exact: true }).click();
    await expect(page.locator('#plan-modal')).toBeHidden();
    expect(writes[1]).toMatchObject({ max_step_commitment: null, auto_purchase: false });

    await page.getByRole('button', { name: 'New Plan', exact: true }).click();
    await expect(cap).toHaveValue('');
    await page.locator('#plan-provider').selectOption('azure');
    await page.locator('#plan-service').selectOption('compute');
    await page.locator('#plan-term').selectOption('1');
    await page.locator('#plan-payment').selectOption('monthly');
    await page.locator('#plan-account-search').fill('Azure Prod');
    await page.locator('#plan-account-suggestions').getByText('Azure Prod (acct-100) (sub-aaaa)', { exact: true }).click();
    await cap.fill('100.10');
    await page.getByRole('button', { name: 'Save Plan', exact: true }).click();
    await expect(page.locator('#plan-modal')).toBeHidden();
    expect(writes[2]).toMatchObject({ max_step_commitment: 100.10, auto_purchase: false });
    await page.getByRole('button', { name: 'Add Purchases', exact: true }).click();
    await page.locator('#add-purchases-count').fill('1');
    await page.locator('#add-purchases-form').getByRole('button', { name: 'Add Purchases', exact: true }).click();
    await expect(page.locator('#planned-purchases-list')).toContainText('Standard_D2_v3');
    await expect(page.locator('#planned-purchases-list tbody td').nth(5)).toHaveText('20');
    expect(errors).toEqual([]);
    expect(failedResponses).toEqual([]);
  });
}
