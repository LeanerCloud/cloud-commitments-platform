import * as state from '../state';
import type { CostPeriod } from '../state';
import { expandedCells, recsState } from './recsState';
import { getColumnLabel } from './columnDefs';
import { clearAllColumnFilters, rerenderRecommendations } from './popover';
import { mountColumnsButton } from './visibility';

//

// Render (or update) the filter-status bar: a "Clear filters (N)" button
// when at least one column filter is active, plus an aria-live region
// announcing visible vs loaded counts. Mounted above the table; survives
// container.innerHTML rewrites because it lives outside #recommendations-list.
export function renderFilterStatusBar(loadedCount: number, visibleCount: number): void {
  const recsTab = document.getElementById('opportunities-tab');
  const list = document.getElementById('recommendations-list');
  if (!recsTab || !list) return;

  let bar = document.getElementById('recommendations-filter-status');
  if (!bar) {
    bar = document.createElement('div');
    bar.id = 'recommendations-filter-status';
    bar.className = 'recommendations-filter-status';
    list.parentNode?.insertBefore(bar, list);
  }

  // Build content fresh on every call. We set textContent on the live
  // region directly so screen readers fire announcements on actual changes.
  let badge = bar.querySelector<HTMLButtonElement>('.clear-filters');
  let live = bar.querySelector<HTMLElement>('.recommendations-filter-live');

  if (!live) {
    live = document.createElement('span');
    live.className = 'recommendations-filter-live';
    live.setAttribute('aria-live', 'polite');
    live.setAttribute('aria-atomic', 'true');
    bar.appendChild(live);
  }
  // Always update the live region (even when no filters active) so the
  // spoken count reflects state after every render. #279: when the user
  // has ticked ≥1 visible row, the line surfaces the selection count too —
  // same narrowing source-of-truth as the summary cards. Selections that
  // are filtered out of view are NOT counted here, matching the card
  // narrowing behaviour (only selected ∩ visible drives the UI).
  const selectedIDs = state.getSelectedRecommendationIDs();
  const visibleRecs = state.getVisibleRecommendations();
  const selectedVisibleCount = visibleRecs.filter((r) => selectedIDs.has(r.id)).length;
  live.textContent = selectedVisibleCount > 0
    ? `${selectedVisibleCount} selected · Showing ${visibleCount} of ${loadedCount} recommendations`
    : `Showing ${visibleCount} of ${loadedCount} recommendations`;

  const filters = state.getRecommendationsColumnFilters();
  const activeCount = Object.keys(filters).length;
  if (activeCount === 0) {
    if (badge) badge.remove();
  } else {
    if (!badge) {
      badge = document.createElement('button');
      badge.type = 'button';
      badge.className = 'clear-filters';
      badge.addEventListener('click', clearAllColumnFilters);
      bar.insertBefore(badge, live);
    }
    badge.textContent = `Clear filters (${activeCount})`;
  }

  // issues #225 + #226: Expand-All toggle button. Only render when there
  // are multi-variant cells to expand (recsState.lastVisibleGroupKeys has entries).
  // The button label flips between "Expand all" and "Collapse all"
  // depending on whether all known groups are currently expanded.
  // issues #225 + #226: Expand-All toggle. Only shown when multi-variant cells exist.
  let expandAllBtn = bar.querySelector<HTMLButtonElement>('.expand-all-toggle');
  const multiVariantGroups = recsState.lastVisibleGroupKeys.length > 0;
  if (!multiVariantGroups) {
    if (expandAllBtn) expandAllBtn.remove();
  } else {
    if (!expandAllBtn) {
      expandAllBtn = document.createElement('button');
      expandAllBtn.type = 'button';
      expandAllBtn.className = 'expand-all-toggle';
      bar.appendChild(expandAllBtn);
      expandAllBtn.addEventListener('click', () => {
        const allExpanded = recsState.lastVisibleGroupKeys.every((k) => expandedCells.has(k));
        if (allExpanded) {
          // Collapse all: clear every key that belongs to the current visible set.
          for (const k of recsState.lastVisibleGroupKeys) expandedCells.delete(k);
        } else {
          // Expand all: add every visible group key.
          for (const k of recsState.lastVisibleGroupKeys) expandedCells.add(k);
        }
        rerenderRecommendations();
      });
    }
    // Update label to reflect current state.
    const allExpanded = recsState.lastVisibleGroupKeys.every((k) => expandedCells.has(k));
    expandAllBtn.textContent = allExpanded ? 'Collapse all' : 'Expand all';
  }

  // issue #319: cost-period selector. Always mount regardless of group state.
  mountCostPeriodSelector(bar);

  // issue #318: "Columns ▾" button — mount-once, always visible.
  // Shows the column-visibility popover; label updates on re-render to
  // reflect any active hidden-column count.
  mountColumnsButton(bar);

  mountSavingsStripeLegend(bar);

  // Sort-hidden indicator: show a note when the active sort column is hidden
  // so users aren't confused by inexplicable row ordering (#318).
  let sortHiddenNote = bar.querySelector<HTMLSpanElement>('.sort-hidden-note');
  const sortCol = state.getRecommendationsSort().column;
  const hidden = state.getHiddenColumns();
  if (hidden.has(sortCol)) {
    if (!sortHiddenNote) {
      sortHiddenNote = document.createElement('span');
      sortHiddenNote.className = 'sort-hidden-note';
      bar.appendChild(sortHiddenNote);
    }
    // Use the period-aware label so the note mirrors the active period.
    const hiddenLabel = getColumnLabel(sortCol, state.getCostPeriod());
    sortHiddenNote.textContent = `(sorted by hidden column: ${hiddenLabel})`;
  } else {
    if (sortHiddenNote) sortHiddenNote.remove();
  }
}

