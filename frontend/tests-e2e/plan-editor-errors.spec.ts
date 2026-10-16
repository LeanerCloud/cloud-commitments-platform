import { test, expect, Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';
import { response as editorResponse } from '../src/__tests__/fixtures/plan-editor';

const diff = { plan: [{ field: 'name', before: 'Existing plan', after: 'Renamed plan' }], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 0, created: 0, canceled: 0, approvals_affected: 0 } };
const longLock = `Step 2 is already running, so its date is fixed. ${'The server explains the lock at length. '.repeat(60)}`;
type Refusal = { status: number; code: string; error: string };
const open = async (page: Page, refusals: Refusal[], puts: string[]): Promise<void> => {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
  await seedAuth(page); await mockApi(page);
  const base = editorResponse(), plan = { id: 'draft-plan', name: 'Existing plan', enabled: true, services: {}, ramp_schedule: base.plan.ramp_schedule };
  const s2 = base.graph!.steps[1]!, editor = { ...base, plan: { ...base.plan, id: plan.id, name: plan.name },
    graph: { ...base.graph!, steps: base.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  await page.route('**/api/plans**', route => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === 'PUT' && path.endsWith('/draft-plan/editor')) {
      puts.push(route.request().postData() ?? '');
      const refusal = refusals.shift();
      return refusal ? route.fulfill({ status: refusal.status, json: { error: refusal.error, code: refusal.code } }) : route.fulfill({ json: { replayed: false, mapping: [], diff, editor } });
    }
    return route.fulfill({ json: path.endsWith('/accounts') ? [] : path.endsWith('/preview') ? { reviewed_diff: 'sha256:' + 'c'.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [], diff } : path.endsWith('/draft-plan/editor') ? editor : { plans: [plan] } });
  });
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.goto('/plans');
  await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
  await page.locator('#plan-name').fill('Renamed plan');
  await page.locator('#plan-modal button[type="submit"]').click();
  await expect(page.locator('#plan-modal button[type="submit"]')).toHaveText('Save changes');
};

for (const width of [1440, 320]) test.describe(`viewport ${width}px`, () => {
  test.use({ isMobile: width <= 390 });
  test.beforeEach(async ({ page }) => { await page.setViewportSize({ width, height: 900 }); });

  test('a long locked-field error stays reachable in the footer, scrolls by keyboard and offers a reload', async ({ page }, testInfo) => {
    const puts: string[] = [];
    await open(page, [{ status: 409, code: 'editor_field_locked', error: longLock }], puts);
    const footer = page.locator('#plan-modal button[type="submit"]'), feedback = page.locator('#plan-review-feedback');
    await footer.click();
    await expect(feedback.locator('[role="alert"]')).toContainText('Step 2 is already running');
    await expect(page.locator('#plan-review-reload')).toHaveText('Reload changes');
    await expect(footer).toBeVisible();
    expect(await feedback.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true);
    await feedback.focus(); await page.keyboard.press('PageDown');
    await expect.poll(() => feedback.evaluate(el => el.scrollTop)).toBeGreaterThan(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('long-locked-error.png') });
    await page.locator('#plan-review-reload').click();
    await expect(page.locator('#plan-review-feedback [role="status"]')).toContainText('Not applied: changes to plan settings');
  });

  test('a busy answer keeps the request: the retry resends the same bytes and succeeds', async ({ page }, testInfo) => {
    const puts: string[] = [];
    await open(page, [{ status: 503, code: 'editor_busy', error: 'busy' }], puts);
    const footer = page.locator('#plan-modal button[type="submit"]');
    await footer.click();
    await expect(page.locator('#plan-review-feedback [role="alert"]')).toContainText('The server is busy');
    await expect(footer).toBeEnabled(); await expect(footer).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('busy-error.png') });
    await footer.click();
    await expect(page.locator('#plan-review-feedback [role="status"]')).toContainText('Saved.');
    expect(puts).toHaveLength(2); expect(puts[1]).toBe(puts[0]);
  });
});
