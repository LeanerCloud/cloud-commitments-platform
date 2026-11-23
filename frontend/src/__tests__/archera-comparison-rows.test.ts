/**
 * Archera comparison section (issue #785): multiple rows, candidates,
 * hypotheticals, line items, refresh replacement, aria-busy and empty vendor
 * strings. Split from archera-comparison.test.ts to stay under the 500-line
 * ceiling.
 */

// The harness registers the module mock, so it must be imported first.
import { status, golden, clone, mount, clickAndLoad, flush, comparison } from './archera-comparison-harness';
import type { ArcheraComparison, ArcheraOffer } from '../api/types';

beforeEach(() => {
  jest.resetAllMocks();
  status.mockResolvedValue({ configured: true, missing: [] });
});

describe('multiple rows, candidates, hypotheticals, line items; refresh replaces', () => {
  // Rewrites every money string to a unique value so any index mixup shows.
  function big(seed: number): ArcheraComparison {
    const c = clone();
    let n = seed;
    const uniq = (node: unknown, key = ''): unknown => {
      if (Array.isArray(node)) return node.map(x => uniq(x, key));
      if (node && typeof node === 'object') {
        return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, uniq(v, k)]));
      }
      return typeof node === 'string' && /^(monthly_730h_|upfront_one_time_cost$|discount_rate$|breakeven_days$)/.test(key)
        ? String(n++) : node;
    };
    const base = uniq(c) as ArcheraComparison;
    const h0 = base.hypotheticals[0]!;
    base.hypotheticals = [h0, JSON.parse(JSON.stringify(uniq(h0))) as typeof h0];
    base.hypotheticals.forEach((h, i) => {
      h.contract_term = `term-${seed}-${i}`;
      const li0 = h.line_items[0]!;
      h.line_items = [0, 1].map(j => ({ ...li0, line_item_id: `li-${seed}-${i}-${j}`, reason: `reason-${i}-${j}` }));
    });
    const r0 = base.rows[0]!;
    const mk = (i: number): typeof r0 => {
      const r = JSON.parse(JSON.stringify(uniq(r0))) as typeof r0;
      r.line_item_id = `row-${seed}-${i}`;
      const cand0 = r.candidates[0]!;
      r.candidates = [0, 1].map(j => ({ ...JSON.parse(JSON.stringify(uniq(cand0))), offer_id: `cand-${seed}-${i}-${j}` }));
      r.current.offer_id = `cur-${seed}-${i}`;
      return r;
    };
    base.rows = [mk(0), mk(1)];
    return base;
  }

  function offerMoney(o: ArcheraOffer): string[] {
    return [o.discount_rate, o.breakeven_days, o.monthly.monthly_730h_net_savings, o.upfront_one_time_cost,
      o.delta_vs_current.monthly_730h_net_savings, o.delta_vs_current.upfront_one_time_cost] as string[];
  }

  it('every row, candidate, hypothetical and line item renders its own values in order', async () => {
    const c = big(1000);
    const root = await mount();
    await clickAndLoad(root, c);
    const tables = Array.from(root.querySelectorAll('table'));
    // totals + hypotheticals + one per row
    expect(tables).toHaveLength(2 + c.rows.length);
    const hyp = tables[1]!;
    const hrows = Array.from(hyp.querySelectorAll('tbody tr'));
    expect(hrows).toHaveLength(2);
    c.hypotheticals.forEach((h, i) => {
      const cells = Array.from(hrows[i]!.children).map(x => x.textContent);
      expect(cells.slice(0, 2)).toEqual([h.contract_term, h.payment_option]);
      expect(cells.slice(2, 8)).toEqual([
        h.totals.monthly_730h_commitment_cost_total, h.totals.monthly_730h_net_savings, h.totals.upfront_one_time_cost,
        h.delta_vs_current.monthly_730h_net_savings, h.delta_vs_current.monthly_730h_commitment_cost,
        h.delta_vs_current.upfront_one_time_cost]);
      const lis = Array.from(hrows[i]!.children[8]!.children).map(x => x.textContent);
      expect(lis).toHaveLength(2);
      h.line_items.forEach((li, j) => expect(lis[j]).toContain(`${li.line_item_id}: ${li.reason}`));
    });
    c.rows.forEach((r, i) => {
      const t = tables[2 + i]!;
      expect(t.querySelector('caption')!.textContent).toBe(`Offers for line item ${r.line_item_id}`);
      const trs = Array.from(t.querySelectorAll('tbody tr'));
      const offers = [r.current, ...r.candidates];
      expect(trs).toHaveLength(3);
      offers.forEach((o, k) => {
        const cells = Array.from(trs[k]!.children).map(x => x.textContent);
        expect(cells[0]).toContain(o.offer_id);
        expect(cells.slice(7)).toEqual(offerMoney(o));
      });
    });
    expect(root.querySelectorAll('h3')).toHaveLength(2);
  });

  it('Refresh replaces the old view: one set of tables, new values, no stale ones', async () => {
    const first = big(2000);
    const second = big(3000);
    const root = await mount();
    await clickAndLoad(root, first);
    const oldVal = first.current.monthly_730h_net_savings!;
    expect(root.textContent).toContain(`row-2000-0`);
    comparison.mockResolvedValueOnce(second);
    root.querySelector('button')!.click();
    await flush();
    expect(root.querySelectorAll('table')).toHaveLength(2 + second.rows.length);
    expect(root.querySelectorAll('h2')).toHaveLength(1);
    expect(root.textContent).toContain('row-3000-0');
    expect(root.textContent).not.toContain('row-2000-0');
    expect(root.textContent).toContain(second.current.monthly_730h_net_savings!);
    expect(root.textContent).not.toContain(`li-2000`);
    void oldVal;
  });
});

