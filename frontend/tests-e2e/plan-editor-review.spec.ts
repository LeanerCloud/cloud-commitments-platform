import { test, expect, Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';
import { response as editorResponse } from '../src/__tests__/fixtures/plan-editor';

const preview = {
  reviewed_diff: 'sha256:' + 'c'.repeat(64), required_cancellation_ids: ['old-2'], required_reapproval_ids: ['old-3'],
  diff: {
    plan: [{ field: 'name', before: 'Existing plan', after: 'Renamed plan' }], services: [], accounts: { added: [], removed: [] },
    steps: [
      { step_number: 2, before_date: '2026-11-05', after_date: null, change: 'canceled', old_execution_id: 'old-2', approval_effect: 'none' },
      { step_number: 3, before_date: '2026-11-09', after_date: '2026-11-12', change: 'moved', old_execution_id: 'old-3', approval_effect: 'reapproval_required' },
    ],
    totals: { moved: 1, created: 0, canceled: 1, approvals_affected: 1 },
  },
};
const open = async (page: Page): Promise<void> => {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
  await seedAuth(page); await mockApi(page);
  const base = editorResponse(), s2 = base.graph!.steps[1]!, plan = { id: 'draft-plan', name: 'Existing plan', enabled: true, services: {}, ramp_schedule: base.plan.ramp_schedule };
  const editor = { ...base, plan: { ...base.plan, id: plan.id, name: plan.name },
    graph: { ...base.graph!, steps: base.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  await page.route('**/api/plans**', route => {
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({ json: path.endsWith('/accounts') ? [] : path.endsWith('/preview') ? preview : path.endsWith('/draft-plan/editor') ? editor : { plans: [plan] } });
  });
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.goto('/plans');
};
const inViewport = async (page: Page, selector: string): Promise<void> => {
  const box = (await page.locator(selector).boundingBox())!, view = page.viewportSize()!;
  expect(box.y).toBeGreaterThanOrEqual(0); expect(box.y + box.height).toBeLessThanOrEqual(view.height);
  expect(box.x).toBeGreaterThanOrEqual(0); expect(box.x + box.width).toBeLessThanOrEqual(view.width);
};

for (const width of [1440, 320]) test.describe(`viewport ${width}px`, () => {
  test.use({ isMobile: width <= 390 });
  test.beforeEach(async ({ page }) => { await page.setViewportSize({ width, height: 900 }); });

  test('review panel requires every acknowledgment and is visible on arrival', async ({ page }, testInfo) => {
    await open(page);
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    await page.locator('#plan-name').fill('Renamed plan');
    await page.locator('#plan-modal button[type="submit"]').click();
    const confirm = page.locator('#plan-review-confirm');
    await expect(page.locator('#plan-review-heading')).toBeFocused();
    await expect(confirm).toBeDisabled();
    await inViewport(page, '#plan-review-heading');
    await inViewport(page, '#plan-review-confirm'); await inViewport(page, '#plan-review [role="status"]');
    const head = (await page.locator('#plan-review-heading').boundingBox())!, scroller = (await page.locator('.plan-editor-layout').boundingBox())!;
    expect(head.y).toBeGreaterThanOrEqual(scroller.y - 1); expect(head.y + head.height).toBeLessThanOrEqual(scroller.y + scroller.height);
    await expect(page.locator('#plan-review-heading')).toBeFocused();
    const boxes = page.locator('#plan-review input[type="checkbox"]');
    await expect(boxes).toHaveCount(2);
    await expect(page.getByLabel('I understand step 2 (2026-11-05 -> no date) will be canceled.')).toBeVisible();
    await page.locator('#plan-review fieldset').first().scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('review-unacknowledged.png') });
    await boxes.first().focus(); await page.keyboard.press('Space');
    await expect(confirm).toBeDisabled();
    await boxes.nth(1).check();
    await expect(confirm).toBeEnabled();
    await expect(page.locator('#plan-review [role="status"]')).toHaveText('Ready to save.');
    await inViewport(page, '#plan-review-confirm'); await inViewport(page, '#plan-review [role="status"]'); await inViewport(page, '#plan-review input[type="checkbox"] >> nth=1');
    await page.screenshot({ path: testInfo.outputPath('review-acknowledged.png') });
    const status = (await page.locator('#plan-review [role="status"]').boundingBox())!, footer = (await page.locator('#plan-review-feedback').boundingBox())!;
    expect(status.y + status.height).toBeLessThanOrEqual(footer.y); // not clipped by the scroller edge
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.locator('#plan-name').fill('Another name');
    await expect(page.locator('#plan-review')).toHaveCount(0);
  });
});
