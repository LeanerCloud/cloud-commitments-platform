import { mountRecommendationArchera } from '../recommendation-archera-comparison';
import { getInsuranceStatus, getRecommendationInsuranceComparison } from '../api/insurance';
import type { RecommendationArcheraComparison, RecommendationArcheraOffer } from '../api/types';
import { canAccess } from '../permissions';

jest.mock('../api/insurance', () => ({
  getInsuranceStatus: jest.fn(), getRecommendationInsuranceComparison: jest.fn(),
}));
jest.mock('../permissions', () => ({ canAccess: jest.fn() }));

const figures: RecommendationArcheraOffer = {
  offer_id: '<img src=x onerror=alert(1)>', provider: 'aws', commitment_type: 'savings_plan',
  hourly_selected_commitment: '0.500000000000000001', native_duration_seconds: '31536000',
  native_plan_type: 'EC2Instance', native_instance_family: 'm7i', effective_contract_term: 'one_year_gris',
  payment_option: 'no_upfront', lease_attached: false, upfront_one_time_cost: '1200',
  monthly: {
    monthly_730h_commitment_cost_total: '110', monthly_730h_cloud_provider_cost: '100',
    monthly_730h_archera_premium: '10', monthly_730h_gross_savings: null,
    monthly_730h_net_savings: '20', monthly_730h_covered_on_demand_cost: '130',
  },
};
const result: RecommendationArcheraComparison = {
  recommendation_id: 'stored/id', status: 'resource_associated', reason: '',
  amount_provenance: 'stored_decimal_only', economic_status: 'unavailable_source_precision',
  fetched_at: '2026-10-15T12:00:00Z', currency: null, comparable_delta: null,
  currency_note: 'Vendor currency unavailable', premium_included: true,
  current: figures, candidates: [
    { offer_id: 'same', status: 'vendor_alternative', reason: '', native_identity_status: 'unavailable',
      figures: { ...figures, native_duration_seconds: null, native_plan_type: null, native_instance_family: null } },
    { offer_id: 'same', status: 'unavailable', reason: 'amount_unproven', native_identity_status: 'unavailable', figures: null },
  ],
  non_gating_disclosure: 'Purchases do not depend on Archera.',
  sponsorship_disclosure: 'Sponsored by Archera.',
};
const flush = async (): Promise<void> => { await Promise.resolve(); await Promise.resolve(); };
const action = (): HTMLButtonElement => document.querySelector<HTMLButtonElement>('[data-action="archera"]')!;

beforeEach(() => {
  document.body.innerHTML = '<table><tbody><tr class="recommendation-row" data-rec-id="stored/id"><td></td></tr></tbody></table>';
  jest.clearAllMocks();
  (canAccess as jest.Mock).mockReturnValue(true);
  (getInsuranceStatus as jest.Mock).mockResolvedValue({ configured: true, missing: [] });
  (getRecommendationInsuranceComparison as jest.Mock).mockResolvedValue(result);
});

test('explicit row action uses the stored ID without comparison on mount or repeated mount', async () => {
  await mountRecommendationArchera(document.body);
  await mountRecommendationArchera(document.body);
  expect(document.querySelectorAll('[data-action="archera"]')).toHaveLength(1);
  expect(getRecommendationInsuranceComparison).not.toHaveBeenCalled();
  action().click();
  await flush();
  expect(getRecommendationInsuranceComparison).toHaveBeenCalledWith('stored/id');
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
});

test.each(['permission', 'unconfigured', 'status_error'])('%s does not expose the action or fetch comparison', async mode => {
  if (mode === 'permission') (canAccess as jest.Mock).mockReturnValue(false);
  if (mode === 'unconfigured') (getInsuranceStatus as jest.Mock).mockResolvedValue({ configured: false });
  if (mode === 'status_error') (getInsuranceStatus as jest.Mock).mockRejectedValue(new Error('offline'));
  await mountRecommendationArchera(document.body);
  expect(document.querySelector('button')).toBeNull();
  expect(getRecommendationInsuranceComparison).not.toHaveBeenCalled();
  if (mode === 'permission') expect(getInsuranceStatus).not.toHaveBeenCalled();
});

