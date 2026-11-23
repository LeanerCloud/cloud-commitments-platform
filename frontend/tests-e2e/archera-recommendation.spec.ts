/**
 * Per-recommendation Archera quote on Opportunities (issue #799).
 * Fixture-based: API responses are mocked with page.route, so this proves the
 * render path only. No live Archera calls.
 */
import { test, expect, type Page } from '@playwright/test';
import { mockApi, RECS, seedAuth } from './fixtures/recs';

const ROW = RECS[0]!.id;
const HOSTILE = '<img src=x onerror=window.__xss=1>';

const offer = {
  offer_id: HOSTILE, provider: 'aws', commitment_type: 'savings_plan',
  hourly_selected_commitment: '1.25', native_duration_seconds: '31536000',
  native_plan_type: null, native_instance_family: null, effective_contract_term: '1yr',
  payment_option: 'no_upfront', lease_attached: false, upfront_one_time_cost: '0',
  monthly: {
    monthly_730h_commitment_cost_total: '912.50', monthly_730h_cloud_provider_cost: '900.00',
    monthly_730h_archera_premium: '12.50', monthly_730h_gross_savings: '300.10',
    monthly_730h_net_savings: '287.60', monthly_730h_covered_on_demand_cost: null,
  },
};
const quote = {
  recommendation_id: ROW, status: 'resource_associated', reason: 'identity_unproven',
  amount_provenance: 'stored_decimal_only', economic_status: 'comparison_basis_unproven',
  fetched_at: '2026-11-23T10:00:00Z', currency: null, currency_note: 'Currency is not stated.',
  comparable_delta: null, premium_included: true, current: offer, candidates: [],
  non_gating_disclosure: 'Not a guarantee (fixture).', sponsorship_disclosure: 'Sponsorship (fixture).',
};

async function setup(page: Page, configured: boolean) {
  const calls: string[] = [];
  await seedAuth(page);
  await mockApi(page);
  await page.route('**/api/insurance/status', r => r.fulfill({ json: { configured, missing: [] } }));
  await page.route('**/api/insurance/recommendations/*', r => {
    calls.push(r.request().url());
    return r.fulfill({ json: quote });
  });
  await page.goto('/');
  await page.getByRole('link', { name: 'Opportunities', exact: true }).click();
  await expect(page.locator('tr.recommendation-row').first()).toBeVisible();
  return calls;
}

test('gate off: no button and no quote request', async ({ page }) => {
  const calls = await setup(page, false);
  await expect(page.locator('.rec-archera-btn')).toHaveCount(0);
  expect(calls).toEqual([]);
});

test('gate on: one request on click, figures and disclosures render as text', async ({ page }) => {
  const calls = await setup(page, true);
  const button = page.locator(`tr.recommendation-row[data-rec-id="${ROW}"] .rec-archera-btn`);
  await expect(button).toBeVisible();
  expect(calls).toEqual([]);
  await button.click();
  const panel = page.locator('.rec-archera-panel');
  await expect(panel.getByText('Not a guarantee (fixture).')).toBeVisible();
  await expect(panel.getByText('912.50')).toBeVisible();
  await expect(panel.getByText(HOSTILE).first()).toBeVisible();
  expect(calls).toHaveLength(1);
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
  await expect(button).toBeFocused();
});
