import * as api from '../api';
import * as state from '../state';
import { getRecommendationsFreshness } from '../api/recommendations';
import type { LocalRecommendation } from '../types';
import { teardownSkeleton } from '../lib/skeleton';
import { captureFocus, restoreFocus } from '../lib/focus-restore';
import { buildFirstRunEmptyState, isScopeFiltered } from '../first-run-empty-state';
import { expandedCells, expandedSpGroups, recsState } from './recsState';
import { canActOnRecommendations } from './shared';
import { loadColumnVisibility, loadColumnFilters, visibleColumns } from './columnPrefs';
import { applyColumnFilters } from './cellValues';
import { cellKey, pickBestVariantPerCell } from './groups';
import { defaultSortDirectionFor } from './sort';
import { openColumnPopover, rebindOpenPopoverAnchor, clearAllColumnFilters, setOpportunitiesChromeHidden, ensureRecommendationsTabObserver } from './popover';
import { renderFilterStatusBar } from './statusBar';
import { buildListMarkup } from './listMarkup';
import { renderRecommendationsSummary } from './summary';
import { mountBottomActionBox, updateBottomActionBox, openCreatePlanFromBottomBox } from './bottomBox';
import { writeSortToUrl, loadRecommendations } from './load';
import { startRecommendationsRefresh } from './refresh';
import { attachArcheraQuoteControls } from '../archera-recommendation';

