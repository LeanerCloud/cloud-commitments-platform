import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

// Issue #1736: at 320px the toast stack covered the sticky settings save bar.
// It must lift above the bar instead.
test('a toast stays off the sticky settings save bar at 320x640', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.fallback() : route.abort());
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/config', route => {
    if (route.request().method() === 'PUT') return route.fulfill({ json: { status: 'updated' } });
    return route.fulfill({ json: { global: {
      enabled_providers: ['aws'], default_term: 3, default_payment: 'all-upfront', default_coverage: 70, notification_days_before: 3,
    }, services: [] } });
  });
  await page.route('**/api/commitment-options', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.route('**/api/ri-exchange/config', route => route.fulfill({ json: {
    mode: 'manual', auto_exchange_enabled: false, utilization_threshold: 80,
    max_payment_per_exchange_usd: 0, max_payment_daily_usd: 0, lookback_days: 30,
  } }));
  await page.route('**/api/ladder/**', route => route.fulfill({ json: { configs: [], runs: [], events: [], total_count: 0, total_usd_hr: '0.000000' } }));
  await page.goto('/admin/purchasing');

  await page.locator('#setting-default-coverage').fill('60');
  await page.locator('#save-purchasing-btn').click();
  const toast = page.locator('.toast').filter({ hasText: 'Settings saved' }).first();
  await expect(toast).toBeVisible();
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

  const bars = page.locator('.settings-savebar');
  const t = (await toast.boundingBox())!;
  const c = (await page.locator('#toast-container').boundingBox())!;
  const seen: unknown[] = [];
  let shown = 0;
  for (let i = 0; i < await bars.count(); i++) {
    const bar = await bars.nth(i).boundingBox();
    if (!bar) continue;
    shown++;
    seen.push(bar);
    const overlaps = t.x < bar.x + bar.width && bar.x < t.x + t.width && t.y < bar.y + bar.height && bar.y < t.y + t.height;
    expect(overlaps, `toast ${JSON.stringify(t)} covers save bar ${JSON.stringify(bar)}`).toBe(false);
  }
  expect(shown).toBeGreaterThan(0);
  console.log(`MEASURE bars=${JSON.stringify(seen)} toast=${JSON.stringify(t)} container=${JSON.stringify(c)}`);
  expect(c.x).toBeGreaterThanOrEqual(12);
  expect(c.x + c.width).toBeLessThanOrEqual(308);
});
