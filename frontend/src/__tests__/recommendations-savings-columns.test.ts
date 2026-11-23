/**
 * Recommendations module tests: effective percent preference, display savings, on-demand monthly and column rendering tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { effectiveSavingsPct } from '../recommendations';
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
    });
  });
});
