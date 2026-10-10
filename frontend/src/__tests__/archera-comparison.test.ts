/**
 * Archera comparison section (issue #785). The fixture is the Go DTO golden
 * file read from its Go path, so the UI is tested against the real shape.
 * Success rendering is therefore fixture-based; no live Archera calls.
 */
import * as fs from 'fs';
import * as path from 'path';
import { buildComparisonView, renderComparisonSection } from '../archera-comparison';
import { getInsuranceComparison, getInsuranceStatus } from '../api/insurance';
import type { ArcheraComparison, ArcheraOffer, ArcheraProductSupport } from '../api/types';

jest.mock('../api/insurance', () => ({
  getInsuranceStatus: jest.fn(),
  getInsuranceComparison: jest.fn(),
}));

const status = getInsuranceStatus as jest.Mock;
const comparison = getInsuranceComparison as jest.Mock;

const GOLDEN_PATH = path.resolve(__dirname, '../../../internal/archera/testdata/comparison.golden.json');
const golden = JSON.parse(fs.readFileSync(GOLDEN_PATH, 'utf8')) as ArcheraComparison;

// satisfies makes tsc fail when the TS DTO gains or loses a key; the
// runtime check below fails when the Go golden file drifts from the TS keys.
const TOP_KEYS = {
  title: 1, plan_id: 1, fetched_at: 1, currency: 1, currency_note: 1, premium_included: 1,
  basis_note: 1, delta_basis_note: 1, current: 1, hypotheticals: 1, rows: 1,
  non_gating_disclosure: 1, sponsorship_disclosure: 1,
} satisfies Record<keyof ArcheraComparison, 1>;
const SUPPORT_KEYS = {
  status: 1, source: 1, evidence: 1, underwriting_allowance: 1, customer_eligibility: 1,
} satisfies Record<keyof ArcheraProductSupport, 1>;

function clone(): ArcheraComparison {
  return JSON.parse(JSON.stringify(golden)) as ArcheraComparison;
}

function firstOffer(c: ArcheraComparison): ArcheraOffer {
  const row = c.rows[0];
  if (!row) throw new Error('golden has no rows');
  return row.current;
}

async function mount(): Promise<HTMLElement> {
  document.body.replaceChildren();
  const root = document.createElement('div');
  document.body.appendChild(root);
  await renderComparisonSection(root);
  return root;
}

async function clickAndLoad(root: HTMLElement, data: ArcheraComparison): Promise<void> {
  comparison.mockResolvedValueOnce(data);
  root.querySelector('button')!.click();
  await flush();
}

async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) await Promise.resolve();
}

beforeEach(() => {
  jest.resetAllMocks();
  status.mockResolvedValue({ configured: true, missing: [] });
});

describe('golden drift', () => {
  it('golden keys equal the TS DTO keys', () => {
    expect(Object.keys(golden).sort()).toEqual(Object.keys(TOP_KEYS).sort());
    const ps = firstOffer(golden).archera_product_support;
    expect(Object.keys(ps).sort()).toEqual(Object.keys(SUPPORT_KEYS).sort());
  });
});

describe('status gating', () => {
  it('configured=false: nothing rendered and no comparison request', async () => {
    status.mockResolvedValue({ configured: false, missing: ['ARCHERA_ORG_ID'] });
    const root = await mount();
    expect(root.innerHTML).toBe('');
    expect(comparison).not.toHaveBeenCalled();
  });

  it('404 (scoped session): nothing rendered and no comparison request', async () => {
    status.mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }));
    const root = await mount();
    expect(root.innerHTML).toBe('');
    expect(comparison).not.toHaveBeenCalled();
  });

  it.each([500, 0])('status error %s: one unavailable line, no comparison request', async code => {
    status.mockRejectedValue(Object.assign(new Error('boom'), { status: code }));
    const root = await mount();
    expect(root.textContent).toBe('Archera comparison status unavailable');
    expect(root.querySelector('button')).toBeNull();
    expect(comparison).not.toHaveBeenCalled();
  });

  it('configured: button only, no comparison request until clicked', async () => {
    const root = await mount();
    expect(root.querySelector('button')!.textContent).toBe('Compare with Archera');
    expect(comparison).not.toHaveBeenCalled();
  });
});

