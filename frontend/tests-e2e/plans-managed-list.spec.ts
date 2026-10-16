import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

for (const width of [1440, 320]) test(`managed plan row at ${width}px shows Add Purchases disabled with its reason`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 900 });
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
  await seedAuth(page); await mockApi(page);
  const plan = { id: 'managed-plan', name: 'Managed ladder', enabled: true, auto_purchase: false, notification_days_before: 3, services: {},
    ramp_schedule: { type: 'weekly', current_step: 1, total_steps: 4, step_interval_days: 7, percent_per_step: 25, dates_managed: true } };
  await page.route('**/api/plans**', route => route.fulfill({ json: { plans: [plan] } }));
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.goto('/plans');
  const add = page.locator('[data-action="add-purchases"]');
  await expect(add).toBeDisabled();
  const note = page.locator('#plan-managed-managed-plan');
  await expect(note).toBeVisible(); await expect(note).toContainText('managed by the editor');
  await expect(add).toHaveAttribute('aria-describedby', 'plan-managed-managed-plan');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await note.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath('plans-managed.png') });
});
