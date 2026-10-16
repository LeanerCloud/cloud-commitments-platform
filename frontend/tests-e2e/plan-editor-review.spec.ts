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
const puts: string[] = [];
let failEditorGets = false;
const mapping = [{ step_number: 2, old_execution_id: 'old-2', new_execution_id: null, scheduled_date: null }, { step_number: 3, old_execution_id: 'old-3', new_execution_id: 'new-3', scheduled_date: '2026-11-12' }];
const open = async (page: Page): Promise<void> => {
  failEditorGets = false;
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
  await seedAuth(page); await mockApi(page);
  const base = editorResponse(), s2 = base.graph!.steps[1]!, plan = { id: 'draft-plan', name: 'Existing plan', enabled: true, services: {}, ramp_schedule: base.plan.ramp_schedule };
  const editor = { ...base, plan: { ...base.plan, id: plan.id, name: plan.name },
    graph: { ...base.graph!, steps: base.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  await page.route('**/api/plans**', route => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === 'PUT' && path.endsWith('/draft-plan/editor')) { puts.push(route.request().postData() ?? ''); return route.fulfill({ json: { replayed: false, mapping, diff: preview.diff, editor } }); }
    if (failEditorGets && path.endsWith('/draft-plan/editor')) return route.fulfill({ status: 500, json: { error: 'down', code: 'internal' } });
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
    const footerButton = page.locator('#plan-modal button[type="submit"]');
    await expect(page.locator('#plan-review-heading')).toBeFocused();
    await expect(footerButton).toHaveText('Review changes');
    await inViewport(page, '#plan-review-heading');
    await inViewport(page, '#plan-review [role="status"]');
    const head = (await page.locator('#plan-review-heading').boundingBox())!, scroller = (await page.locator('.plan-editor-layout').boundingBox())!;
    expect(head.y).toBeGreaterThanOrEqual(scroller.y - 1); expect(head.y + head.height).toBeLessThanOrEqual(scroller.y + scroller.height);
    await expect(page.locator('#plan-review-heading')).toBeFocused();
    const boxes = page.locator('#plan-review input[type="checkbox"]');
    await expect(boxes).toHaveCount(2);
    await expect(page.getByLabel('I understand step 2 (2026-11-05 -> no date) will be canceled.')).toBeVisible();
    await page.locator('#plan-review fieldset').first().scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('review-unacknowledged.png') });
    await boxes.first().focus(); await page.keyboard.press('Space');
    await expect(footerButton).toHaveText('Review changes');
    await boxes.nth(1).check();
    await expect(footerButton).toHaveText('Save changes'); await expect(footerButton).toBeEnabled(); await inViewport(page, '#plan-modal button[type="submit"]');
    await expect(page.locator('#plan-review [role="status"]')).toHaveText('Ready to save. Use Save changes.');
    await inViewport(page, '#plan-review [role="status"]'); await inViewport(page, '#plan-review input[type="checkbox"] >> nth=1');
    await page.screenshot({ path: testInfo.outputPath('review-acknowledged.png') });
    const status = (await page.locator('#plan-review [role="status"]').boundingBox())!, footer = (await page.locator('#plan-review-feedback').boundingBox())!;
    expect(status.y + status.height).toBeLessThanOrEqual(footer.y); // not clipped by the scroller edge
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    puts.length = 0;
    await footerButton.focus(); await page.keyboard.press('Enter');
    await expect(page.locator('#plan-review-feedback [role="status"]')).toHaveText('Saved. 1 date moved; 1 step removed; 1 approval affected.');
    await inViewport(page, '#plan-review-feedback [role="status"]');
    await expect(page.locator('#plan-review-mapping')).toHaveText('2 purchases changed: step 2 canceled; step 3 replaced, now 2026-11-12.');
    await inViewport(page, '#plan-review-mapping');
    expect(await page.locator('#plan-review-feedback').evaluate(el => el.scrollHeight <= el.clientHeight)).toBe(true);
    expect(puts).toHaveLength(1);
    expect(JSON.parse(puts[0]!)).toMatchObject({ reviewed_diff: preview.reviewed_diff, acknowledged_cancellation_ids: ['old-2'], acknowledged_reapproval_ids: ['old-3'], plan: { name: 'Renamed plan' } });
    await expect(page.locator('#plan-review')).toHaveCount(0);
    await expect(page.locator('.plan-editor-layout')).not.toHaveAttribute('inert', /.*/);
    await page.screenshot({ path: testInfo.outputPath('saved.png') });
  });

  test('a failed reload after a successful Save keeps the result, offers Reload and remounts on retry', async ({ page }, testInfo) => {
    await open(page);
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    await page.locator('#plan-name').fill('Renamed plan');
    const footerButton = page.locator('#plan-modal button[type="submit"]');
    await footerButton.click();
    await page.locator('#plan-review input[type="checkbox"]').first().check(); await page.locator('#plan-review input[type="checkbox"]').nth(1).check();
    failEditorGets = true;
    await footerButton.click();
    await expect(page.locator('#plan-review-feedback [role="alert"]')).toHaveText('Saved, but reloading failed. Reload to keep editing.');
    const reload = page.locator('#plan-review-reload');
    await inViewport(page, '#plan-review-feedback [role="alert"]'); await inViewport(page, '#plan-review-reload');
    await expect(page.locator('#plan-review-feedback [role="status"]')).toHaveText(/^Saved\./);
    expect(await page.locator('#plan-review-feedback').evaluate(el => el.scrollHeight <= el.clientHeight)).toBe(true); // nothing clipped by the cap
    await expect(footerButton).toBeDisabled();
    await expect(page.locator('.plan-editor-layout')).toHaveAttribute('inert', '');
    await page.screenshot({ path: testInfo.outputPath('reload-failed.png') });
    failEditorGets = false;
    await reload.focus(); await page.keyboard.press('Enter');
    await expect(page.locator('#plan-review-reload')).toHaveCount(0);
    await expect(page.locator('.plan-editor-layout')).not.toHaveAttribute('inert', /.*/);
    await expect(page.locator('#plan-name')).toBeEnabled();
    await expect(page.locator('#plan-review-feedback [role="status"]')).toHaveText(/^Saved\./);
  });

  test('Escape closes the Edit dialog', async ({ page }) => {
    await open(page);
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    await expect(page.locator('#plan-modal')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('#plan-modal')).toBeHidden();
  });
});