export function renderRecommendationsList(loadedRecs: LocalRecommendation[]): void {
  const container = document.getElementById('recommendations-list');
  if (!container) return;

  // Seed column filters from localStorage on the first render (issue #163).
  // recsState.columnFiltersSeeded stays true for the rest of the session so in-session
  // changes are not overwritten on subsequent rerenders.
  if (!recsState.columnFiltersSeeded) {
    const persisted = loadColumnFilters();
    for (const [col, filter] of Object.entries(persisted)) {
      state.setRecommendationsColumnFilter(
        col as state.RecommendationsColumnId,
        filter,
      );
    }
    recsState.columnFiltersSeeded = true;
  }

  // Seed column visibility from localStorage on the first render.
  // recsState.columnVisibilitySeeded stays true for the rest of the session so
  // in-session toggles (via the "Columns ▾" popover) are not overwritten.
  if (!recsState.columnVisibilitySeeded) {
    state.setHiddenColumns(loadColumnVisibility());
    recsState.columnVisibilitySeeded = true;
  }

  // Pipeline:
  //   loaded -> applyColumnFilters -> visible
  //   state.setVisibleRecommendations(visible)   (read by plans.ts:savePlan)
  // When the column-filters record is empty, applyColumnFilters returns a
  // clone of the input.
  const recommendations = applyColumnFilters(
    loadedRecs ?? [],
    state.getRecommendationsColumnFilters(),
  );
  state.setVisibleRecommendations(recommendations as unknown as readonly api.Recommendation[]);

  // Keep the savings card in sync with the visible (post-filter) set on
  // every list rerender so the card and the per-cell banner under the
  // table never diverge (#272 CR follow-up). The cached
  // recsState.lastRecommendationsSummary holds the API-derived counts (total_count,
  // total_upfront_cost, avg_payback_months); the savings figure itself is
  // recomputed from `recommendations` inside renderRecommendationsSummary
  // via the same pageLevelRange the banner uses.
  renderRecommendationsSummary(recsState.lastRecommendationsSummary, recommendations, loadedRecs?.length ?? 0);

  // Mount once; update is per-render below.
  mountBottomActionBox();

  // Issue #756: nothing loaded and no column filter narrowing the fetch is a
  // first run (or an empty scope), not a filter miss. Show an onboarding card
  // instead of an empty 15-column table, $0 summary cards and a Purchase bar
  // that can never enable. The summary and action box stay rendered (disabled)
  // but hidden, so state is coherent once rows arrive.
  const firstRun = (loadedRecs?.length ?? 0) === 0
    && Object.keys(state.getRecommendationsColumnFilters()).length === 0;
  setOpportunitiesChromeHidden(firstRun);
  if (firstRun) {
    recsState.lastVisibleGroupKeys = [];
    updateBottomActionBox(0, 0);
    teardownSkeleton(container);
    container.replaceChildren(buildFirstRunEmptyState({
      hasAccounts: recsState.lastHasAccounts,
      scopeFiltered: isScopeFiltered(state.getCurrentProvider(), state.getCurrentAccountIDs()),
      onRefresh: () => startRecommendationsRefresh(loadRecommendations),
      loadFreshness: getRecommendationsFreshness,
    }));
    return;
  }

  const emptyResult = !recommendations || recommendations.length === 0;
  if (emptyResult) {
    recsState.lastVisibleGroupKeys = [];
  }

  // Compute the visible column set once per render — passed to buildListMarkup
  // so header and row rendering use the same snapshot of column visibility state.
  const visibleCols = visibleColumns();

  // Issue #869: viewer (readonly) sessions have no purchase/plan actions;
  // hide the checkbox column entirely so the table doesn't have a useless
  // selection surface. The bottom action box already hides its CTA buttons
  // for this role (mountBottomActionBox, issue #365).
  const showCheckboxes = canActOnRecommendations();

  const selectedIDs = state.getSelectedRecommendationIDs();
  // Dynamic table markup: every caller-provided value passes through
  // escapeHtml or is a number. The string is built in buildListMarkup.
  // NOTE: buildListMarkup also populates recsState.lastVisibleGroupKeys, so it MUST
  // run before renderFilterStatusBar (which reads it for the Expand-All button).
  // safe: buildListMarkup escapes all API-derived values via escapeHtml.
  // nosec: innerHTML is intentional here; see security note above.
  // Issue #191: the rebuild below drops the focused control (row checkbox,
  // chevron, sort header); put keyboard focus back on its replacement.
  const focusSnapshot = captureFocus(container);
  container.innerHTML = buildListMarkup(recommendations ?? [], selectedIDs, visibleCols, showCheckboxes); // nosec
  restoreFocus(container, focusSnapshot);

  // Issue #700: when the filter yields zero rows, preserve the <thead> by
  // injecting a hint row into the empty <tbody> rather than replacing the
  // entire table with a <p>. The column headers remain visible so the user
  // can see which columns are active while they adjust filters.
  if (emptyResult) {
    const tbody = container.querySelector('tbody');
    if (tbody) {
      // colspan = leading col (always 1: checkbox for editors, empty for viewers) + all visible data columns.
      const colspan = 1 + visibleCols.length;
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.setAttribute('colspan', String(colspan));
      td.className = 'empty';
      td.textContent = 'No rows match these filters.';
      // Issue #756: give the hint a next step right where the user looks.
      if (Object.keys(state.getRecommendationsColumnFilters()).length > 0) {
        const clearBtn = document.createElement('button');
        clearBtn.type = 'button';
        clearBtn.className = 'btn-small empty-clear-filters';
        clearBtn.textContent = 'Clear filters';
        clearBtn.addEventListener('click', clearAllColumnFilters);
        td.append(' ', clearBtn);
      }
      tr.appendChild(td);
      tbody.appendChild(tr);
    }
  }

  const visibleCount = recommendations?.length ?? 0;

  // Filter status: Clear-filters badge + aria-live count + Expand-All toggle.
  // Rendered AFTER buildListMarkup so recsState.lastVisibleGroupKeys is populated.
  // Mounted as a sibling above the table so it survives the container's
  // innerHTML rewrite without losing aria-live announcements.
  renderFilterStatusBar(loadedRecs?.length ?? 0, visibleCount);

  updateBottomActionBox(visibleCount, loadedRecs?.length ?? visibleCount);


  // Per-column filter button: trigger opens the popover anchored to the
  // button. e.stopPropagation prevents the surrounding <th>'s sort handler
  // from also firing.
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.RecommendationsColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      openColumnPopover(column, btn);
    });
  });

  // After the table is rebuilt, re-anchor any open popover to the new
  // trigger DOM node and re-sync .checked / .value from current state.
  rebindOpenPopoverAnchor();

  // Watch the recommendations tab's class so we can close the popover if
  // the user switches away to another tab.
  ensureRecommendationsTabObserver();

  // Sortable column headers. Toggle ascending/descending on repeat click.
  // The click listener sits on the <th> so the whole header stays a mouse
  // target; keyboard users reach it through the inner .sort-btn, whose
  // native Enter/Space activation bubbles here as a click. The filter
  // button stops propagation, so it never sorts (issue #755).
  container.querySelectorAll<HTMLTableCellElement>('th.sortable').forEach((th) => {
    const onActivate = (): void => {
      const col = th.dataset['sort'];
      if (!col) return;
      // The re-render below replaces the header, so remember whether the
      // sort button had focus and move focus to its replacement.
      const hadFocus = th.contains(document.activeElement);
      const prev = state.getRecommendationsSort();
      // Issue #480: first click on a different column uses that column's
      // per-config default direction (asc for text/most numerics; desc for
      // `savings` and `on_demand_monthly`). Subsequent clicks on the active
      // column toggle desc <-> asc.
      const direction: 'asc' | 'desc' =
        prev.column === col && prev.direction === 'desc' ? 'asc'
          : prev.column === col && prev.direction === 'asc' ? 'desc'
          : defaultSortDirectionFor(col as state.RecommendationsColumnId);
      state.setRecommendationsSort({ column: col as state.RecommendationsSortColumn, direction });
      // Issue #481: persist sort to the URL so refreshes / bookmarks /
      // shareable links restore the user's column + direction. Catches the
      // common UX surprise of "I sorted by Account, refreshed, now I'm back
      // at Savings desc".
      writeSortToUrl({ column: col as state.RecommendationsSortColumn, direction });
      renderRecommendationsList(loadedRecs);
      if (hadFocus) {
        document.querySelector<HTMLButtonElement>(
          `#recommendations-list th[data-sort="${CSS.escape(col)}"] .sort-btn`,
        )?.focus();
      }
    };
    th.addEventListener('click', onActivate);
  });

  // Issue #869: selection handlers are only meaningful when the session can
  // act on recommendations (admin/operator). Skip wiring them for viewer
  // (readonly) sessions: the checkboxes are absent from the DOM anyway
  // (showCheckboxes === false), and the row-click handler would be inert
  // because there is no checkbox to toggle.
  if (showCheckboxes) {
    // Add event listeners
    const selectAllCheckbox = document.getElementById('select-all-recs') as HTMLInputElement | null;
    if (selectAllCheckbox) {
      // Issue #479: indeterminate is a DOM property only, so apply it from
      // the data attribute the renderer threaded through. Without this, a
      // partial selection shows no visual cue and clicking the header
      // repeatedly becomes a no-op because the checkbox's .checked never
      // flips.
      selectAllCheckbox.indeterminate = selectAllCheckbox.dataset['indeterminate'] === 'true';
      selectAllCheckbox.addEventListener('change', () => {
        if (selectAllCheckbox.checked) {
          // Issue #224: select-all picks ONE variant per cell (highest-effective-
          // savings) rather than every visible row. After PR #195's per-(term,
          // payment) fan-out, naive "select every row" produces 6x the intended
          // commitments per resource -- wrong purchase intent. Clear current
          // selection first so a stale choice from a different filter context
          // doesn't bleed through.
          state.clearSelectedRecommendations();
          for (const r of pickBestVariantPerCell(recommendations)) {
            state.addSelectedRecommendation(r.id);
          }
        } else {
          state.clearSelectedRecommendations();
        }
        renderRecommendationsList(loadedRecs);
      });
    }

    // ID-keyed selection toggles. data-rec-id persists across filter
    // changes so a stale selection from a previous filter is a no-op
    // once the user narrows, rather than pointing at whichever rec
    // happens to occupy the old index position.
    //
    // Issue #224: enforce one-variant-per-cell radio behaviour on check.
    // When the user checks a variant, deselect any other variant of the
    // same cell that's already selected -- a single physical resource
    // can only carry one (term, payment) commitment at a time.
    container.querySelectorAll<HTMLInputElement>('input[data-rec-id]').forEach(cb => {
      cb.addEventListener('change', () => {
        const id = cb.dataset['recId'] || '';
        if (!id) return;
        if (cb.checked) {
          const newRec = recommendations.find((r) => r.id === id);
          if (newRec) {
            const newCell = cellKey(newRec);
            const selected = state.getSelectedRecommendationIDs();
            // Scan the full loaded set (not just the filtered view) so that
            // hidden siblings (e.g. filtered-out term variants) are also
            // deselected, preserving the one-variant-per-cell contract.
            const allLoaded = state.getRecommendations() as unknown as LocalRecommendation[];
            for (const r of allLoaded) {
              if (r.id !== id && selected.has(r.id) && cellKey(r) === newCell) {
                state.removeSelectedRecommendation(r.id);
              }
            }
          }
          state.addSelectedRecommendation(id);
        } else {
          state.removeSelectedRecommendation(id);
        }
        renderRecommendationsList(loadedRecs);
      });
    });

    // Row-click toggles selection (issue #344 T4'). Clicking anywhere on
    // the row's body now toggles the row's checkbox + dispatches the
    // existing change handler -- which already enforces the
    // one-variant-per-cell radio behaviour (issue #224) and rerenders.
    // Skip clicks on the checkbox itself (its native click already
    // toggles) and on any interactive child (button / a / input / label /
    // select / [data-action]) so per-row controls keep their own
    // semantics. The previous row-click -> openDetailDrawer behaviour was
    // dropped: see plan.md T4 (the detail drawer's payload duplicated
    // the table, with backend-deferred fields the only differentiators).
    container.querySelectorAll<HTMLTableRowElement>('tr.recommendation-row').forEach((tr) => {
      tr.addEventListener('click', (e) => {
        if (!(e.target instanceof Element)) return;
        const target = e.target as HTMLElement;
        if (target.closest('input, button, a, label, select, [data-action]')) return;
        const cb = tr.querySelector<HTMLInputElement>('input[type="checkbox"][data-rec-id]');
        if (!cb) return;
        cb.checked = !cb.checked;
        cb.dispatchEvent(new Event('change', { bubbles: true }));
      });
    });
  }

  // issues #225 + #226: chevron click toggles expand/collapse for a cell group.
  // Available for all roles (expand/collapse is a view-only operation).
  container.querySelectorAll<HTMLButtonElement>('.rec-cell-chevron').forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const key = btn.dataset['cellKey'] ?? '';
      if (!key) return;
      if (expandedCells.has(key)) {
        expandedCells.delete(key);
      } else {
        expandedCells.add(key);
      }
      renderRecommendationsList(loadedRecs);
    });
  });

  // Issue #120: per-row Plan button deep-links into Create Purchase Plan modal.
  // Fires openCreatePlanFromBottomBox with the single rec so the modal is
  // pre-seeded -- same path as the bulk Create Plan button but scoped to one row.
  container.querySelectorAll<HTMLButtonElement>('button.rec-plan-btn').forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const recId = btn.dataset['recId'] ?? '';
      if (!recId) return;
      const rec = recommendations.find((r) => r.id === recId);
      if (!rec) return;
      void openCreatePlanFromBottomBox([rec]);
    });
  });

  // Issue #799: per-row Archera quote button, only when insurance is configured.
  void attachArcheraQuoteControls(container);

  // issue #135: SP group chevron click toggles expand/collapse for the plan-type group.
  container.querySelectorAll<HTMLButtonElement>('.rec-sp-group-chevron').forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const key = btn.dataset['spGroupKey'] ?? '';
      if (!key) return;
      if (expandedSpGroups.has(key)) {
        expandedSpGroups.delete(key);
      } else {
        expandedSpGroups.add(key);
      }
      renderRecommendationsList(loadedRecs);
    });
  });
}