describe('rendering the golden response', () => {
  it('renders the DTO title, notes, both disclosures and fetched-at verbatim', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    expect(root.querySelector('h2')!.textContent).toBe(golden.title);
    const txt = root.textContent!;
    for (const s of [golden.currency_note, golden.basis_note, golden.delta_basis_note,
      golden.non_gating_disclosure, golden.sponsorship_disclosure,
      firstOffer(golden).archera_offer_name_note!]) {
      expect(txt).toContain(s);
    }
    expect(root.querySelector('.archera-fetched-at')!.textContent).toContain('Fetched at');
    expect(root.querySelector('.archera-fetched-at')!.textContent).toContain('9 Oct 2026');
    expect(txt).not.toContain('org_id');
  });

  it('renders disclosures from the response, not a hardcoded copy', async () => {
    const c = clone();
    c.non_gating_disclosure = 'DISCLOSURE-A-CHANGED';
    c.sponsorship_disclosure = 'DISCLOSURE-B-CHANGED';
    const root = await mount();
    await clickAndLoad(root, c);
    expect(root.textContent).toContain('DISCLOSURE-A-CHANGED');
    expect(root.textContent).toContain('DISCLOSURE-B-CHANGED');
    expect(root.textContent).not.toContain(golden.non_gating_disclosure);
  });

  it('shows no currency symbol, USD or dollar wording of its own', async () => {
    const c = clone();
    c.basis_note = 'one-time amounts';
    const root = await mount();
    await clickAndLoad(root, c);
    expect(root.textContent).not.toMatch(/\$|USD|dollar/i);
  });

  it('uses captions and column/row header scopes on every table', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    const tables = root.querySelectorAll('table');
    expect(tables.length).toBeGreaterThanOrEqual(3);
    tables.forEach(t => {
      expect(t.querySelector('caption')!.textContent).not.toBe('');
      t.querySelectorAll('th').forEach(th => expect(th.getAttribute('scope')).toMatch(/^(col|row)$/));
    });
  });
});

describe('money', () => {
  it('null renders Unknown, "0" stays 0, long decimals are verbatim', async () => {
    const c = clone();
    c.current.monthly_730h_net_savings = null;
    c.current.monthly_730h_gross_savings = '0';
    c.current.upfront_one_time_cost = '0.0000000000000000000000000001234567890123';
    const root = await mount();
    await clickAndLoad(root, c);
    const rows = Array.from(root.querySelectorAll('table')[0]!.querySelectorAll('tr'));
    const byLabel = (l: string) => rows.find(r => r.textContent!.startsWith(l))!.querySelector('td')!.textContent;
    expect(byLabel('Net savings')).toBe('Unknown');
    expect(byLabel('Gross savings')).toBe('0');
    expect(byLabel('Upfront cost')).toBe('0.0000000000000000000000000001234567890123');
  });
});

describe('vendor strings are text only', () => {
  it('hostile strings create no elements', async () => {
    const hostile = '<img src=x onerror=alert(1)><script>alert(2)</script>';
    const c = clone();
    const o = firstOffer(c);
    o.offer_id = hostile;
    o.region = hostile;
    o.commitment_type = hostile;
    o.archera_offer_name = hostile;
    o.archera_product_support.evidence = hostile;
    c.rows[0]!.line_item_id = hostile;
    c.hypotheticals[0]!.line_items[0]!.reason = hostile;
    const root = await mount();
    await clickAndLoad(root, c);
    expect(root.querySelector('img')).toBeNull();
    expect(root.querySelector('script')).toBeNull();
    expect(root.textContent).toContain(hostile);
  });
});

