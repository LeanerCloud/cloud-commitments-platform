/**
 * Archera comparison section (issue #785). The fixture is the Go DTO golden
 * file read from its Go path, so the UI is tested against the real shape.
 * Success rendering is therefore fixture-based; no live Archera calls.
 */
// The harness registers the module mock, so it must be imported first.
import { status, golden, TOP_KEYS, SUPPORT_KEYS, clone, firstOffer, mount, clickAndLoad, flush, comparison } from './archera-comparison-harness';
import type { ArcheraComparison } from '../api/types';

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

describe('numeric columns are right-aligned (num class) in header and body', () => {
  const NUM_HEADS = /monthly\)|one-time\)|^Amount$|^Discount rate$|^Breakeven days$/;
  const TEXT_HEADS = /^(Measure|Offer|Provider|Region|Term|Lease|Archera|Contract|Payment|Line items)/;

  it('every numeric column carries num on th and td, text columns do not', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    const tables = Array.from(root.querySelectorAll('table'));
    expect(tables.length).toBeGreaterThanOrEqual(3);
    let numSeen = 0;
    for (const t of tables) {
      Array.from(t.querySelectorAll('thead th')).forEach((h, i) => {
        const text = h.textContent!;
        const isNum = NUM_HEADS.test(text);
        if (!isNum && !TEXT_HEADS.test(text)) throw new Error(`unclassified header ${text}`);
        expect(h.classList.contains('num')).toBe(isNum);
        t.querySelectorAll('tbody tr').forEach(tr => {
          expect(tr.children[i]!.classList.contains('num')).toBe(isNum);
        });
        if (isNum) numSeen++;
      });
    }
    expect(numSeen).toBeGreaterThan(10);
  });
});

