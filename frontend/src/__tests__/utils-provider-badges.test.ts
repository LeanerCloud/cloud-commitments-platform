/**
 * Unit tests for utility functions: provider badges, cloning, compact money,
 * amortization (split from utils.test.ts, issue #1965).
 */
import {
  formatCompactCurrency,
  formatCurrency,
  deepClone,
  jsonClone,
  providerBadgeClass,
  providerBadgeHtml,
  amortizedMonthly,
} from '../utils';

// Regression tests for H1/D1/L4: providerBadgeClass whitelist.
// Pre-fix the class was interpolated raw from the API (plans.ts) or allowed any
// alphanumeric string (dashboard.ts).  Post-fix all sites go through this
// helper which enforces the aws|azure|gcp allow-list.
describe('providerBadgeClass', () => {
  test('returns lowercase provider for known aws', () => {
    expect(providerBadgeClass('aws')).toBe('aws');
    expect(providerBadgeClass('AWS')).toBe('aws');
  });

  test('returns lowercase provider for known azure', () => {
    expect(providerBadgeClass('azure')).toBe('azure');
    expect(providerBadgeClass('Azure')).toBe('azure');
  });

  test('returns lowercase provider for known gcp', () => {
    expect(providerBadgeClass('gcp')).toBe('gcp');
  });

  test('returns empty string for unknown provider (XSS payload neutralised)', () => {
    // Regression: pre-fix this value would be injected verbatim as a CSS class.
    expect(providerBadgeClass('aws"><img src=x onerror=alert(1)>')).toBe('');
    expect(providerBadgeClass('evil-class')).toBe('');
    expect(providerBadgeClass('unknown')).toBe('');
  });

  test('returns empty string for null/undefined/empty', () => {
    expect(providerBadgeClass(null)).toBe('');
    expect(providerBadgeClass(undefined)).toBe('');
    expect(providerBadgeClass('')).toBe('');
  });
});

// Regression tests for H1: providerBadgeHtml XSS in plans.ts.
// Pre-fix: plans.ts interpolated provider raw into class + textContent via innerHTML.
// Post-fix: class is whitelisted; text is HTML-escaped.
describe('providerBadgeHtml', () => {
  test('renders known provider with correct class and uppercased label', () => {
    const html = providerBadgeHtml('aws');
    expect(html).toContain('class="provider-badge aws"');
    expect(html).toContain('>AWS<');
  });

  test('renders azure with correct class', () => {
    const html = providerBadgeHtml('azure');
    expect(html).toContain('class="provider-badge azure"');
    expect(html).toContain('>AZURE<');
  });

  test('neutralises XSS payload in class attribute position', () => {
    // The raw value would previously be injected as: class="provider-badge <payload>"
    const payload = 'aws"><img src=x onerror=alert(document.cookie)>';
    const html = providerBadgeHtml(payload);
    // Class must not contain the raw payload.
    expect(html).not.toContain(payload);
    // No <img> or onerror in the output.
    expect(html).not.toContain('<img');
    expect(html).not.toContain('onerror');
    // Class falls back to just provider-badge (no extra class token).
    expect(html).toMatch(/class="provider-badge"/);
  });

  test('HTML-escapes text content of unknown provider', () => {
    const html = providerBadgeHtml('<script>alert(1)</script>');
    expect(html).not.toContain('<script>');
    expect(html).toContain('&lt;SCRIPT&gt;');
  });

  test('handles null/undefined gracefully', () => {
    expect(providerBadgeHtml(null)).toContain('provider-badge');
    expect(providerBadgeHtml(undefined)).toContain('provider-badge');
  });
});

// ---------------------------------------------------------------------------
// Regression tests for finding 11-N1: deepClone defaults to structuredClone.
// The jsdom test environment polyfills structuredClone via
// `@ungap/structured-clone` (see setup.ts), which implements the real HTML
// structured clone algorithm, so full browser semantics are asserted here.
// ---------------------------------------------------------------------------
describe('deepClone (11-N1: structuredClone default)', () => {
  test('deep copy: mutation of clone does not affect source', () => {
    const obj = { nested: { x: 1 } };
    const clone = deepClone(obj);
    clone.nested.x = 99;
    expect(obj.nested.x).toBe(1);
  });

  test('deep copy: clones arrays independently', () => {
    const arr = [1, [2, 3]] as [number, number[]];
    const clone = deepClone(arr);
    (clone[1] as number[])[0] = 99;
    expect((arr[1] as number[])[0]).toBe(2);
  });

  test('handles primitives and null without throwing', () => {
    expect(deepClone(null)).toBe(null);
    expect(deepClone(42)).toBe(42);
    expect(deepClone('hello')).toBe('hello');
  });
});

