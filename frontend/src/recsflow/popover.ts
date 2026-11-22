import * as state from '../state';
import { isSavingsPlanService } from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import type { ColumnFilterKind } from '../lib/column-filters';
import {
  type ColumnFilterValueGroup,
  closeColumnFilterPopoverForScope,
  openColumnFilterPopover,
  rebindColumnFilterPopover,
} from '../lib/column-filter-popover';
import { columnLabel, NUMERIC_COLUMNS } from './columnDefs';
import { saveColumnFilters } from './columnPrefs';
import { applyColumnFilters, categoricalDisplayLabel, distinctValuesForColumn } from './cellValues';
import { closeVisibilityPopover } from './visibility';
import { renderRecommendationsList } from './renderList';
import { loadRecommendations } from './load';

const RECS_SCOPE = 'recs';

// Columns whose single-value categorical filter is pushed down to the server
// (issue #162): a commit or clear on one of these re-fetches instead of
// filtering client-side. 'account' drives account_ids, a server-side hint.
const SERVER_PUSHDOWN_COLUMNS: ReadonlySet<state.RecommendationsColumnId> = new Set([
  'service', 'region', 'account',
]);

function getColumnTriggerButton(column: state.RecommendationsColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `th .column-filter-btn[data-column="${column}"]`,
  );
}

function commitColumnFilter(column: state.RecommendationsColumnId, filter: ColumnFilterKind | null): void {
  state.setRecommendationsColumnFilter(column, filter);
  saveColumnFilters(state.getRecommendationsColumnFilters());
  // Server-pushdown columns (issue #162) re-fetch so the backend prunes rows
  // before they hit the wire; every other column filters client-side. The
  // re-fetch is async/void: the table skeleton renders while it is in flight.
  if (SERVER_PUSHDOWN_COLUMNS.has(column)) {
    void loadRecommendations();
  } else {
    rerenderRecommendations();
  }
}

export function openColumnPopover(column: state.RecommendationsColumnId, anchor: HTMLElement): void {
  const numeric = NUMERIC_COLUMNS.has(column);
  let distinctValues: string[] | undefined;
  let valueGroups: ColumnFilterValueGroup[] | undefined;
  if (!numeric) {
    const recs = state.getRecommendations() as unknown as LocalRecommendation[];
    // Cross-column-aware distinct values (issue #164): build the checkbox list
    // from rows that pass all OTHER active filters, not the full unfiltered set.
    // This way, selecting Provider=AWS first narrows the Service popover to only
    // AWS services -- values that produce non-empty results.
    //
    // Mitigation for the "broaden a single column" UX: any value that is part of
    // the column's own currently-active filter is always included even if the
    // cross-filtered set would omit it (e.g. opening Provider popover while
    // Provider=AWS + Service=ec2 are both active still shows AWS so the user can
    // deselect it without first clearing the Service filter).
    const allFilters = state.getRecommendationsColumnFilters();
    const filtersExceptThisColumn: state.RecommendationsColumnFilters = { ...allFilters };
    delete filtersExceptThisColumn[column];
    const ownFilter = allFilters[column];
    const alwaysInclude: ReadonlySet<string> = ownFilter && ownFilter.kind === 'set'
      ? new Set(ownFilter.values)
      : new Set();
    distinctValues = distinctValuesForColumn(applyColumnFilters(recs, filtersExceptThisColumn), column, alwaysInclude);
    // Issue #137: with 2+ SP plan-type slugs in the service popover, offer an
    // "All Savings Plans" group toggle scoped to the SP slugs.
    const spSlugs = column === 'service' ? distinctValues.filter((v) => isSavingsPlanService(v)) : [];
    if (spSlugs.length >= 2) {
      valueGroups = [{ role: 'sp-group', label: 'All Savings Plans', members: spSlugs }];
    }
  }
  openColumnFilterPopover({
    scope: RECS_SCOPE,
    column,
    label: columnLabel(column),
    kind: numeric ? 'numeric' : 'categorical',
    distinctValues,
    displayLabel: (v) => categoricalDisplayLabel(column, v),
    valueGroups,
    getFilter: () => state.getRecommendationsColumnFilters()[column],
    onCommit: (filter) => commitColumnFilter(column, filter),
    findTrigger: () => getColumnTriggerButton(column),
    followOnScroll: true,
  }, anchor);
}

export function closePopover(restoreFocus = false): void {
  closeColumnFilterPopoverForScope(RECS_SCOPE, restoreFocus);
}

// Called by renderRecommendationsList after the table is rebuilt to re-anchor
// any open popover to the freshly-rendered trigger button.
export function rebindOpenPopoverAnchor(): void {
  rebindColumnFilterPopover(RECS_SCOPE);
}

/**
 * Clears every column filter. Re-fetches when a server-pushed-down column
 * (service/region/account) was filtered, since the loaded set itself was
 * narrowed; otherwise a client-side rerender is enough.
 */
export function clearAllColumnFilters(): void {
  const active = Object.keys(state.getRecommendationsColumnFilters()) as state.RecommendationsColumnId[];
  const refetch = active.some((c) => SERVER_PUSHDOWN_COLUMNS.has(c));
  state.clearAllRecommendationsColumnFilters();
  saveColumnFilters(state.getRecommendationsColumnFilters());
  if (refetch) void loadRecommendations();
  else rerenderRecommendations();
}

/**
 * Hides (or restores) the summary cards, filter-status bar and bottom action
 * box around the list. Used by the first-run empty state (issue #756), where
 * zero counts and a disabled Purchase bar only add noise.
 */
export function setOpportunitiesChromeHidden(hidden: boolean): void {
  for (const id of ['recommendations-summary', 'recommendations-filter-status', 'recommendations-action-box']) {
    document.getElementById(id)?.classList.toggle('hidden', hidden);
  }
}

// rerenderRecommendations triggers a full re-render from the latest loaded
// state. Used by popover commits and the Clear-filters badge so the table
// reflects new column-filter state immediately.
//
// #272 (CR follow-up): the summary card is recomputed inside
// renderRecommendationsList itself (against the post-filter visible set),
// so this helper doesn't need to call renderRecommendationsSummary
// separately — every entry to renderRecommendationsList covers it.
export function rerenderRecommendations(): void {
  const loaded = state.getRecommendations() as unknown as LocalRecommendation[];
  renderRecommendationsList(loaded);
}

// Close the popover when the Opportunities tab loses .active, so the
// detached popover doesn't float over other tabs' content. Wired via a
// MutationObserver on the opportunities-tab element.
let recommendationsTabObserver: MutationObserver | null = null;
export function ensureRecommendationsTabObserver(): void {
  if (recommendationsTabObserver) return;
  const tab = document.getElementById('opportunities-tab');
  if (!tab) return;
  recommendationsTabObserver = new MutationObserver(() => {
    if (tab.classList.contains('active')) return;
    closeColumnFilterPopoverForScope(RECS_SCOPE);
    closeVisibilityPopover();
  });
  recommendationsTabObserver.observe(tab, { attributes: true, attributeFilter: ['class'] });
}
