import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

test.use({ hasTouch: true, isMobile: true });

for (const width of [320, 390, 768, 1440]) {
  test(`ladder config dialog fits and Save is tappable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
      ? route.fallback() : route.abort());
    await seedAuth(page);
    await mockApi(page);
    await page.route('**/api/config', route => route.fulfill({ json: { global: {}, services: [] } }));
    await page.route('**/api/commitment-options', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
    await page.route('**/api/ri-exchange/config', route => route.fulfill({ json: {
      mode: 'manual', auto_exchange_enabled: false, utilization_threshold: 80,
      max_payment_per_exchange_usd: 0, max_payment_daily_usd: 0, lookback_days: 30,
    } }));
    const config = {
      id: 'config-0', cloud_account_id: 'account-0', provider: 'aws', enabled: true,
      mode: 'email_approval', cadence: 'daily', target_coverage: 80, buffer_fraction: 0.2,
      baseline_percentile: 10, lookback_days: 14, buffer_utilization_threshold: 85,
      max_hourly_commit_per_run: 5, max_actions_per_run: 3,
      ramp_schedule: { steps: [{ after_days: 0, fraction: 0.5 }, { after_days: 7, fraction: 0.5 }] },
    };
    const saves: unknown[] = [];
    await page.route('**/api/ladder/configs', async route => {
      if (route.request().method() === 'GET') {
        await route.fulfill({ json: { configs: [config] } });
        return;
      }
      saves.push(route.request().postDataJSON());
      await route.fulfill({ json: config });
    });
    await page.route('**/api/ladder/runs?*', route => route.fulfill({ json: { total_count: 0, runs: [] } }));
    await page.route('**/api/ladder/tranches?*', route => route.fulfill({ json: { total_count: 0, total_usd_hr: '0.000000', events: [] } }));
    await page.goto('/admin/purchasing');
    const edit = page.locator('.ladder-edit-btn[data-provider="aws"]').first();
    await expect(edit).toBeVisible();
    // The page behind the dialog must not overflow: on a real phone that widens the layout viewport the dialog sizes to.
    expect(await page.evaluate((w) => document.documentElement.scrollWidth - w, width)).toBeLessThanOrEqual(0);
    await edit.tap();
    await expect(page.locator('#ladder-config-modal .ladder-config-dialog')).toBeVisible();

    const save = page.locator('#ladder-config-save-btn');
    const m = await page.evaluate((W) => {
      const d = document.querySelector('#ladder-config-modal .ladder-config-dialog')!.getBoundingClientRect();
      const s = document.querySelector('#ladder-config-save-btn')!.getBoundingClientRect();
      return { dialogW: d.width, dialogL: d.left, dialogR: d.right, overflow: document.documentElement.scrollWidth - W, saveH: s.height };
    }, width);
    expect(m.dialogL).toBeGreaterThanOrEqual(0);
    expect(m.dialogR).toBeLessThanOrEqual(width);
    expect(m.overflow).toBeLessThanOrEqual(0);
    expect(m.saveH).toBeGreaterThanOrEqual(44);
    await save.scrollIntoViewIfNeeded();
    await save.tap({ timeout: 3000 });
    await expect.poll(() => saves.length).toBe(1);
  });
}