// ---------------------------------------------------------------------------
// Regression tests for TEST-07: the test-environment structuredClone must
// implement real structured clone semantics, not a JSON round-trip. Each test
// below fails against the previous JSON.parse(JSON.stringify(...)) polyfill
// and passes against the browser/Node algorithm.
// ---------------------------------------------------------------------------
describe('deepClone (TEST-07: real structured clone semantics)', () => {
  test('preserves undefined-valued properties (JSON round-trip drops them)', () => {
    const obj: { a: number; b: number | undefined } = { a: 1, b: undefined };
    const clone = deepClone(obj);
    expect('b' in clone).toBe(true);
    expect(clone.b).toBeUndefined();
  });

  test('preserves Date instances (JSON round-trip stringifies them)', () => {
    const obj = { ts: new Date('2026-01-02T03:04:05.678Z') };
    const clone = deepClone(obj);
    expect(clone.ts).toBeInstanceOf(Date);
    expect(clone.ts.getTime()).toBe(obj.ts.getTime());
    expect(clone.ts).not.toBe(obj.ts);
  });

  test('preserves Map and Set (JSON round-trip degrades them to {})', () => {
    const obj = { m: new Map([['k', 1]]), s: new Set([1, 2]) };
    const clone = deepClone(obj);
    expect(clone.m).toBeInstanceOf(Map);
    expect(clone.m.get('k')).toBe(1);
    expect(clone.m).not.toBe(obj.m);
    expect(clone.s).toBeInstanceOf(Set);
    expect(clone.s.has(2)).toBe(true);
    expect(clone.s).not.toBe(obj.s);
  });

  test('handles cyclic references (JSON round-trip throws)', () => {
    interface Cyclic {
      name: string;
      self?: Cyclic;
    }
    const obj: Cyclic = { name: 'loop' };
    obj.self = obj;
    const clone = deepClone(obj);
    expect(clone).not.toBe(obj);
    expect(clone.self).toBe(clone);
  });

  test('throws on functions like the browser implementation', () => {
    // JSON round-trip silently drops functions; structured clone must throw.
    expect(() => deepClone({ fn: (): number => 1 })).toThrow();
  });
});

describe('jsonClone (explicit JSON-serialisation variant)', () => {
  test('drops undefined values (expected JSON behaviour)', () => {
    const obj = { a: 1, b: undefined };
    const clone = jsonClone(obj);
    // JSON round-trip removes keys whose value is undefined.
    expect('b' in clone).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Regression tests for finding 11-N2: formatCurrency distinguishes absent from $0.
// ---------------------------------------------------------------------------
describe('formatCompactCurrency (issue #749)', () => {
  test('abbreviates thousands and millions like a chart axis', () => {
    expect(formatCompactCurrency(0)).toBe('$0');
    expect(formatCompactCurrency(950)).toBe('$950');
    expect(formatCompactCurrency(1500)).toBe('$1.5K');
    expect(formatCompactCurrency(12345)).toBe('$12.35K');
    expect(formatCompactCurrency(1234567)).toBe('$1.23M');
    expect(formatCompactCurrency(12345, 1)).toBe('$12.3K');
  });

  test('keeps close fractional ticks distinct', () => {
    expect(formatCompactCurrency(0.1)).not.toBe(formatCompactCurrency(0.2));
  });

  test('uses the same absent marker as formatCurrency', () => {
    expect(formatCompactCurrency(null)).toBe(formatCurrency(null));
    expect(formatCompactCurrency(NaN)).toBe(formatCurrency(NaN));
  });
});

describe('formatCurrency (11-N2: absent vs real zero)', () => {
  test('null returns an em dash, not $0', () => {
    expect(formatCurrency(null as unknown as number)).toBe('—');
  });

  test('undefined returns an em dash, not $0', () => {
    expect(formatCurrency(undefined as unknown as number)).toBe('—');
  });

  test('NaN returns an em dash, not $0', () => {
    expect(formatCurrency(NaN)).toBe('—');
  });

  test('Infinity returns an em dash, not $Infinity', () => {
    expect(formatCurrency(Infinity)).toBe('—');
  });

  test('real zero still renders as $0', () => {
    expect(formatCurrency(0)).toBe('$0');
  });
});

describe('amortizedMonthly', () => {
  test('All Upfront: zero recurring cost produces positive amortized value', () => {
    // $0/mo recurring + $1200 upfront over 1 year = $100/mo amortized
    expect(amortizedMonthly(0, 1200, 1)).toBeCloseTo(100, 5);
  });

  test('All Upfront: 3-year term spreads upfront over 36 months', () => {
    // $0/mo recurring + $3600 upfront over 3 years = $100/mo amortized
    expect(amortizedMonthly(0, 3600, 3)).toBeCloseTo(100, 5);
  });

  test('Partial Upfront: recurring + amortized-upfront slice', () => {
    // $50/mo recurring + $600 upfront over 1 year = $50 + $50 = $100/mo
    expect(amortizedMonthly(50, 600, 1)).toBeCloseTo(100, 5);
  });

  test('No Upfront (upfront === 0): result equals monthlyCost unchanged', () => {
    expect(amortizedMonthly(80, 0, 1)).toBeCloseTo(80, 5);
    expect(amortizedMonthly(80, 0, 3)).toBeCloseTo(80, 5);
  });

  test('term <= 0: returns monthlyCost unchanged (guard against divide-by-zero)', () => {
    expect(amortizedMonthly(50, 600, 0)).toBe(50);
    expect(amortizedMonthly(50, 600, -1)).toBe(50);
  });

  test('non-finite term: returns monthlyCost unchanged', () => {
    expect(amortizedMonthly(50, 600, Infinity)).toBe(50);
    expect(amortizedMonthly(50, 600, NaN)).toBe(50);
  });

  test('null upfrontCost: returns monthlyCost unchanged', () => {
    expect(amortizedMonthly(80, null, 1)).toBe(80);
  });

  test('undefined upfrontCost: returns monthlyCost unchanged', () => {
    expect(amortizedMonthly(80, undefined, 1)).toBe(80);
  });

  test('non-finite upfrontCost: returns monthlyCost unchanged', () => {
    expect(amortizedMonthly(80, Infinity, 1)).toBe(80);
    expect(amortizedMonthly(80, NaN, 1)).toBe(80);
  });
});