// Rows with large savings carry a coloured stripe on their left edge
// (tr.high-savings / tr.medium-savings, set in buildVariantRowMarkup from the
// monthly savings). The legend names the two thresholds; mounted once.
function mountSavingsStripeLegend(bar: HTMLElement): void {
  if (bar.querySelector('.savings-stripe-legend')) return;
  const legend = document.createElement('span');
  legend.className = 'savings-stripe-legend';
  const items: Array<[string, string]> = [
    ['high', 'Over $1,000/mo savings'],
    ['medium', 'Over $100/mo'],
  ];
  for (const [tone, text] of items) {
    const item = document.createElement('span');
    item.className = 'savings-stripe-legend-item';
    const swatch = document.createElement('span');
    swatch.className = `savings-stripe-swatch savings-stripe-swatch--${tone}`;
    swatch.setAttribute('aria-hidden', 'true');
    item.append(swatch, text);
    legend.appendChild(item);
  }
  legend.title = 'Row marker on the left edge: monthly savings of the recommendation';
  bar.appendChild(legend);
}

/**
 * Mount (or re-sync) the cost-period dropdown inside the filter-status bar.
 * The dropdown is mounted once then kept in sync with state.getCostPeriod()
 * on subsequent calls. Changing the dropdown triggers a rerenderRecommendations
 * and persists the choice in localStorage.
 */
function mountCostPeriodSelector(bar: HTMLElement): void {
  let wrapper = bar.querySelector<HTMLElement>('.cost-period-selector-wrapper');
  if (!wrapper) {
    wrapper = document.createElement('span');
    wrapper.className = 'cost-period-selector-wrapper';

    const label = document.createElement('label');
    label.className = 'cost-period-selector-label';
    label.htmlFor = 'cost-period-select';
    label.textContent = 'Show costs: ';
    wrapper.appendChild(label);

    const select = document.createElement('select');
    select.id = 'cost-period-select';
    select.className = 'cost-period-select';
    select.setAttribute('aria-label', 'Cost display period');

    const options: Array<[CostPeriod, string]> = [
      ['hourly', 'Hourly'],
      ['daily', 'Daily'],
      ['monthly', 'Monthly'],
      ['yearly', 'Yearly'],
    ];
    for (const [value, text] of options) {
      const opt = document.createElement('option');
      opt.value = value;
      opt.textContent = text;
      select.appendChild(opt);
    }

    select.addEventListener('change', () => {
      const newPeriod = select.value as CostPeriod;
      state.setCostPeriod(newPeriod);
      rerenderRecommendations();
    });

    wrapper.appendChild(select);
    bar.appendChild(wrapper);
  }

  // Sync the selected option to the current period state (handles reload from localStorage).
  const select = wrapper.querySelector<HTMLSelectElement>('.cost-period-select');
  if (select) {
    select.value = state.getCostPeriod();
  }
}

// renderBulkToolbar was the old "N selected / Add to plan / Clear" pill that
// rendered above the table whenever any rows were selected. Bundle B folded
// that surface into the sticky bottom action box: the selection-summary text
// is in updateBottomActionBox, and the Create-Plan button in the bottom box
// supersedes the old "Add to plan". Function removed; tests for its DOM
// (bulk-count etc.) are gone too.
//
// For "Clear selection" affordance, see the bottom box's selection-summary
// text — when a selection exists the summary is followed by the row-checkbox
// columns (per-row deselect) and the all-selectAll checkbox in the table
// header (selectAll's third state clears).

// (Bundle B) selectedRecsFromVisible was inlined into resolvePurchaseTarget
// inside the bottom action box. The old helper had only one caller; folding
// keeps the action-target logic centralised in one place.