test('loading deduplicates across rerenders and closing never reopens the response', async () => {
  let resolve!: (value: RecommendationArcheraComparison) => void;
  (getRecommendationInsuranceComparison as jest.Mock).mockReturnValue(new Promise(r => { resolve = r; }));
  await mountRecommendationArchera(document.body);
  action().click();
  expect(action().disabled).toBe(true);
  expect(document.body.textContent).toContain('Loading Archera alternatives');
  const row = document.querySelector('tr')!;
  row.querySelector('button')!.remove();
  await mountRecommendationArchera(document.body);
  action().click();
  expect(getRecommendationInsuranceComparison).toHaveBeenCalledTimes(1);
  (document.querySelector('[role="dialog"] button') as HTMLButtonElement).click();
  resolve(result);
  await flush();
  expect(document.querySelector('[role="dialog"]')).toBeNull();
});

test('native exact figures, null economics, lease distinctions and disclosures remain text', async () => {
  await mountRecommendationArchera(document.body);
  action().click();
  await flush();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.querySelector('img')).toBeNull();
  for (const text of [figures.offer_id, '0.500000000000000001', '31536000', 'one_year_gris',
    '1200', '110', 'Unknown', 'Original provider precision is unproven', 'Currency: unknown',
    'Comparable cost difference: unavailable', result.non_gating_disclosure, result.sponsorship_disclosure]) {
    expect(dialog.textContent).toContain(text);
  }
  expect(dialog.textContent).not.toMatch(/\$|USD/);
  expect(dialog.querySelectorAll('h3')).toHaveLength(2);
  expect(dialog.textContent).toContain('Alternative 2: unavailable. amount unproven');
  expect(dialog.textContent).toContain('native product identity is unavailable');
});

test.each([[400, 'invalid'], [403, 'not authorised'], [404, 'no longer exists'], [409, 'changed or is ambiguous'],
  [422, 'cannot be compared'], [502, 'unavailable or returned'], [503, 'not available'], [504, 'did not respond']])(
'status %i shows a fixed message and never the response text', async (status, text) => {
  (getRecommendationInsuranceComparison as jest.Mock).mockRejectedValue(Object.assign(new Error('<img src=bad>'), { status }));
  await mountRecommendationArchera(document.body);
  action().click();
  await flush();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.textContent).toContain(text);
  expect(dialog.textContent).not.toContain('<img');
  expect(dialog.querySelectorAll('h3')).toHaveLength(0);
  expect(action().disabled).toBe(false);
});

test.each(['unmatched', 'error', 'wrong_id'])('%s never presents unrelated figures', async mode => {
  if (mode === 'unmatched') (getRecommendationInsuranceComparison as jest.Mock).mockResolvedValue({
    ...result, status: 'unavailable', reason: 'identity_unproven', current: null, candidates: [],
  });
  if (mode === 'error') (getRecommendationInsuranceComparison as jest.Mock).mockRejectedValue(new Error('<img src=bad>'));
  if (mode === 'wrong_id') (getRecommendationInsuranceComparison as jest.Mock).mockResolvedValue({ ...result, recommendation_id: 'another' });
  await mountRecommendationArchera(document.body);
  action().click();
  await flush();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.querySelectorAll('h3')).toHaveLength(0);
  expect(dialog.querySelector('img')).toBeNull();
  expect(action().disabled).toBe(false);
  if (mode === 'unmatched') expect(dialog.textContent).toContain('identity unproven');
  if (mode === 'error') expect(dialog.textContent).toContain('Archera alternatives unavailable.');
  if (mode === 'wrong_id') expect(dialog.textContent).toContain('changed or is ambiguous');
});

test('closing while loading restores focus without allowing a duplicate request', async () => {
  let resolve!: (value: RecommendationArcheraComparison) => void;
  (getRecommendationInsuranceComparison as jest.Mock).mockReturnValue(new Promise(r => { resolve = r; }));
  await mountRecommendationArchera(document.body);
  const button = action();
  button.focus();
  button.click();
  (document.querySelector('[role="dialog"] button') as HTMLButtonElement).click();
  expect(document.activeElement).toBe(button);
  button.click();
  expect(getRecommendationInsuranceComparison).toHaveBeenCalledTimes(1);
  resolve(result);
  await flush();
});

test('the module imports no purchase or plan API', () => {
  const source = jest.requireActual<typeof import('fs')>('fs').readFileSync(require.resolve('../recommendation-archera-comparison'), 'utf8');
  const imports = [...source.matchAll(/from '([^']+)'/g)].map(m => m[1]);
  expect(imports).toEqual(['./api/insurance', './api/types', './permissions', './modal']);
});