describe('money in every cell matches the golden value for that column', () => {
  function grid(t: HTMLTableElement): Array<Record<string, string>> {
    const heads = Array.from(t.querySelectorAll('thead th')).map(h => h.textContent!);
    return Array.from(t.querySelectorAll('tbody tr')).map(tr => {
      const out: Record<string, string> = {};
      Array.from(tr.children).forEach((c, i) => { out[heads[i]!] = c.textContent!; });
      return out;
    });
  }
  const tableByCaption = (root: HTMLElement, cap: RegExp) =>
    Array.from(root.querySelectorAll('table')).find(t => cap.test(t.querySelector('caption')!.textContent!))!;

  it('current plan totals table', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    const rows = Array.from(tableByCaption(root, /^Current plan totals/).querySelectorAll('tbody tr'))
      .map(tr => [tr.children[0]!.textContent, tr.children[1]!.textContent]);
    const c = golden.current;
    expect(rows).toEqual([
      ['Commitment cost total (monthly)', c.monthly_730h_commitment_cost_total],
      ['Cloud provider cost (monthly)', c.monthly_730h_cloud_provider_cost],
      ['Archera premium (monthly)', c.monthly_730h_archera_premium],
      ['Gross savings (monthly)', c.monthly_730h_gross_savings],
      ['Net savings (monthly)', c.monthly_730h_net_savings],
      ['Covered on-demand cost (monthly)', c.monthly_730h_covered_on_demand_cost],
      ['Upfront cost (one-time)', c.upfront_one_time_cost],
    ]);
    expect(new Set(rows.map(r => r[1])).size).toBe(7);
  });

  it('hypotheticals table', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    const g = grid(tableByCaption(root, /^Hypothetical/));
    expect(g).toHaveLength(golden.hypotheticals.length);
    golden.hypotheticals.forEach((h, i) => {
      const r = g[i]!;
      expect(r['Contract term']).toBe(h.contract_term);
      expect(r['Payment option']).toBe(h.payment_option);
      expect(r['Commitment cost total (monthly)']).toBe(h.totals.monthly_730h_commitment_cost_total);
      expect(r['Net savings (monthly)']).toBe(h.totals.monthly_730h_net_savings);
      expect(r['Upfront cost (one-time)']).toBe(h.totals.upfront_one_time_cost);
      expect(r['Net savings change vs current plan (monthly)']).toBe(h.delta_vs_current.monthly_730h_net_savings);
      expect(r['Commitment cost change vs current plan (monthly)']).toBe(h.delta_vs_current.monthly_730h_commitment_cost);
      expect(r['Upfront change vs current plan (one-time)']).toBe(h.delta_vs_current.upfront_one_time_cost);
      h.line_items.forEach(li => {
        expect(r['Line items']).toContain(`${li.line_item_id}: ${li.reason} (term ${li.actual_term}, payment ${li.actual_payment_option}, type ${li.actual_commitment_type})`);
      });
    });
  });

  it('offer tables: current and every candidate', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    golden.rows.forEach(row => {
      const g = grid(tableByCaption(root, new RegExp(`^Offers for line item ${row.line_item_id}$`)));
      const offers = [row.current, ...row.candidates];
      expect(g).toHaveLength(offers.length);
      offers.forEach((o, i) => {
        const r = g[i]!;
        expect(r['Offer']).toBe(`${o.offer_id} (${o.is_current ? 'current' : 'candidate'})`);
        expect(r['Provider / commitment type']).toBe(`${o.provider} / ${o.commitment_type}`);
        expect(r['Region']).toBe(o.region);
        expect(r['Term / payment option']).toBe(`${o.contract_term} / ${o.payment_option}`);
        expect(r['Discount rate']).toBe(o.discount_rate);
        expect(r['Breakeven days']).toBe(o.breakeven_days);
        expect(r['Net savings (monthly)']).toBe(o.monthly.monthly_730h_net_savings);
        expect(r['Upfront cost (one-time)']).toBe(o.upfront_one_time_cost);
        expect(r['Net savings change vs current plan (monthly)']).toBe(o.delta_vs_current.monthly_730h_net_savings);
        expect(r['Upfront change vs current plan (one-time)']).toBe(o.delta_vs_current.upfront_one_time_cost);
      });
    });
  });

  it('golden money values are distinct per column so a swap cannot hide', () => {
    const vals = [golden.current, golden.hypotheticals[0]!.totals].flatMap(t => Object.values(t));
    expect(new Set(vals).size).toBe(vals.length);
  });
});

describe('fetched-at shows local time and UTC', () => {
  const OPTS: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'long' };
  const instant = new Date(golden.fetched_at);
  const utcText = new Intl.DateTimeFormat('en-GB', { ...OPTS, timeZone: 'UTC' }).format(instant);
  const RealDTF = Intl.DateTimeFormat;

  afterEach(() => {
    Intl.DateTimeFormat = RealDTF;
  });

  // jest cannot change the process zone, so the browser's default zone is
  // simulated: formatters built without an explicit timeZone get `tz`.
  async function fetchedAt(tz: string): Promise<string> {
    Intl.DateTimeFormat = function (locale?: string | string[], o?: Intl.DateTimeFormatOptions) {
      return new RealDTF(locale, o?.timeZone ? o : { ...o, timeZone: tz });
    } as unknown as typeof Intl.DateTimeFormat;
    const root = await mount();
    await clickAndLoad(root, golden);
    return root.querySelector('.archera-fetched-at')!.textContent!;
  }

  it('non-UTC zone: local text and UTC text are both present and exact', async () => {
    const got = await fetchedAt('America/New_York');
    const local = new Intl.DateTimeFormat(undefined, { ...OPTS, timeZone: 'America/New_York' }).format(instant);
    expect(local).not.toBe(utcText);
    expect(utcText).toContain('12:00:00');
    expect(got).toBe(`Fetched at ${local} (${utcText})`);
  });

  it('UTC zone: exact text', async () => {
    const got = await fetchedAt('UTC');
    const local = new Intl.DateTimeFormat(undefined, { ...OPTS, timeZone: 'UTC' }).format(instant);
    expect(got).toBe(`Fetched at ${local} (${utcText})`);
  });

  it('UTC part stays in UTC when the browser zone is far away', async () => {
    const got = await fetchedAt('Pacific/Auckland');
    expect(got.endsWith(`(${utcText})`)).toBe(true);
  });
});

