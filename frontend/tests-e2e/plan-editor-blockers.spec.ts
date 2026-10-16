import { test, expect, Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';
import { response as editorResponse, service } from '../src/__tests__/fixtures/plan-editor';

const open = async (page: Page, services: Record<string, ReturnType<typeof service>> | null): Promise<void> => {
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
  await seedAuth(page); await mockApi(page);
  const base = editorResponse(), plan = { id: 'draft-plan', name: 'Existing plan', enabled: true, services: {}, ramp_schedule: base.plan.ramp_schedule };
  const editor = { ...base, plan: { ...base.plan, id: plan.id, name: plan.name, ...(services ? { services } : {}) } };
  await page.route('**/api/plans**', route => route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/accounts') ? []
    : new URL(route.request().url()).pathname.endsWith('/draft-plan/editor') ? editor : { plans: [plan] } }));
  await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
  await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
  await page.goto('/plans');
};
const feedbackAboveFold = async (page: Page): Promise<void> => {
  const reason = page.locator('#plan-review-feedback #plan-review-reason'), box = (await reason.boundingBox())!;
  expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize()!.height);
};

for (const width of [1440, 320]) test.describe(`viewport ${width}px`, () => {
  test.use({ isMobile: width <= 390 });
  test.beforeEach(async ({ page }) => { await page.setViewportSize({ width, height: 900 }); });

  test('Edit of a plan with an enabled unsupported service is read-only with a visible reason', async ({ page }, testInfo) => {
    await open(page, { first: { ...service(40), provider: 'azure', service: 'bogus', enabled: true } });
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    await expect(page.locator('#plan-review-feedback #plan-review-reason')).toContainText('azure/bogus');
    await expect(page.locator('#plan-modal button[type="submit"]')).toBeDisabled();
    await expect(page.locator('#plan-name')).toBeDisabled();
    await expect(page.locator('#plan-editor-panel [data-edit="date"]')).toBeDisabled();
    await feedbackAboveFold(page);
    await page.screenshot({ path: testInfo.outputPath('edit-unsupported.png') });
  });

  test('Edit of a Savings Plans plan shows the SP reason', async ({ page }, testInfo) => {
    await open(page, { first: { ...service(40), service: 'savingsplans', enabled: true } });
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    await expect(page.locator('#plan-review-feedback #plan-review-reason')).toContainText('Savings Plans cannot be laddered');
    await expect(page.locator('#plan-service-reason')).toContainText('Savings Plans are not supported yet');
    await feedbackAboveFold(page);
    await page.screenshot({ path: testInfo.outputPath('edit-savings-plans.png') });
  });

  test('Create shows the Savings Plans options disabled and the auto-purchase reason', async ({ page }, testInfo) => {
    await open(page, null);
    await page.locator('#new-plan-btn').click();
    await expect(page.locator('#plan-service option[value="savings-plans-compute"]')).toHaveJSProperty('disabled', true);
    await expect(page.locator('#plan-service option[value="savings-plans-compute"]')).toHaveText(/Savings Plans are not supported yet/);
    await page.locator('#plan-service').evaluate(select => { (select as HTMLSelectElement).size = 12; }); // a native popup is not capturable
    await page.locator('#plan-service').scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('create-service-select.png') });
    await page.locator('#plan-advanced-settings > summary').click();
    await expect(page.locator('#plan-auto-purchase')).toBeDisabled();
    await expect(page.locator('#plan-auto-purchase-reason')).toBeVisible();
    await page.locator('#plan-auto-purchase-reason').scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath('create-auto-purchase.png') });
  });
});
