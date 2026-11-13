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

// The backend refuses a plan request above these counts (plan_groups.go).
const MAX_GROUP_RECOMMENDATIONS = 1000;
const MAX_PLAN_RECOMMENDATIONS = 5000;
const MAX_PLAN_GROUPS = 200;

// planSelectionLimit explains why a selection is too large for one plan, or
// returns null when the backend would accept it.
export function planSelectionLimit(recs: readonly Recommendation[]): string | null {
  if (recs.length > MAX_PLAN_RECOMMENDATIONS) {
    return `A plan can hold at most ${MAX_PLAN_RECOMMENDATIONS} recommendations. Refine your selection.`;
  }
  const groups = groupSelection(recs, 0);
  if (groups.length > MAX_PLAN_GROUPS) {
    return `A plan can hold at most ${MAX_PLAN_GROUPS} groups of provider, service, term and payment. Refine your selection.`;
  }
  const largest = Math.max(0, ...groups.map((g) => g.recommendation_ids.length));
  if (largest > MAX_GROUP_RECOMMENDATIONS) {
    return `A plan can hold at most ${MAX_GROUP_RECOMMENDATIONS} recommendations of one provider, service, term and payment. Refine your selection.`;
  }
  return null;
}
