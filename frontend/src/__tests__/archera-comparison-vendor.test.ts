/**
 * Archera comparison section (issue #785): vendor strings, product support, refresh,
 * errors and accessibility. Split from archera-comparison.test.ts to stay under
 * the 500-line ceiling.
 */

// The harness registers the module mock, so it must be imported first.
import { status, golden, clone, firstOffer, mount, clickAndLoad, flush, comparison } from './archera-comparison-harness';
import { buildComparisonView } from '../archera-comparison';
import type { ArcheraComparison } from '../api/types';

beforeEach(() => {
  jest.resetAllMocks();
  status.mockResolvedValue({ configured: true, missing: [] });
});

describe('vendor strings are text only', () => {
  const HOSTILE = (n: number) => `<img src=x onerror=H${n}><script>S${n}</script><svg onload=V${n}>\u0007\u202e${n}`;

  // Replaces every string leaf except the enum-like ones that gate rendering.
  function poison(node: unknown, tokens: Map<string, string>, p: string): unknown {
    if (typeof node === 'string') {
      if (/(^|\.)(status|fetched_at)$/.test(p)) return node;
      const v = HOSTILE(tokens.size);
      tokens.set(p, v);
      return v;
    }
    if (Array.isArray(node)) return node.map((x, i) => poison(x, tokens, `${p}[${i}]`));
    if (node && typeof node === 'object') {
      return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, poison(v, tokens, p ? `${p}.${k}` : k)]));
    }
    return node;
  }

  // Leaves the UI deliberately does not display.
  const NOT_RENDERED = /^(plan_id|rows\[\d+\]\.(current|candidates\[\d+\])\.(monthly\.monthly_730h_(commitment_cost_total|cloud_provider_cost|archera_premium|gross_savings|covered_on_demand_cost)|delta_vs_current\.(discount_rate|breakeven_days)|archera_product_support\.(underwriting_allowance|customer_eligibility))|hypotheticals\[\d+\]\.totals\.monthly_730h_(cloud_provider_cost|archera_premium|gross_savings|covered_on_demand_cost))$/;

  it('every rendered vendor string is text; no element is created from any of them', async () => {
    const tokens = new Map<string, string>();
    const c = poison(clone(), tokens, '') as ArcheraComparison;
    const root = await mount();
    await clickAndLoad(root, c);
    const rendered = root.textContent!;
    const missing: string[] = [];
    for (const [p, v] of tokens) {
      if (!NOT_RENDERED.test(p) && !rendered.includes(v)) missing.push(p);
    }
    expect(missing).toEqual([]);
    expect(root.querySelectorAll('img, script, svg, iframe, a').length).toBe(0);
    // Only the structural elements the component itself builds exist.
    const allowed = new Set(['H2', 'H3', 'P', 'DIV', 'TABLE', 'CAPTION', 'THEAD', 'TBODY', 'TR', 'TH', 'TD', 'SECTION', 'BUTTON']);
    root.querySelectorAll('*').forEach(n => expect(allowed.has(n.tagName)).toBe(true));
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

  it('429 without retry_after_seconds uses the real server text, saying so exactly once', async () => {
    const root = await mount();
    // Exact text from mapInsuranceError (internal/api/handler_insurance_comparison.go).
    comparison.mockRejectedValueOnce(Object.assign(new Error('Archera rate limit reached; retry-after not given'), { status: 429 }));
    root.querySelector('button')!.click();
    await flush();
    const t = root.querySelector('[aria-live="polite"]')!.textContent!;
    expect(t).toBe('Archera rate limit reached; retry-after not given');
    expect(t.match(/retry-after not given/gi)).toHaveLength(1);
  });

  it('429 whose message lacks the wait text gets it appended once', async () => {
    const root = await mount();
    comparison.mockRejectedValueOnce(Object.assign(new Error('Rate limited'), { status: 429 }));
    root.querySelector('button')!.click();
    await flush();
    expect(root.querySelector('[aria-live="polite"]')!.textContent!.match(/retry-after not given/gi)).toHaveLength(1);
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
