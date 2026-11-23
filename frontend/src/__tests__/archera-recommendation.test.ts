/**
 * Per-recommendation Archera quote control (issue #799). The insurance API is
 * mocked; fixtures are synthetic and prove the render path only.
 */
import {
  attachArcheraQuoteControls, resetArcheraRecommendation, failureMessage,
  NO_QUOTE, MSG_AMBIGUOUS, MSG_INELIGIBLE, MSG_NOT_FOUND, MSG_GENERIC,
} from '../archera-recommendation';
import { getInsuranceRecommendation, getInsuranceStatus } from '../api/insurance';
import type { ArcheraRecommendationComparison, ArcheraRecommendationOffer } from '../api/types';

jest.mock('../api/insurance', () => ({
  getInsuranceStatus: jest.fn(),
  getInsuranceRecommendation: jest.fn(),
}));

const status = getInsuranceStatus as jest.Mock;
const quote = getInsuranceRecommendation as jest.Mock;

const HOSTILE = '<img src=x onerror=window.__xss=1>';

function offer(over: Partial<ArcheraRecommendationOffer> = {}): ArcheraRecommendationOffer {
  return {
    offer_id: 'offer-1', provider: 'aws', commitment_type: 'savings_plan',
    hourly_selected_commitment: '1.2500', native_duration_seconds: '31536000',
    native_plan_type: 'ec2_instance', native_instance_family: 'm5',
    effective_contract_term: '1yr', payment_option: 'no_upfront', lease_attached: false,
    monthly: {
      monthly_730h_commitment_cost_total: '912.50', monthly_730h_cloud_provider_cost: '900.00',
      monthly_730h_archera_premium: '12.50', monthly_730h_gross_savings: '300.10',
      monthly_730h_net_savings: '287.60', monthly_730h_covered_on_demand_cost: null,
    },
    upfront_one_time_cost: '0', ...over,
  };
}

function response(over: Partial<ArcheraRecommendationComparison> = {}): ArcheraRecommendationComparison {
  return {
    recommendation_id: 'rec-a', status: 'resource_associated', reason: 'identity_unproven',
    amount_provenance: 'stored_decimal_only', economic_status: 'comparison_basis_unproven',
    fetched_at: '2026-11-23T10:00:00Z', currency: null, currency_note: 'Currency is not stated.',
    comparable_delta: null, premium_included: true, current: offer(), candidates: [],
    non_gating_disclosure: 'NG disclosure text', sponsorship_disclosure: 'Sponsorship disclosure text',
    ...over,
  };
}

function mount(ids: string[] = ['rec-a', 'rec-b']): HTMLElement {
  document.body.innerHTML = '<table><tbody></tbody></table>';
  const root = document.body.querySelector('table') as HTMLElement;
  const body = root.querySelector('tbody')!;
  for (const id of ids) {
    const tr = document.createElement('tr');
    tr.className = 'recommendation-row';
    tr.dataset['recId'] = id;
    tr.innerHTML = '<td class="checkbox-col"><span class="rec-select-cell"></span></td><td>x</td>';
    body.appendChild(tr);
  }
  return root;
}