describe('product support', () => {
  it('supported shows source and evidence', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    const ps = firstOffer(golden).archera_product_support;
    expect(root.textContent).toContain('Supported (documented)');
    expect(root.textContent).toContain(ps.source!);
    expect(root.textContent).toContain(ps.evidence!);
  });

  it.each(['unknown', 'unsupported', '', 'SUPPORTED', 'garbage'])('status %p renders not-known and never "unsupported"', async st => {
    const c = clone();
    for (const r of c.rows) for (const o of [r.current, ...r.candidates]) {
      o.archera_product_support = {
        status: st, source: 'SRC-LEAK', evidence: 'EVD-LEAK',
        underwriting_allowance: 'allowed', customer_eligibility: 'eligible',
      };
    }
    const root = await mount();
    await clickAndLoad(root, c);
    const txt = root.textContent!;
    expect(txt).toContain('Not known to be supported by Archera');
    expect(txt).not.toMatch(/unsupported/i);
    expect(txt).not.toContain('SRC-LEAK');
    expect(txt).not.toContain('EVD-LEAK');
    expect(txt).not.toMatch(/allowed|eligible\b/);
    expect(txt).toContain('Underwriting allowance: Unknown');
    expect(txt).toContain('Customer eligibility: Unknown');
  });

  it('lease attached is labelled', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    expect(root.textContent).toContain('lease attached (lease-cur)');
  });
});

describe('refresh, errors and accessibility', () => {
  it('refresh re-requests once per click; in-flight clicks are ignored; nothing polls', async () => {
    jest.useFakeTimers();
    try {
      const root = await mount();
      const button = root.querySelector('button')!;
      let resolve!: (c: ArcheraComparison) => void;
      comparison.mockReturnValueOnce(new Promise<ArcheraComparison>(r => { resolve = r; }));
      button.click();
      expect(button.disabled).toBe(true);
      expect(button.getAttribute('aria-busy')).toBe('true');
      button.click();
      expect(comparison).toHaveBeenCalledTimes(1);
      resolve(golden);
      await flush();
      expect(button.disabled).toBe(false);
      expect(button.textContent).toBe('Refresh');
      expect(document.activeElement).toBe(button);
      jest.advanceTimersByTime(10 * 60 * 1000);
      await flush();
      expect(comparison).toHaveBeenCalledTimes(1);
      comparison.mockResolvedValueOnce(golden);
      button.click();
      await flush();
      expect(comparison).toHaveBeenCalledTimes(2);
    } finally {
      jest.useRealTimers();
    }
  });

  it('never schedules timers or intervals', async () => {
    const si = jest.spyOn(global, 'setInterval');
    const st = jest.spyOn(global, 'setTimeout');
    const root = await mount();
    await clickAndLoad(root, golden);
    expect(si).not.toHaveBeenCalled();
    expect(st).not.toHaveBeenCalled();
    si.mockRestore();
    st.mockRestore();
  });

  it('429 with retry_after_seconds shows the wait; Refresh stays enabled', async () => {
    const root = await mount();
    comparison.mockRejectedValueOnce(Object.assign(new Error('Archera rate limit reached'),
      { status: 429, details: { retry_after_seconds: 30 } }));
    root.querySelector('button')!.click();
    await flush();
    const live = root.querySelector('[aria-live="polite"]')!;
    expect(live.textContent).toContain('Retry after 30 seconds');
    expect(root.querySelector('button')!.disabled).toBe(false);
  });

  it('429 without retry_after_seconds says retry-after not given', async () => {
    const root = await mount();
    comparison.mockRejectedValueOnce(Object.assign(new Error('Archera rate limit reached'), { status: 429 }));
    root.querySelector('button')!.click();
    await flush();
    expect(root.querySelector('[aria-live="polite"]')!.textContent).toContain('Retry-after not given');
  });

  it.each([502, 503])('server error %s message shown as text in the live region', async code => {
    const root = await mount();
    comparison.mockRejectedValueOnce(Object.assign(new Error('fixed server text'), { status: code }));
    root.querySelector('button')!.click();
    await flush();
    expect(root.querySelector('[aria-live="polite"]')!.textContent).toBe('fixed server text');
  });

  it('unparseable fetched_at is shown raw', () => {
    const c = clone();
    c.fetched_at = 'not-a-date';
    expect(buildComparisonView(c).querySelector('.archera-fetched-at')!.textContent).toBe('Fetched at not-a-date');
  });
});
