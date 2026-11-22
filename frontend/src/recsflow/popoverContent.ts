import * as state from '../state';
import { isSavingsPlanService } from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { parseNumericFilter } from '../lib/column-filters';
import { columnLabel, NUMERIC_COLUMNS } from './columnDefs';
import { saveColumnFilters } from './columnPrefs';
import { distinctValuesForColumn, categoricalDisplayLabel } from './cellValues';
import { rerenderRecommendations } from './popover';
import { loadRecommendations } from '../recommendations';

export interface PopoverState {
  column: state.RecommendationsColumnId;
  el: HTMLDivElement;
  // The categorical-popover checkboxes are keyed by their underlying filter
  // value (cloud_account_id for Account, "1"/"3" for Term, raw string
  // otherwise). Saved here so anchor re-bind can resync .checked from state.
  checkboxes: Map<string, HTMLInputElement>;
  // Numeric-popover input + error span for re-sync.
  input: HTMLInputElement | null;
  errorEl: HTMLElement | null;
  // Trigger lives in the table; rebound by `[data-column="..."]` on each
  // render so we don't hold a stale reference.
  triggerColumn: state.RecommendationsColumnId;
}

export function getColumnTriggerButton(column: state.RecommendationsColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `th .column-filter-btn[data-column="${column}"]`,
  );
}

export function positionPopover(popover: HTMLElement, anchor: HTMLElement): void {
  const rect = anchor.getBoundingClientRect();
  // Show, then measure (popover may be display:none initially).
  popover.style.display = 'block';
  const popRect = popover.getBoundingClientRect();
  const margin = 8;

  // Vertical: prefer below, flip above on overflow.
  let top = rect.bottom + 4;
  if (top + popRect.height > window.innerHeight - margin) {
    top = Math.max(margin, rect.top - popRect.height - 4);
  }

  // Horizontal: clamp right edge.
  let left = rect.left;
  if (left + popRect.width > window.innerWidth - margin) {
    left = Math.max(margin, window.innerWidth - margin - popRect.width);
  }

  popover.style.position = 'absolute';
  popover.style.top = `${top + window.scrollY}px`;
  popover.style.left = `${left + window.scrollX}px`;
}

// Columns whose single-value categorical filter is pushed down to the server
// (issue #162). When a commit on one of these columns resolves to exactly one
// selected value, loadRecommendations() re-fetches from the API with that
// value as a query param; the backend applies it in the WHERE clause before
// returning rows. For multi-value or cleared filters on these columns the
// filter is still applied client-side so the UX stays consistent.
// 'account' drives account_ids which is already a server-side hint; include
// it here so clearing the account filter triggers a re-fetch like the others.
export const SERVER_PUSHDOWN_COLUMNS: ReadonlySet<state.RecommendationsColumnId> = new Set([
  'service', 'region', 'account',
]);

// commitAndRefresh is called by every categorical popover commit. For
// server-pushdown columns it fires loadRecommendations() (re-fetch) so the
// backend can prune rows before they hit the wire; for all other columns it
// calls rerenderRecommendations() (client-side filter, no extra API call).
//
// The re-fetch path is intentionally async/void: the popover commit returns
// immediately and the table skeleton renders while the fetch is in flight,
// matching the existing provider/account-change flow.
function commitAndRefresh(column: state.RecommendationsColumnId): void {
  if (SERVER_PUSHDOWN_COLUMNS.has(column)) {
    void loadRecommendations();
  } else {
    rerenderRecommendations();
  }
}

