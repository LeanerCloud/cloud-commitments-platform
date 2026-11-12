import { planFilterLines, renderPlanFiltersNotice } from '../plan-filters-notice';

describe('planFilterLines (#1737)', () => {
  test('names every filter kind, sorted by service key', () => {
    expect(planFilterLines({
      'aws/rds': { exclude_engines: ['mysql'], exclude_regions: ['eu-west-1'], include_types: ['db.r6g'], exclude_types: ['db.t3'] },
      'aws/ec2': { include_engines: ['linux'], min_count: 3 },
    })).toEqual([
      'aws/ec2: include engines: linux; minimum count: 3',
      'aws/rds: exclude engines: mysql; exclude regions: eu-west-1; include types: db.r6g; exclude types: db.t3',
    ]);
  });

  test('a bound entry min_count of 1 is the rollback sentinel, a plain entry is not', () => {
    expect(planFilterLines({ 'aws/ec2#g1': { min_count: 1 } })).toEqual([]);
    expect(planFilterLines({ 'aws/ec2#g1': { min_count: 2 } })).toEqual(['aws/ec2#g1: minimum count: 2']);
    expect(planFilterLines({ 'aws/ec2': { min_count: 1 } })).toEqual(['aws/ec2: minimum count: 1']);
  });

  test('empty lists, zero count and missing services give nothing', () => {
    expect(planFilterLines({ 'aws/ec2': { include_regions: [], min_count: 0 } })).toEqual([]);
    expect(planFilterLines(undefined)).toEqual([]);
  });
});

describe('renderPlanFiltersNotice (#1737)', () => {
  test('clears a previous plan notice and hides it', () => {
    const host = document.createElement('div');
    renderPlanFiltersNotice(host, { 'aws/ec2': { include_regions: ['us-east-1'] } });
    expect(host.classList.contains('hidden')).toBe(false);
    renderPlanFiltersNotice(host, undefined);
    expect(host.classList.contains('hidden')).toBe(true);
    expect(host.childElementCount).toBe(0);
  });

  test('tolerates a missing host', () => {
    expect(() => renderPlanFiltersNotice(null, { 'aws/ec2': { min_count: 2 } })).not.toThrow();
  });
});
