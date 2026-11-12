// Read-only notice for the per-service filters a plan can carry (issue #1737).
// Plan steps refuse any plan whose service sets one (planstep.Precheck,
// ErrUnsupportedPlan), and the edit form cannot change them, so the modal
// lists them and says why steps fail. Every server string goes in through
// textContent.

interface FilterFields {
  include_engines?: string[];
  exclude_engines?: string[];
  include_regions?: string[];
  exclude_regions?: string[];
  include_types?: string[];
  exclude_types?: string[];
  min_count?: number;
}

export type PlanServiceFilters = Record<string, FilterFields>;

const LIST_FIELDS: ReadonlyArray<[keyof FilterFields, string]> = [
  ['include_engines', 'include engines'],
  ['exclude_engines', 'exclude engines'],
  ['include_regions', 'include regions'],
  ['exclude_regions', 'exclude regions'],
  ['include_types', 'include types'],
  ['exclude_types', 'exclude types'],
];

export const PLAN_FILTERS_NOTICE =
  'This plan has filters (engines/regions/types/minimum count) that plan steps cannot honor. ' +
  'Cancel the scheduled root and recreate the plan to use plan steps.';

/** One line per service that sets a filter, sorted by service key. A bound
 *  entry ("#" in its key) with min_count 1 is the rollback sentinel, not a
 *  filter, matching planstep.firstFilterSet. */
export function planFilterLines(services: PlanServiceFilters | undefined): string[] {
  const lines: string[] = [];
  for (const key of Object.keys(services ?? {}).sort()) {
    const svc = (services ?? {})[key] as FilterFields;
    const parts: string[] = [];
    for (const [field, label] of LIST_FIELDS) {
      const values = svc[field];
      if (Array.isArray(values) && values.length > 0) parts.push(`${label}: ${values.join(', ')}`);
    }
    const min = svc.min_count;
    if (typeof min === 'number' && min !== 0 && !(key.includes('#') && min === 1)) parts.push(`minimum count: ${min}`);
    if (parts.length > 0) lines.push(`${key}: ${parts.join('; ')}`);
  }
  return lines;
}

/** Fills host with the notice and filter list, or hides it when there are none. */
export function renderPlanFiltersNotice(host: HTMLElement | null, services: PlanServiceFilters | undefined): void {
  if (!host) return;
  host.replaceChildren();
  const lines = planFilterLines(services);
  host.classList.toggle('hidden', lines.length === 0);
  if (lines.length === 0) return;
  const text = document.createElement('p');
  text.className = 'help-text';
  text.textContent = PLAN_FILTERS_NOTICE;
  const list = document.createElement('ul');
  for (const line of lines) {
    const item = document.createElement('li');
    item.textContent = line;
    list.appendChild(item);
  }
  host.append(text, list);
}