describe('captions, notes, live text and unknown money in every cell', () => {
  const captions = (root: HTMLElement) =>
    Array.from(root.querySelectorAll('caption')).map(c => c.textContent);

  it('offer table captions carry the line item id; empty is "Not given"; hostile stays text', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    expect(captions(root)).toContain(`Offers for line item ${golden.rows[0]!.line_item_id}`);

    const empty = clone();
    empty.rows[0]!.line_item_id = '';
    const r2 = await mount();
    await clickAndLoad(r2, empty);
    expect(captions(r2)).toContain('Offers for line item Not given');

    const hostile = clone();
    hostile.rows[0]!.line_item_id = '<img src=x onerror=1>';
    const r3 = await mount();
    await clickAndLoad(r3, hostile);
    expect(captions(r3)).toContain('Offers for line item <img src=x onerror=1>');
    expect(r3.querySelector('img')).toBeNull();
  });

  it('shows the no-cache note', async () => {
    const root = await mount();
    await clickAndLoad(root, golden);
    expect(root.textContent).toContain('Archera does not document a cache lifetime. Use Refresh to fetch again.');
  });

  it('live region text: loading, loaded; button stays Refresh after a later error', async () => {
    const root = await mount();
    const button = root.querySelector('button')!;
    const live = root.querySelector('.archera-comparison [aria-live="polite"]') ?? root.querySelector('[aria-live="polite"]')!;
    let resolve!: (c: ArcheraComparison) => void;
    comparison.mockReturnValueOnce(new Promise<ArcheraComparison>(r => { resolve = r; }));
    button.click();
    expect(live.textContent).toBe('Loading Archera comparison');
    resolve(golden);
    await flush();
    expect(live.textContent).toBe('Archera comparison loaded');
    expect(button.textContent).toBe('Refresh');
    comparison.mockRejectedValueOnce(Object.assign(new Error('x'), { status: 502 }));
    button.click();
    await flush();
    expect(button.textContent).toBe('Refresh');
  });

  function nullMoney(node: unknown, key = ''): unknown {
    if (Array.isArray(node)) return node.map(x => nullMoney(x, key));
    if (node && typeof node === 'object') {
      return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, nullMoney(v, k)]));
    }
    return /^(monthly_730h_|upfront_one_time_cost$|discount_rate$|breakeven_days$)/.test(key) ? null : node;
  }

  it('a null money value renders Unknown in every money cell of every table', async () => {
    const root = await mount();
    await clickAndLoad(root, nullMoney(clone()) as ArcheraComparison);
    const moneyHeads = /savings|cost|upfront|Upfront|Discount|Breakeven|premium|Amount/i;
    let checked = 0;
    root.querySelectorAll('table').forEach(t => {
      const heads = Array.from(t.querySelectorAll('thead th')).map(h => h.textContent!);
      t.querySelectorAll('tbody tr').forEach(tr => {
        Array.from(tr.children).forEach((c, i) => {
          const h = heads[i]!;
          const rowLabel = tr.children[0]!.textContent!;
          const isMoneyCell = h === 'Amount' ? true : (moneyHeads.test(h) && h !== 'Line items');
          if (isMoneyCell && !(h === 'Amount' && c === tr.children[0])) {
            expect({ h, rowLabel, v: c.textContent }).toEqual({ h, rowLabel, v: 'Unknown' });
            checked++;
          }
        });
      });
    });
    // 7 totals + 7 hypothetical + 6 per offer * 2 offers
    expect(checked).toBe(7 + 6 + 6 * 2);
  });
});
