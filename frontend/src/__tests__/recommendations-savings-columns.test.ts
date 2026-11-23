/**
 * Recommendations module tests: effective percent preference, display savings, on-demand monthly and column rendering tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { effectiveSavingsPct, displaySavingsPct, onDemandMonthly } from '../recommendations';
import type { LocalRecommendation } from '../types';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('effectiveSavingsPct', () => {
  // mk() defaults to 'azure' for reconstruction-path tests. AWS rows require
  // on_demand_cost to be set; without it effectiveSavingsPct returns null
  // (#323). Azure reconstruction is still valid (Azure may omit on_demand_cost
  // for older cached rows where the field was not yet populated).
  const mk = (overrides: Partial<LocalRecommendation>): LocalRecommendation => ({
    id: 'r',
    provider: 'azure',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    savings: 100,
    upfront_cost: 0,
    monthly_cost: 50,
    ...overrides,
  } as unknown as LocalRecommendation);

  // #274: on_demand_cost (when populated by the provider) is used directly
  // as the denominator instead of reconstructing it from
  // monthly_cost + savings + amortized. The reconstruction collapses for
  // Azure all-upfront recs where monthly_cost = $0 and inflates the % well
  // past realistic ceilings.
  describe('on_demand_cost preference (#274)', () => {
    test('on_demand_cost null/undefined falls back to reconstruction (back-compat)', () => {
      // Pre-#274 cached recs have no on_demand_cost. The frontend
      // reconstructs as before so older data still renders meaningful
      // percentages while it gets refreshed.
      const r = mk({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1 });
      delete (r as { on_demand_cost?: unknown }).on_demand_cost;
      const pct = effectiveSavingsPct(r);
      expect(pct).not.toBeNull();
      expect(pct!).toBeCloseTo(66.67, 1);
    });

    test('on_demand_cost = 0 from the provider is treated as not-populated (falls back)', () => {
      // The backend's convertRecommendations writes 0 → nil so this case
      // is unlikely on a fresh refresh, but defence-in-depth: a literal 0
      // baseline is impossible (the resource would be free), so the
      // formula treats it the same as null.
      const pct = effectiveSavingsPct(
        mk({ savings: 100, upfront_cost: 0, monthly_cost: 50, term: 1, on_demand_cost: 0 }),
      );
      expect(pct).not.toBeNull();
      expect(pct!).toBeCloseTo(66.67, 1);
    });

    test('on_demand_cost populated rescues the missing-monthly_cost case', () => {
      // Without on_demand_cost: monthly_cost === null returns null
      // (denominator can't be reconstructed). With the provider baseline,
      // the formula has everything it needs and returns a real value.
      const r = mk({ savings: 100, upfront_cost: 0, monthly_cost: null, term: 1, on_demand_cost: 250 });
      const pct = effectiveSavingsPct(r);
      expect(pct).not.toBeNull();
      expect(pct!).toBeCloseTo(40, 1);
    });

    // #303: AWS RI and SP fixtures — the same on_demand_cost preference
    // applies to AWS recommendations (previously only verified for Azure).
    describe('AWS provider fixtures (#303)', () => {
      test('AWS EC2 RI: on_demand_cost from EstimatedMonthlyOnDemandCost is used as denominator', () => {
        // Real-world shape: 2 × m5.large 1yr partial-upfront.
        // EstimatedMonthlyOnDemandCost = $150 (AWS CE field plumbed via
        // parseAWSCostDetails → common.Recommendation.OnDemandCost).
        // savings = $45 (already net of the $10/mo amortized upfront per #1103).
        // pct = 45 / 150 × 100 = 30%.
        // (Old formula double-subtracted amortized: (45-10)/150=23.33%.)
        const pct = effectiveSavingsPct(
          mk({ savings: 45, upfront_cost: 120, monthly_cost: 60, term: 1, on_demand_cost: 150 }),
        );
        expect(pct).not.toBeNull();
        expect(pct!).toBeCloseTo(30, 1);
      });

      test('AWS Compute SP: on_demand_cost from CurrentAverageHourlyOnDemandSpend×730 is used as denominator', () => {
        // SP rows carry monthly_cost = the recurring commitment charge, not
        // the full on-demand baseline. Without on_demand_cost the
        // reconstruction would over- or under-count the denominator.
        // Here: savings=$200, monthly_cost=$800 (commitment), on_demand_cost=$1000.
        // Reconstruction would give: onDemand = 800 + 200 + 0 = $1000 — same
        // in this clean case, but diverges for partial-upfront SP where
        // monthly_cost ≠ hourlyCommitment × 730.
        // effectiveSavings = 200 - 0 = $200; pct = 200 / 1000 × 100 = 20%.
        const pct = effectiveSavingsPct(
          mk({ savings: 200, upfront_cost: 0, monthly_cost: 800, term: 1, on_demand_cost: 1000 }),
        );
        expect(pct).not.toBeNull();
        expect(pct!).toBeCloseTo(20, 1);
      });

      test('AWS SP no-upfront with null monthly_cost: on_demand_cost alone is sufficient', () => {
        // SP recommendation where the backend did not populate monthly_cost
        // (e.g. all-upfront SP with recurring = $0 stored as nil).
        // With on_demand_cost the formula is fully determined.
        const pct = effectiveSavingsPct(
          mk({ savings: 300, upfront_cost: 0, monthly_cost: null, term: 1, on_demand_cost: 1500 }),
        );
        expect(pct).not.toBeNull();
        // savings already net; pct = 300 / 1500 × 100 = 20%
        expect(pct!).toBeCloseTo(20, 1);
      });

      test('regression #1103: high-upfront 3yr AWS RI no longer shows negative pct', () => {
        // Pre-fix: savings=30 (net), upfront=2988, term=3, on_demand=100.
        // Old formula: amortized=2988/36=83, effectiveSavings=30-83=-53,
        //   pct=-53/100=-53% (wrong -- purchase genuinely saves money).
        // Fixed formula: pct=30/100=30%.
        const pct = effectiveSavingsPct(
          mk({ provider: 'aws', savings: 30, upfront_cost: 2988, monthly_cost: 0, term: 3, on_demand_cost: 100 }),
        );
        expect(pct).not.toBeNull();
        expect(pct!).toBeGreaterThan(0);
        expect(pct!).toBeCloseTo(30, 1);
      });
    });
  });
});

// CLI/GUI parity: displaySavingsPct prefers the provider-authoritative
// `savings_percentage` (the same figure the CLI/reporter prints) over the
// client-side reconstruction (effectiveSavingsPct), falling back only when no
// provider % is present. This closes the gap where the GUI re-derived the %
// (could drift, and returned an em-dash for AWS recs missing on_demand_cost despite
// the provider reporting a real %, the #323 case).
describe('displaySavingsPct (CLI/GUI parity)', () => {
  const mk = (overrides: Partial<LocalRecommendation>): LocalRecommendation => ({
    id: 'r',
    provider: 'aws',
    service: 'ec2',
    resource_type: 'm5.large',
    region: 'us-east-1',
    count: 1,
    term: 1,
    savings: 45,
    upfront_cost: 120,
    monthly_cost: 60,
    on_demand_cost: 150,
    ...overrides,
  } as unknown as LocalRecommendation);

  test('(a) uses provider savings_percentage verbatim and ignores the computed value', () => {
    // Provider reports 30%. The reconstruction would compute a DIFFERENT
    // number (savings 45 / on_demand 150 = 30% here would coincide, so skew
    // on_demand to force divergence): with on_demand_cost=300 the computed
    // value is 45/300=15%, but the provider % (30) must win verbatim.
    const r = mk({ savings_percentage: 30, savings: 45, on_demand_cost: 300 });
    // Sanity: the computed fallback would have been 15%, proving the provider
    // value is used, not the reconstruction.
    expect(effectiveSavingsPct(r)).toBeCloseTo(15, 1);
    expect(displaySavingsPct(r)).toBe(30);
  });

  test('(b) falls back to effectiveSavingsPct when savings_percentage is null', () => {
    // No provider %, so the displayed value is the net-savings reconstruction.
    // savings (net) = 45, on_demand_cost = 150 => 45 / 150 = 30%.
    const r = mk({ savings_percentage: null, savings: 45, on_demand_cost: 150 });
    expect(displaySavingsPct(r)).toBeCloseTo(30, 1);
    expect(displaySavingsPct(r)).toBe(effectiveSavingsPct(r));
  });

  test('(b2) falls back when savings_percentage is undefined (pre-plumbing cached recs)', () => {
    const r = mk({ savings: 48, on_demand_cost: 200 });
    delete (r as { savings_percentage?: unknown }).savings_percentage;
    expect(displaySavingsPct(r)).toBeCloseTo(24, 1); // 48 / 200
    expect(displaySavingsPct(r)).toBe(effectiveSavingsPct(r));
  });

  test('(c) AWS rec missing on_demand_cost still shows the provider % (not an em-dash)', () => {
    // Pre-parity: effectiveSavingsPct returns null for an AWS rec without
    // on_demand_cost (#323), so the column rendered an em-dash. With the provider %
    // plumbed through, displaySavingsPct returns the real number.
    const r = mk({ provider: 'aws', savings_percentage: 22, on_demand_cost: null, monthly_cost: 60 });
    // Sanity: the reconstruction fallback alone WOULD be null (the bug).
    expect(effectiveSavingsPct(r)).toBeNull();
    // With the provider %, the displayed value is the authoritative 22%.
    expect(displaySavingsPct(r)).toBe(22);
  });

  test('ignores a non-finite savings_percentage and falls back', () => {
    const r = mk({ savings_percentage: Number.NaN, savings: 45, on_demand_cost: 150 });
    expect(displaySavingsPct(r)).toBeCloseTo(30, 1);
  });

  test('returns null when neither provider % nor a computable fallback exists', () => {
    // No provider %, AWS rec with no on_demand_cost => effectiveSavingsPct is
    // null (#323) => displaySavingsPct is null => column renders an em-dash.
    const r = mk({ provider: 'aws', savings_percentage: null, on_demand_cost: null });
    expect(displaySavingsPct(r)).toBeNull();
  });
});

describe('onDemandMonthly', () => {
  // #330: onDemandMonthly returns the provider-supplied on_demand_cost
  // directly. No reconstruction fallback — when the provider didn't
  // populate on_demand_cost, the column renders an em-dash.
  const mk = (overrides: Partial<LocalRecommendation>): LocalRecommendation => ({
    id: 'r',
    provider: 'aws',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    savings: 100,
    upfront_cost: 0,
    monthly_cost: 50,
    ...overrides,
  } as unknown as LocalRecommendation);

  test('returns provider on_demand_cost directly when populated', () => {
    // The function must return the raw provider value, not any reconstruction.
    const odm = onDemandMonthly(mk({
      on_demand_cost: 500,
      savings: 100,
      upfront_cost: 0,
      monthly_cost: 50,
      term: 1,
    }));
    expect(odm).toBe(500);
  });

  test('null on_demand_cost returns null (no reconstruction fallback)', () => {
    // Explicit null — common for legacy cached recs that pre-date #277/#312.
    expect(onDemandMonthly(mk({ on_demand_cost: null }))).toBeNull();
  });

  test('explicit undefined on_demand_cost returns null', () => {
    // TypeScript's strict mode lets callers pass `undefined` rather than
    // omitting the field; assert that branch explicitly so a future
    // `r.on_demand_cost > 0` guard regression doesn't accept undefined.
    expect(onDemandMonthly(mk({ on_demand_cost: undefined }))).toBeNull();
  });

  test('missing on_demand_cost field returns null', () => {
    // Distinct from the explicit-undefined case: the property never
    // existed on the object (e.g. legacy cached recs from before #277).
    const r = mk({});
    delete (r as { on_demand_cost?: unknown }).on_demand_cost;
    expect(onDemandMonthly(r)).toBeNull();
  });

  test('on_demand_cost=0 returns null (provider treats 0 as not-populated)', () => {
    // Same convention as the on_demand_cost preference branch in
    // effectiveSavingsPct: zero means the provider didn't report it.
    expect(onDemandMonthly(mk({ on_demand_cost: 0 }))).toBeNull();
  });

  test('positive on_demand_cost wins regardless of monthly_cost / term / upfront', () => {
    // Whatever the supporting fields say, the provider value is authoritative.
    expect(onDemandMonthly(mk({
      on_demand_cost: 1500,
      monthly_cost: null,
      term: 0,
      upfront_cost: 99999,
    }))).toBe(1500);
  });
});
