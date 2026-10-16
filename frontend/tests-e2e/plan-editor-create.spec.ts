import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';
import { response as editorResponse } from '../src/__tests__/fixtures/plan-editor';

const diff = { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 0, created: 1, canceled: 0, approvals_affected: 0 } };

for (const width of [1440, 320]) test.describe(`viewport ${width}px`, () => {
  test.use({ isMobile: width <= 390 });
  test(`Create saves through PUT /editor at ${width}px and never touches the legacy routes`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const sent: Array<{ method: string; path: string; body: string }> = [];
    await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
    await seedAuth(page); await mockApi(page);
    await page.route('**/api/plans**', route => {
      const request = route.request(), path = new URL(request.url()).pathname;
      if (request.method() !== 'GET') sent.push({ method: request.method(), path, body: request.postData() ?? '' });
      if (request.method() === 'PUT' && path.endsWith('/editor')) return route.fulfill({ json: { replayed: false, mapping: [], diff, editor: editorResponse() } });
      if (path.endsWith('/editor/preview')) return route.fulfill({ json: { reviewed_diff: 'sha256:' + 'e'.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [], diff } });
      return route.fulfill({ json: path.endsWith('/accounts') ? [] : { plans: [] } });
    });
    await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
    await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
    await page.goto('/plans');
    await page.locator('#new-plan-btn').click();
    const modal = page.locator('#plan-modal'), footer = modal.locator('button[type="submit"]');
    await modal.locator('#plan-account-search').fill('AWS');
    await modal.locator('.account-suggestion-item').first().click();
    await expect(footer).toHaveText('Review changes'); await expect(footer).toBeEnabled();
    await footer.click();
    await expect(footer).toHaveText('Save changes');
    await expect(page.locator('#plan-review-feedback [role="status"]')).toContainText('1 step created');
    expect(sent.map(item => `${item.method} ${item.path.replace(/[0-9a-f-]{36}/, '<id>')}`)).toEqual(['POST /api/plans/<id>/editor/preview']);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('create-ready-to-save.png') });
    sent.length = 0;
    await footer.click();
    await expect(modal).toBeHidden();
    await expect(page.locator('.toast', { hasText: 'Plan created successfully' })).toBeVisible();
    expect(sent).toHaveLength(1);
    expect(sent[0]!.method).toBe('PUT'); expect(sent[0]!.path).toMatch(/^\/api\/plans\/[0-9a-f-]{36}\/editor$/);
    expect(JSON.parse(sent[0]!.body)).toMatchObject({ expected_revision: null, reviewed_diff: 'sha256:' + 'e'.repeat(64), plan: { ramp_schedule: { percent_per_step: expect.any(Number) } } });
    await page.screenshot({ path: testInfo.outputPath('create-saved.png') });
  });
});
