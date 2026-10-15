import { test, expect } from '@playwright/test';
import { mockApi, seedAuth, RECS } from './fixtures/recs';

test('readonly stored row opens native Archera figures without selecting or purchasing', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await seedAuth(page);
  const { calls } = await mockApi(page);
  await page.route('**/api/auth/me', route => route.fulfill({ json: { id: 'viewer', groups: [], email: 'viewer@example.com' } }));
  await page.route('**/api/auth/me/permissions', route => route.fulfill({ json: {
    permissions: [{ action: 'view', resource: 'recommendations' }], is_admin: false,
  } }));
  await page.route(/\/api\/recommendations(?:\?.*)?$/, route => route.fulfill({ json: {
    recommendations: [{ ...RECS[0], id: 'stored/id', service: 'savings-plans-ec2instance', resource_type: 'm7i' }],
    summary: {}, regions: ['us-east-1'],
  } }));
  await page.route('**/api/insurance/status', route => route.fulfill({ json: { configured: true, missing: [] } }));
  const comparisonCalls: string[] = [];
  const figures = {
    offer_id: '<img src=x onerror=window.__archeraXss=1>', provider: 'aws', commitment_type: 'savings_plan',
    hourly_selected_commitment: '0.500000000000000001', native_duration_seconds: '31536000',
    native_plan_type: 'EC2Instance', native_instance_family: 'm7i', effective_contract_term: 'one_year_gris',
    payment_option: 'no_upfront', lease_attached: false, upfront_one_time_cost: '1200',
    monthly: { monthly_730h_commitment_cost_total: '110', monthly_730h_cloud_provider_cost: '100',
      monthly_730h_archera_premium: '10', monthly_730h_gross_savings: null,
      monthly_730h_net_savings: '20', monthly_730h_covered_on_demand_cost: '130' },
  };
  await page.route('**/api/insurance/comparison?*', route => {
    comparisonCalls.push(route.request().url());
    return route.fulfill({ json: {
      recommendation_id: 'stored/id', status: 'resource_associated', reason: '',
      amount_provenance: 'stored_decimal_only', economic_status: 'unavailable_source_precision',
      fetched_at: '2026-10-15T12:00:00Z', currency: null, comparable_delta: null,
      currency_note: 'Vendor currency unavailable', premium_included: true, current: figures,
      candidates: [{ offer_id: 'alternative', status: 'vendor_alternative', reason: '',
        native_identity_status: 'unavailable', figures: { ...figures, native_duration_seconds: null,
          native_plan_type: null, native_instance_family: null, lease_attached: true, upfront_one_time_cost: '2400' } }],
      non_gating_disclosure: 'Purchases do not depend on Archera.', sponsorship_disclosure: 'Sponsored by Archera.',
    } });
  });
  await page.goto('/opportunities');
  const button = page.getByRole('button', { name: 'Archera alternatives' });
  await expect(button).toBeVisible();
  expect(comparisonCalls).toEqual([]);
  await expect(page.getByRole('checkbox', { name: 'Select recommendation', exact: true })).toHaveCount(0);
  await button.click();
  const dialog = page.getByRole('dialog', { name: 'Archera recommendation alternatives' });
  await expect(dialog).toContainText('0.500000000000000001');
  await expect(dialog).toContainText('one_year_gris');
  await expect(dialog).toContainText('2400');
  await expect(dialog).toContainText('native product identity is unavailable');
  await expect(dialog).toContainText('Currency: unknown. Comparable cost difference: unavailable.');
  await expect(dialog).toContainText('Original provider precision is unproven');
  await expect(dialog).toContainText('Sponsored by Archera.');
  await expect(dialog.locator('img')).toHaveCount(0);
  expect(new URL(comparisonCalls[0]!).searchParams.get('recommendation_id')).toBe('stored/id');
  expect(comparisonCalls).toHaveLength(1);
  expect(calls.filter(call => call.method !== 'GET')).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __archeraXss?: number }).__archeraXss)).toBeUndefined();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await dialog.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(button).toBeFocused();
  expect(errors).toEqual([]);
});
