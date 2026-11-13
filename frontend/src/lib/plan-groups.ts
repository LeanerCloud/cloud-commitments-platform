import type { PlanGroupRequest, Recommendation } from '../api/types';

// groupSelection splits a selection into one plan group per
// (provider, service, term, payment), each carrying its recommendation ids, so
// the backend binds the plan to exactly the selected recommendations (#1746).
export function groupSelection(recs: readonly Recommendation[], coverage: number): PlanGroupRequest[] {
  const groups = new Map<string, PlanGroupRequest>();
  for (const rec of recs) {
    const key = JSON.stringify([rec.provider, rec.service, rec.term, rec.payment]);
    let group = groups.get(key);
    if (!group) {
      group = {
        provider: rec.provider, service: rec.service, term: rec.term, payment: rec.payment,
        coverage, recommendation_ids: [],
      };
      groups.set(key, group);
    }
    group.recommendation_ids.push(rec.id);
  }
  return [...groups.values()];
}
