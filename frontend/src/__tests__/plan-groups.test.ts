import { groupSelection } from '../lib/plan-groups';
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