// Build the popover DOM for a given column. Categorical: checkbox list with
// (All) tri-state + Clear footer. Numeric: free-text expression input with
// inline error and Clear footer.
//
// `recs` should already be pre-filtered to the cross-column narrowed set
// (all filters except this column's own applied). `alwaysInclude` pins any
// currently-active values for this column so the user can still deselect
// them even when the cross-filtered set would otherwise omit them.
export function buildPopoverContent(
  column: state.RecommendationsColumnId,
  recs: readonly LocalRecommendation[],
  alwaysInclude?: ReadonlySet<string>,
): { el: HTMLDivElement; checkboxes: Map<string, HTMLInputElement>; input: HTMLInputElement | null; errorEl: HTMLElement | null } {
  const popover = document.createElement('div');
  popover.className = 'column-filter-popover';
  popover.setAttribute('role', 'dialog');
  popover.setAttribute('aria-modal', 'false');

  const headingId = `column-filter-heading-${column}`;
  popover.setAttribute('aria-labelledby', headingId);

  const heading = document.createElement('h3');
  heading.id = headingId;
  heading.className = 'column-filter-heading';
  heading.textContent = `Filter ${columnLabel(column)}`;
  popover.appendChild(heading);

  const checkboxes = new Map<string, HTMLInputElement>();
  let input: HTMLInputElement | null = null;
  let errorEl: HTMLElement | null = null;
  // commitAllRef is set inside the categorical else-branch so the Clear
  // button handler (which lives outside that branch) can call commitAll()
  // and thereby invoke updateAllTriState() — fixing issue #700.
  let commitAllRef: ((target: boolean) => void) | null = null;

  if (NUMERIC_COLUMNS.has(column)) {
    const label = document.createElement('label');
    label.className = 'column-filter-numeric-label';
    label.textContent = 'Expression';
    input = document.createElement('input');
    input.type = 'text';
    input.className = 'column-filter-numeric-input';
    input.placeholder = 'e.g. >100, 50..200, 5';
    input.setAttribute('aria-describedby', `column-filter-error-${column}`);
    label.appendChild(input);
    popover.appendChild(label);

    errorEl = document.createElement('div');
    errorEl.id = `column-filter-error-${column}`;
    errorEl.className = 'column-filter-error';
    errorEl.setAttribute('role', 'status');
    popover.appendChild(errorEl);

    const commit = (): void => {
      const expr = input!.value.trim();
      if (expr === '') {
        state.setRecommendationsColumnFilter(column, null);
        saveColumnFilters(state.getRecommendationsColumnFilters());
        errorEl!.textContent = '';
        rerenderRecommendations();
        return;
      }
      const parsed = parseNumericFilter(expr);
      if (!parsed.ok) {
        errorEl!.textContent = parsed.error;
        return;
      }
      errorEl!.textContent = '';
      state.setRecommendationsColumnFilter(column, { kind: 'expr', expr });
      saveColumnFilters(state.getRecommendationsColumnFilters());
      rerenderRecommendations();
    };
    input.addEventListener('blur', commit);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        commit();
      }
    });
  } else {
    const distinct = distinctValuesForColumn(recs, column, alwaysInclude);

    // (All) tri-state checkbox at the top.
    const allLabel = document.createElement('label');
    allLabel.className = 'column-filter-all';
    const allBox = document.createElement('input');
    allBox.type = 'checkbox';
    allBox.dataset['role'] = 'all';
    allLabel.appendChild(allBox);
    const allText = document.createElement('span');
    allText.textContent = '(All)';
    allLabel.appendChild(allText);
    popover.appendChild(allLabel);

    // Issue #137: when the service column popover lists 2+ SP plan-type
    // slugs, render a second tri-state row "All Savings Plans" between
    // (All) and the individual list. PR #123 split the single
    // 'savings-plans' option into per-plan-type slugs, removing the
    // one-click "filter to all SP recommendations" affordance — this
    // restores it as a group-level toggle scoped to SP slugs only.
    // Per-plan-type checkboxes remain individually selectable for
    // narrowing.
    const spSlugs: string[] = column === 'service'
      ? distinct.filter((v) => isSavingsPlanService(v))
      : [];
    let spBox: HTMLInputElement | null = null;
    if (spSlugs.length >= 2) {
      const spLabel = document.createElement('label');
      spLabel.className = 'column-filter-group';
      spBox = document.createElement('input');
      spBox.type = 'checkbox';
      spBox.dataset['role'] = 'sp-group';
      spLabel.appendChild(spBox);
      const spText = document.createElement('span');
      spText.textContent = 'All Savings Plans';
      spLabel.appendChild(spText);
      popover.appendChild(spLabel);
    }

    const list = document.createElement('div');
    list.className = 'column-filter-list';
    for (const value of distinct) {
      const itemLabel = document.createElement('label');
      itemLabel.className = 'column-filter-item';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.dataset['value'] = value;
      itemLabel.appendChild(cb);
      const text = document.createElement('span');
      text.textContent = categoricalDisplayLabel(column, value);
      itemLabel.appendChild(text);
      list.appendChild(itemLabel);
      checkboxes.set(value, cb);
    }
    popover.appendChild(list);

    const updateAllTriState = (): void => {
      const total = checkboxes.size;
      let checked = 0;
      checkboxes.forEach((cb) => { if (cb.checked) checked++; });
      allBox.indeterminate = checked > 0 && checked < total;
      allBox.checked = checked === total && total > 0;
    };

    // SP-group tri-state mirrors updateAllTriState but is scoped to the
    // savings-plans-* boxes only. checked = every SP box ticked,
    // indeterminate = some-but-not-all, unchecked = none.
    const updateSPTriState = (): void => {
      if (!spBox) return;
      let checked = 0;
      for (const slug of spSlugs) {
        const cb = checkboxes.get(slug);
        if (cb && cb.checked) checked++;
      }
      spBox.indeterminate = checked > 0 && checked < spSlugs.length;
      spBox.checked = checked === spSlugs.length && spSlugs.length > 0;
    };

    // Individual-checkbox commit (issue #482):
    //   - N=size selected → null (no narrowing applied; all values pass)
    //   - 0<=N<size → {set, selected}; N=0 explicitly stores an empty
    //     allow-list so unchecking the last value reaches the same
    //     zero-row state as the (All) checkbox and the Clear button,
    //     rather than snapping back to "all checked".
    // For server-pushdown columns (service / region / account) a single-value
    // selection triggers a re-fetch so the backend prunes rows before they
    // hit the wire (issue #162). All other columns stay client-side only.
    const commit = (): void => {
      const selected: string[] = [];
      checkboxes.forEach((cb, value) => { if (cb.checked) selected.push(value); });
      if (selected.length === checkboxes.size) {
        state.setRecommendationsColumnFilter(column, null);
      } else {
        state.setRecommendationsColumnFilter(column, { kind: 'set', values: selected });
      }
      saveColumnFilters(state.getRecommendationsColumnFilters());
      updateAllTriState();
      updateSPTriState();
      commitAndRefresh(column);
    };

    // (All) and Clear use commitAll(target) directly:
    //   - target=true → no narrowing (null); resync renders all individual
    //     boxes checked. Matches issue #482's "All selects every value".
    //   - target=false → explicit empty allow-list ({set, []}), table shows
    //     0 rows. Matches issue #482's requirement that Clear/uncheck-All is
    //     distinct from no-filter.
    const commitAll = (target: boolean): void => {
      checkboxes.forEach((cb) => { cb.checked = target; });
      if (target) {
        state.setRecommendationsColumnFilter(column, null);
      } else {
        state.setRecommendationsColumnFilter(column, { kind: 'set', values: [] });
      }
      saveColumnFilters(state.getRecommendationsColumnFilters());
      updateAllTriState();
      updateSPTriState();
      commitAndRefresh(column);
    };
    // Expose commitAll to the Clear button handler outside this else-branch.
    commitAllRef = commitAll;

    checkboxes.forEach((cb) => {
      cb.addEventListener('change', commit);
    });
    allBox.addEventListener('change', () => {
      // Browser resolves indeterminate to checked on click, so allBox.checked
      // after the click is the desired target.
      commitAll(allBox.checked);
    });
    if (spBox) {
      spBox.addEventListener('change', () => {
        // Clicking SP tri-state ticks ALL SP boxes when transitioning
        // from unchecked/indeterminate → checked, and unticks all SP
        // boxes when transitioning checked → unchecked. The browser
        // resolves indeterminate to checked on click, so spBox.checked
        // after the click is the desired target state for SP boxes.
        const target = spBox!.checked;
        for (const slug of spSlugs) {
          const cb = checkboxes.get(slug);
          if (cb) cb.checked = target;
        }
        commit();
      });
    }
  }

  // Footer with Clear button.
  const footer = document.createElement('div');
  footer.className = 'column-filter-footer';
  const clearBtn = document.createElement('button');
  clearBtn.type = 'button';
  clearBtn.className = 'column-filter-clear';
  // Categorical: selects nothing (hides every row), so not "Clear" (#727).
  clearBtn.textContent = input ? 'Clear' : 'Select none';
  clearBtn.addEventListener('click', () => {
    if (input) {
      // Numeric column: Clear drops the expression entirely (no filter).
      state.setRecommendationsColumnFilter(column, null);
      saveColumnFilters(state.getRecommendationsColumnFilters());
      input.value = '';
      if (errorEl) errorEl.textContent = '';
      rerenderRecommendations();
    } else {
      // Issue #482: Clear on a categorical filter sets an explicit empty
      // allow-list rather than null, so it's distinguishable from "no
      // filter applied" (which renders as all-checked). The popover's
      // checkboxes flip unchecked; the table renders 0 rows.
      // Issue #700: call commitAllRef(false) (the same as commitAll(false)
      // inside the categorical branch) so updateAllTriState() is invoked and
      // the (All) checkbox reflects the cleared state. commitAllRef also
      // calls commitAndRefresh(column) internally, which re-fetches for
      // server-pushdown columns (issue #162) or re-renders for the others.
      commitAllRef?.(false);
    }
  });
  footer.appendChild(clearBtn);
  popover.appendChild(footer);

  return { el: popover, checkboxes, input, errorEl };
}
