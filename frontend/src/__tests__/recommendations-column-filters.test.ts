/**
 * Recommendations module tests: applyColumnFilters, the filter accessors and the first column header filter tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { applyColumnFilters } from '../recommendations';
import type { LocalRecommendation } from '../types';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('applyColumnFilters', () => {
  const rec = (
    overrides: Partial<LocalRecommendation> = {},
  ): LocalRecommendation => ({
    id: overrides.id ?? 'r1',
    provider: overrides.provider ?? 'aws',
    cloud_account_id: overrides.cloud_account_id ?? 'acct-1',
    service: overrides.service ?? 'ec2',
    resource_type: overrides.resource_type ?? 't3.medium',
    region: overrides.region ?? 'us-east-1',
    count: overrides.count ?? 1,
    term: overrides.term ?? 1,
    upfront_cost: overrides.upfront_cost ?? 100,
    monthly_cost: overrides.monthly_cost ?? 10,
    savings: overrides.savings ?? 50,
    engine: overrides.engine,
  } as unknown as LocalRecommendation);

  test('empty filters returns a clone of the input (no-op)', () => {
    const recs = [rec({ id: 'a' }), rec({ id: 'b' })];
    const out = applyColumnFilters(recs, {});
    expect(out).toEqual(recs);
    expect(out).not.toBe(recs); // defensive clone
  });

  test('categorical set filter narrows by membership', () => {
    const recs = [
      rec({ id: 'a', provider: 'aws' }),
      rec({ id: 'b', provider: 'azure' }),
      rec({ id: 'c', provider: 'gcp' }),
    ];
    const out = applyColumnFilters(recs, {
      provider: { kind: 'set', values: ['aws'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('Account filter matches on cloud_account_id, not display name', () => {
    const recs = [
      rec({ id: 'a', cloud_account_id: 'acct-prod' }),
      rec({ id: 'b', cloud_account_id: 'acct-dev' }),
    ];
    const out = applyColumnFilters(recs, {
      account: { kind: 'set', values: ['acct-prod'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('Term filter values are strings; row term integer stringifies', () => {
    const recs = [
      rec({ id: 'a', term: 1 }),
      rec({ id: 'b', term: 3 }),
    ];
    const out = applyColumnFilters(recs, {
      term: { kind: 'set', values: ['3'] },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });

  test('(empty) sentinel matches null / undefined / "" cloud_account_id', () => {
    // Build raw objects — bypass the rec() factory because its `??` defaults
    // would replace null/undefined cloud_account_id with 'acct-1'.
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), cloud_account_id: '' } as LocalRecommendation,
      { ...rec({ id: 'b' }), cloud_account_id: undefined } as unknown as LocalRecommendation,
      rec({ id: 'c', cloud_account_id: 'acct-x' }),
    ];
    const out = applyColumnFilters(recs, {
      account: { kind: 'set', values: [''] },
    });
    expect(out.map(r => r.id).sort()).toEqual(['a', 'b']);
  });

  test('numeric expr filter narrows by predicate', () => {
    const recs = [
      rec({ id: 'a', savings: 25 }),
      rec({ id: 'b', savings: 150 }),
      rec({ id: 'c', savings: 1500 }),
    ];
    const out = applyColumnFilters(recs, {
      savings: { kind: 'expr', expr: '>100' },
    });
    expect(out.map(r => r.id)).toEqual(['b', 'c']);
  });

  test('invalid numeric expression is ignored (filter not applied)', () => {
    const recs = [rec({ id: 'a', savings: 1 }), rec({ id: 'b', savings: 100 })];
    const out = applyColumnFilters(recs, {
      savings: { kind: 'expr', expr: '>>5' }, // syntax error
    });
    // All rows pass — broken filter is silently inert.
    expect(out.map(r => r.id)).toEqual(['a', 'b']);
  });

  test('multiple column filters AND together', () => {
    const recs = [
      rec({ id: 'a', provider: 'aws', savings: 50 }),
      rec({ id: 'b', provider: 'aws', savings: 500 }),
      rec({ id: 'c', provider: 'azure', savings: 500 }),
    ];
    const out = applyColumnFilters(recs, {
      provider: { kind: 'set', values: ['aws'] },
      savings: { kind: 'expr', expr: '>100' },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });

  test('capacity filter matches formatted "N vCPU / M GB" string', () => {
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), vcpu: 8,   memory_gb: 32 } as LocalRecommendation,
      { ...rec({ id: 'b' }), vcpu: 4,   memory_gb: 16 } as LocalRecommendation,
      { ...rec({ id: 'c' }), vcpu: null, memory_gb: null } as unknown as LocalRecommendation,
    ];
    const out = applyColumnFilters(recs, {
      capacity: { kind: 'set', values: ['8 vCPU / 32 GB'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('capacity filter with empty string matches recs without vcpu/memory_gb', () => {
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), vcpu: 8,   memory_gb: 32 } as LocalRecommendation,
      { ...rec({ id: 'b' }), vcpu: null, memory_gb: null } as unknown as LocalRecommendation,
    ];
    const out = applyColumnFilters(recs, {
      capacity: { kind: 'set', values: [''] },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });
});

//
// Bundle A: state-accessor tests for the new column-filter / visible-recs API.
// These import the REAL state module (the recommendations.test.ts above mocks
// it; here we exercise the actual implementation in a separate require scope).
//

describe('state.ts column-filter accessors', () => {
  // The top-level jest.mock('../state', …) replaces the module for every
  // import. Use jest.requireActual to bypass it for these state-accessor
  // tests so we exercise the real implementation. Each test starts from
  // a clean filter state via clearAllRecommendationsColumnFilters().
  const realState = jest.requireActual<typeof import('../state')>('../state');

  beforeEach(() => {
    realState.clearAllRecommendationsColumnFilters();
    realState.setVisibleRecommendations([]);
  });

  test('default filters are empty', () => {
    expect(realState.getRecommendationsColumnFilters()).toEqual({});
  });

  test('setRecommendationsColumnFilter adds an entry', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    expect(realState.getRecommendationsColumnFilters()).toEqual({
      provider: { kind: 'set', values: ['aws'] },
    });
  });

  test('passing null clears that single column', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    realState.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    realState.setRecommendationsColumnFilter('provider', null);
    expect(realState.getRecommendationsColumnFilters()).toEqual({
      savings: { kind: 'expr', expr: '>100' },
    });
  });

  test('clearAllRecommendationsColumnFilters empties the record', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    realState.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    realState.clearAllRecommendationsColumnFilters();
    expect(realState.getRecommendationsColumnFilters()).toEqual({});
  });
});