async function flush(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

async function setup(configured = true, ids?: string[]): Promise<HTMLElement> {
  status.mockResolvedValue({ configured, missing: [] });
  const root = mount(ids);
  await attachArcheraQuoteControls(root);
  return root;
}

const btn = (root: HTMLElement, id: string) =>
  root.querySelector<HTMLButtonElement>(`tr[data-rec-id="${id}"] .rec-archera-btn`)!;
const live = (root: HTMLElement) => root.querySelector('[role="status"]')!;

beforeEach(() => {
  resetArcheraRecommendation();
  status.mockReset();
  quote.mockReset();
  delete (window as unknown as Record<string, unknown>)['__xss'];
});

describe('gate', () => {
  it.each([
    ['configured=false', () => status.mockResolvedValue({ configured: false, missing: ['X'] })],
    ['status 404', () => status.mockRejectedValue(Object.assign(new Error('nf'), { status: 404 }))],
    ['status network error', () => status.mockRejectedValue(new Error('net'))],
  ])('%s: no button and no quote request', async (_n, arrange) => {
    arrange();
    const root = mount();
    await attachArcheraQuoteControls(root);
    expect(root.querySelectorAll('.rec-archera-btn')).toHaveLength(0);
    expect(quote).not.toHaveBeenCalled();
  });

  it('configured: one named button per row and no request on load', async () => {
    const root = await setup();
    expect(root.querySelectorAll('.rec-archera-btn')).toHaveLength(2);
    expect(btn(root, 'rec-a').getAttribute('aria-label')).toMatch(/Archera quote/);
    expect(quote).not.toHaveBeenCalled();
    await attachArcheraQuoteControls(root);
    expect(root.querySelectorAll('.rec-archera-btn')).toHaveLength(2);
    expect(status).toHaveBeenCalledTimes(1);
  });
});

describe('responses', () => {
  it('200 with figures: insured figures and both disclosures', async () => {
    const root = await setup();
    quote.mockResolvedValue(response({ candidates: [
      { offer_id: 'alt-1', status: 'vendor_alternative', reason: 'candidate_basis_unproven', native_identity_status: 'unavailable', figures: offer({ offer_id: 'alt-1' }) },
      { offer_id: 'alt-2', status: 'unavailable', reason: 'x', native_identity_status: 'unavailable', figures: null },
    ] }));
    btn(root, 'rec-a').click();
    await flush();
    expect(quote).toHaveBeenCalledWith('rec-a', expect.any(AbortSignal));
    const panel = root.querySelector('.rec-archera-panel')!;
    expect(panel.textContent).toContain('NG disclosure text');
    expect(panel.textContent).toContain('Sponsorship disclosure text');
    expect(panel.textContent).toContain('912.50');
    expect(panel.textContent).toContain('287.60');
    expect(panel.textContent).toContain('Unknown');
    expect(panel.textContent).toContain('Alternative alt-1');
    expect(panel.textContent).toContain('No figures');
    expect(panel.textContent).not.toContain(NO_QUOTE);
    expect(live(root).textContent).toBe('Archera quote loaded');
  });

  it('200 unavailable: "No Archera quote" and no figures even if the body carries some', async () => {
    const root = await setup();
    quote.mockResolvedValue(response({ status: 'unavailable', reason: 'identity_unproven' }));
    btn(root, 'rec-a').click();
    await flush();
    const panel = root.querySelector('.rec-archera-panel')!;
    expect(panel.textContent).toContain(NO_QUOTE);
    expect(panel.textContent).not.toContain('912.50');
    expect(panel.querySelector('table')).toBeNull();
  });

  it.each([
    ['409', 409, MSG_AMBIGUOUS],
    ['422', 422, MSG_INELIGIBLE],
    ['404', 404, MSG_NOT_FOUND],
    ['400', 400, MSG_GENERIC],
    ['429', 429, MSG_GENERIC],
    ['500', 500, MSG_GENERIC],
    ['502', 502, MSG_GENERIC],
    ['network (0)', 0, MSG_GENERIC],
  ])('%s: fixed message, no figures, server text never shown', async (_n, code, msg) => {
    const root = await setup();
    quote.mockRejectedValue(Object.assign(new Error(`vendor says ${HOSTILE} secret`), { status: code }));
    btn(root, 'rec-a').click();
    await flush();
    const panel = root.querySelector('.rec-archera-panel')!;
    expect(live(root).textContent).toBe(msg);
    expect(panel.textContent).not.toContain('vendor says');
    expect(panel.querySelector('table, img')).toBeNull();
    expect(btn(root, 'rec-a').disabled).toBe(false);
  });

  it('error without a status and unknown 200 status are generic', async () => {
    expect(failureMessage(new Error('x'))).toBe(MSG_GENERIC);
    expect(failureMessage(null)).toBe(MSG_GENERIC);
    const root = await setup();
    quote.mockResolvedValue(response({ status: 'something_new' }));
    btn(root, 'rec-a').click();
    await flush();
    expect(live(root).textContent).toBe(MSG_GENERIC);
    expect(root.querySelector('.rec-archera-panel table')).toBeNull();
  });

  it('resource_associated without a current offer is generic, never blank figures', async () => {
    const root = await setup();
    quote.mockResolvedValue(response({ current: null }));
    btn(root, 'rec-a').click();
    await flush();
    expect(live(root).textContent).toBe(MSG_GENERIC);
  });

  it('renders hostile strings in any field as text', async () => {
    const root = await setup();
    const o = offer({ offer_id: HOSTILE, provider: HOSTILE, effective_contract_term: HOSTILE });
    o.monthly.monthly_730h_net_savings = HOSTILE;
    quote.mockResolvedValue(response({
      current: o, currency_note: HOSTILE, non_gating_disclosure: HOSTILE, fetched_at: HOSTILE,
      candidates: [{ offer_id: HOSTILE, status: HOSTILE, reason: '', native_identity_status: '', figures: null }],
    }));
    btn(root, 'rec-a').click();
    await flush();
    const panel = root.querySelector('.rec-archera-panel')!;
    expect(panel.querySelector('img')).toBeNull();
    expect(panel.textContent).toContain(HOSTILE);
    expect((window as unknown as Record<string, unknown>)['__xss']).toBeUndefined();
  });
});

describe('interaction', () => {
  it('shows loading, disables the button in flight', async () => {
    const root = await setup();
    let resolve!: (v: ArcheraRecommendationComparison) => void;
    quote.mockReturnValue(new Promise<ArcheraRecommendationComparison>(r => { resolve = r; }));
    const b = btn(root, 'rec-a');
    b.click();
    expect(quote).toHaveBeenCalledTimes(1);
    expect(b.disabled).toBe(true);
    expect(b.getAttribute('aria-busy')).toBe('true');
    expect(live(root).textContent).toBe('Loading Archera quote');
    expect(live(root).getAttribute('aria-live')).toBe('polite');
    resolve(response());
    await flush();
    expect(b.disabled).toBe(false);
    expect(b.hasAttribute('aria-busy')).toBe(false);
    expect(document.activeElement).toBe(b);
  });

  it('a second click closes without refetching; aria-expanded and focus follow', async () => {
    const root = await setup();
    quote.mockResolvedValue(response());
    const b = btn(root, 'rec-a');
    expect(b.getAttribute('aria-expanded')).toBe('false');
    b.click();
    await flush();
    expect(b.getAttribute('aria-expanded')).toBe('true');
    expect(root.querySelector(`#${b.getAttribute('aria-controls')}`)).not.toBeNull();
    b.click();
    expect(quote).toHaveBeenCalledTimes(1);
    expect(root.querySelector('.rec-archera-row')).toBeNull();
    expect(b.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(b);
    b.click();
    expect(quote).toHaveBeenCalledTimes(2);
  });

  it.each([['button', 'btn'], ['panel', 'panel']])('Escape on the %s aborts an in-flight request and closes', async (_n, where) => {
    const root = await setup();
    quote.mockReturnValue(new Promise(() => undefined));
    const b = btn(root, 'rec-a');
    b.click();
    const signal = quote.mock.calls[0][1] as AbortSignal;
    const target = where === 'btn' ? b : root.querySelector('.rec-archera-row')!;
    target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(signal.aborted).toBe(true);
    expect(root.querySelector('.rec-archera-row')).toBeNull();
    expect(b.getAttribute('aria-expanded')).toBe('false');
    expect(b.disabled).toBe(false);
    expect(document.activeElement).toBe(b);
    expect(quote).toHaveBeenCalledTimes(1);
  });

  it.each([['editable', '<td class="checkbox-col"><span class="rec-select-cell"></span></td>'], ['viewer', '<td class="checkbox-col"></td>']])(
    'panel colSpan equals the real column count (%s row)', async (_n, first) => {
      const root = await setup();
      root.querySelector('tbody')!.innerHTML =
        `<tr class="recommendation-row" data-rec-id="rec-c">${first}${'<td>x</td>'.repeat(6)}</tr>`;
      await attachArcheraQuoteControls(root);
      quote.mockResolvedValue(response());
      btn(root, 'rec-c').click();
      const row = root.querySelector('tr[data-rec-id="rec-c"]')!;
      expect((row.nextElementSibling!.firstElementChild as HTMLTableCellElement).colSpan).toBe(7);
    });

  it('empty-string money is Unknown; odd decimals stay inert verbatim text', async () => {
    const root = await setup();
    const o = offer();
    Object.assign(o.monthly, { monthly_730h_net_savings: '', monthly_730h_gross_savings: '1e999', monthly_730h_archera_premium: '-1', monthly_730h_cloud_provider_cost: '<b>1</b>' });
    quote.mockResolvedValue(response({ current: o }));
    btn(root, 'rec-a').click();
    await flush();
    const rows = Array.from(root.querySelectorAll('.rec-archera-panel tr')).map(r => r.textContent);
    expect(rows).toContain('Net savings (monthly)Unknown');
    expect(rows).toContain('Gross savings (monthly)1e999');
    expect(rows).toContain('Archera premium (monthly)-1');
    expect(rows).toContain('Cloud provider cost (monthly)<b>1</b>');
    expect(root.querySelector('.rec-archera-panel b')).toBeNull();
  });

  it('a slow response for A does not overwrite the view after the user moved to B', async () => {
    const root = await setup();
    let resolveA!: (v: ArcheraRecommendationComparison) => void;
    quote.mockImplementationOnce(() => new Promise<ArcheraRecommendationComparison>(r => { resolveA = r; }));
    btn(root, 'rec-a').click();
    const panelA = root.querySelector('.rec-archera-panel')!;
    const signalA = quote.mock.calls[0][1] as AbortSignal;
    quote.mockResolvedValueOnce(response({ status: 'unavailable' }));
    btn(root, 'rec-b').click();
    await flush();
    expect(signalA.aborted).toBe(true);
    expect(root.querySelectorAll('.rec-archera-panel')).toHaveLength(1);
    resolveA(response({ current: offer({ offer_id: 'A-OFFER' }) }));
    await flush();
    const panel = root.querySelector('.rec-archera-panel')!;
    expect(panel.textContent).toContain(NO_QUOTE);
    expect(panel.textContent).not.toContain('A-OFFER');
    expect(root.querySelector('tr[data-rec-id="rec-b"] + .rec-archera-row')).not.toBeNull();
    expect(btn(root, 'rec-a').disabled).toBe(false);
    expect(panelA.textContent).not.toContain('A-OFFER');
    expect(document.activeElement).not.toBe(btn(root, 'rec-a'));
  });

  it('a re-render that drops the panel aborts the request', async () => {
    const root = await setup();
    quote.mockReturnValue(new Promise(() => undefined));
    btn(root, 'rec-a').click();
    const signal = quote.mock.calls[0][1] as AbortSignal;
    root.querySelector('.rec-archera-row')!.remove();
    await attachArcheraQuoteControls(root);
    expect(signal.aborted).toBe(true);
  });
});
