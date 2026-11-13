import { groupSelection, planSelectionLimit } from '../lib/plan-groups';
import type { Recommendation } from '../api/types';

const rec = (id: string, over: Partial<Recommendation> = {}): Recommendation =>
  ({ id, provider: 'aws', service: 'ec2', term: 3, payment: 'all-upfront', ...over }) as Recommendation;

describe('groupSelection', () => {
  test('a homogeneous selection is one group holding every id in order', () => {
    expect(groupSelection([rec('a'), rec('b')], 70)).toEqual([
      { provider: 'aws', service: 'ec2', term: 3, payment: 'all-upfront', coverage: 70, recommendation_ids: ['a', 'b'] },
    ]);
  });

  test('provider, service, term and payment each split a group', () => {
    const groups = groupSelection([
      rec('a'), rec('b', { provider: 'azure' }), rec('c', { service: 'rds' }), rec('d', { term: 1 }), rec('e', { payment: 'no-upfront' }), rec('f'),
    ], 80);
    expect(groups.map((g) => g.recommendation_ids)).toEqual([['a', 'f'], ['b'], ['c'], ['d'], ['e']]);
  });
});

describe('planSelectionLimit', () => {
  const many = (n: number, over: Partial<Recommendation> = {}) => Array.from({ length: n }, (_, i) => rec(`${JSON.stringify(over)}-${i}`, over));

  test('accepts exactly 1000 in a group and 5000 in total', () => {
    expect(planSelectionLimit(many(1000))).toBeNull();
    const five = [...many(1000), ...many(1000, { service: 'rds' }), ...many(1000, { service: 'cache' }), ...many(1000, { term: 1 }), ...many(1000, { provider: 'azure' })];
    expect(five).toHaveLength(5000);
    expect(planSelectionLimit(five)).toBeNull();
  });

  test('refuses 1001 in one group', () => {
    expect(planSelectionLimit(many(1001))).toContain('at most 1000');
  });

  test('refuses 5001 in total even when every group fits', () => {
    const six = [...many(1000), ...many(1000, { service: 'rds' }), ...many(1000, { service: 'cache' }), ...many(1000, { term: 1 }), ...many(1000, { provider: 'azure' }), ...many(1, { service: 'x' })];
    expect(planSelectionLimit(six)).toContain('at most 5000');
  });

  test('refuses more than 200 groups and accepts exactly 200', () => {
    const groups = (n: number) => Array.from({ length: n }, (_, i) => rec(`g${i}`, { service: `svc-${i}` }));
    expect(planSelectionLimit(groups(200))).toBeNull();
    expect(planSelectionLimit(groups(201))).toContain('at most 200 groups');
  });
});