describe('aria-busy and empty vendor strings', () => {
  it('aria-busy is absent after success and after error', async () => {
    const root = await mount();
    const button = root.querySelector('button')!;
    await clickAndLoad(root, golden);
    expect(button.hasAttribute('aria-busy')).toBe(false);
    comparison.mockRejectedValueOnce(Object.assign(new Error('fixed server text'), { status: 502 }));
    button.click();
    expect(button.getAttribute('aria-busy')).toBe('true');
    await flush();
    expect(button.hasAttribute('aria-busy')).toBe(false);
  });

  const VENDOR_KEYS = new Set([
    'offer_id', 'provider', 'commitment_type', 'region', 'contract_term', 'payment_option',
    'lease_menu_item_id', 'archera_offer_name', 'line_item_id', 'reason', 'actual_term',
    'actual_payment_option', 'actual_commitment_type',
  ]);
  function blank(node: unknown, key = ''): unknown {
    if (typeof node === 'string') return VENDOR_KEYS.has(key) ? '' : node;
    if (Array.isArray(node)) return node.map(x => blank(x, key));
    if (node && typeof node === 'object') {
      return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, blank(v, k)]));
    }
    return node;
  }

  it('an empty vendor string renders "Not given" in every place it is shown', async () => {
    const c = blank(clone()) as ArcheraComparison;
    const root = await mount();
    await clickAndLoad(root, c);
    const NG = 'Not given';
    const hyp = Array.from(root.querySelectorAll('table')).find(t => /^Hypothetical/.test(t.querySelector('caption')!.textContent!))!;
    const hcells = Array.from(hyp.querySelectorAll('tbody tr')[0]!.children).map(x => x.textContent);
    expect(hcells[0]).toBe(NG);
    expect(hcells[1]).toBe(NG);
    expect(hcells[8]).toBe(`${NG}: ${NG} (term ${NG}, payment ${NG}, type ${NG})`);
    expect(root.querySelector('h3')!.textContent).toBe(`Line item ${NG}`);
    expect(root.querySelector('caption + thead')).not.toBeNull();
    const offers = root.querySelectorAll('table')[2]!;
    const trs = Array.from(offers.querySelectorAll('tbody tr'));
    expect(trs.length).toBeGreaterThan(1);
    trs.forEach((tr, i) => {
      const cells = Array.from(tr.children).map(x => x.textContent);
      expect(cells[0]).toBe(`${NG} (${i === 0 ? 'current' : 'candidate'})`);
      expect(cells[1]).toBe(`${NG} / ${NG}`);
      expect(cells[2]).toBe(NG);
      expect(cells[3]).toBe(`${NG} / ${NG}`);
      expect(cells[4]).toBe(i === 0 ? `lease attached (${NG})` : 'no lease');
      expect(cells[5]).toBe(NG);
    });
  });
});
