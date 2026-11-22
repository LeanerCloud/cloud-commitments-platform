import * as state from '../state';
import { isSavingsPlanService } from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { restoreFocus } from '../lib/focus-restore';
import { anchorInView } from '../lib/table-scroll';
import { saveColumnFilters } from './columnPrefs';
import { applyColumnFilters } from './cellValues';
import { type PopoverState, getColumnTriggerButton, positionPopover, SERVER_PUSHDOWN_COLUMNS, buildPopoverContent } from './popoverContent';
import { loadRecommendations, closeVisibilityPopover, renderRecommendationsList } from '../recommendations';

let openPopover: PopoverState | null = null;
// Document-level click-outside listener; attached once on first open and
// torn down on close.
let outsideClickHandler: ((e: MouseEvent) => void) | null = null;
let escKeyHandler: ((e: KeyboardEvent) => void) | null = null;
let scrollCloseHandler: ((e: Event) => void) | null = null;
let resizeHandler: (() => void) | null = null;

// Re-sync popover .checked / .value from current filter state, except when
// the active element is the numeric input (mid-typing protection).
function resyncOpenPopover(): void {
  if (!openPopover) return;
  const f = state.getRecommendationsColumnFilters()[openPopover.column];
  if (openPopover.input) {
    if (document.activeElement !== openPopover.input) {
      const expr = f && f.kind === 'expr' ? f.expr : '';
      openPopover.input.value = expr;
      if (openPopover.errorEl) openPopover.errorEl.textContent = '';
    }
    return;
  }
  // Categorical: tick checkboxes whose value is in the active filter.
  // Issue #482: when no filter is set (f == null), render every checkbox
  // as checked and the (All) box as checked. This reflects the user
  // mental model that "no narrowing applied" means "every value is
  // included", distinct from an explicit empty allow-list ({set, []})
  // which renders every box as unchecked.
  if (f == null) {
    openPopover.checkboxes.forEach((cb) => { cb.checked = true; });
  } else {
    const values: ReadonlySet<string> = f.kind === 'set' ? new Set(f.values) : new Set();
    openPopover.checkboxes.forEach((cb, value) => {
      cb.checked = values.has(value);
    });
  }
  // Update (All) tri-state.
  const allBox = openPopover.el.querySelector<HTMLInputElement>('input[data-role="all"]');
  if (allBox) {
    const total = openPopover.checkboxes.size;
    let checked = 0;
    openPopover.checkboxes.forEach((cb) => { if (cb.checked) checked++; });
    allBox.indeterminate = checked > 0 && checked < total;
    allBox.checked = checked === total && total > 0;
  }
  // Issue #137: also resync the "All Savings Plans" tri-state when the
  // service column has the SP-group affordance rendered.
  const spBox = openPopover.el.querySelector<HTMLInputElement>('input[data-role="sp-group"]');
  if (spBox) {
    let spTotal = 0;
    let spChecked = 0;
    openPopover.checkboxes.forEach((cb, value) => {
      if (isSavingsPlanService(value)) {
        spTotal++;
        if (cb.checked) spChecked++;
      }
    });
    spBox.indeterminate = spChecked > 0 && spChecked < spTotal;
    spBox.checked = spChecked === spTotal && spTotal > 0;
  }
}

function attachPopoverGlobalListeners(): void {
  if (outsideClickHandler) return;
  outsideClickHandler = (e: MouseEvent): void => {
    if (!openPopover) return;
    const target = e.target as Node | null;
    if (!target) return;
    if (openPopover.el.contains(target)) return;
    // Any column-filter trigger button click is handled by the trigger's own
    // handler; don't double-close.
    if (target instanceof Element && target.closest('.column-filter-btn')) return;
    closePopover();
  };
  escKeyHandler = (e: KeyboardEvent): void => {
    if (!openPopover) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      closePopover(true);
    }
  };
  scrollCloseHandler = (e: Event): void => {
    if (!openPopover) return;
    // Issue #483: ignore scrolls that originate inside the popover itself
    // (the outer popover scroll container or the inner column-filter-list).
    // Capture-phase scroll events from inner scrollables bubble up to the
    // window listener; without this guard, scrolling the popover's own
    // contents dismisses it before the user can reach values below the fold.
    const target = e.target as Node | null;
    if (target && openPopover.el.contains(target)) return;
    // While its header button is still on screen the popover follows it
    // (a sideways table scroll, the mirrored strip catching up a frame
    // later, a page scroll); it closes once the button scrolls out of view.
    const trigger = getColumnTriggerButton(openPopover.column);
    if (trigger && anchorInView(trigger)) {
      positionPopover(openPopover.el, trigger);
      return;
    }
    closePopover();
  };
  resizeHandler = (): void => {
    if (!openPopover) return;
    const trigger = getColumnTriggerButton(openPopover.column);
    if (!trigger) {
      closePopover();
      return;
    }
    positionPopover(openPopover.el, trigger);
  };
  document.addEventListener('mousedown', outsideClickHandler);
  document.addEventListener('keydown', escKeyHandler);
  // Use capture for scroll so we catch all scroll containers; passive for perf.
  window.addEventListener('scroll', scrollCloseHandler, { capture: true, passive: true });
  window.addEventListener('resize', resizeHandler);
}

function detachPopoverGlobalListeners(): void {
  if (outsideClickHandler) document.removeEventListener('mousedown', outsideClickHandler);
  if (escKeyHandler) document.removeEventListener('keydown', escKeyHandler);
  if (scrollCloseHandler) window.removeEventListener('scroll', scrollCloseHandler, { capture: true } as EventListenerOptions);
  if (resizeHandler) window.removeEventListener('resize', resizeHandler);
  outsideClickHandler = null;
  escKeyHandler = null;
  scrollCloseHandler = null;
  resizeHandler = null;
}

export function openColumnPopover(column: state.RecommendationsColumnId, anchor: HTMLElement): void {
  // Defensive: if the popover element is no longer in the document (jest
  // DOM reset between tests, or an external script removed it), drop the
  // stale ref before applying the toggle/swap logic below.
  if (openPopover && !openPopover.el.isConnected) {
    detachPopoverGlobalListeners();
    openPopover = null;
  }
  // Toggle: clicking same trigger closes.
  if (openPopover && openPopover.column === column) {
    closePopover(true);
    return;
  }
  if (openPopover) closePopover();

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
  const recsForDistinct = applyColumnFilters(recs, filtersExceptThisColumn);
  const ownFilter = allFilters[column];
  const alwaysInclude: ReadonlySet<string> = ownFilter && ownFilter.kind === 'set'
    ? new Set(ownFilter.values)
    : new Set();
  const built = buildPopoverContent(column, recsForDistinct, alwaysInclude);
  document.body.appendChild(built.el);
  openPopover = {
    column,
    el: built.el,
    checkboxes: built.checkboxes,
    input: built.input,
    errorEl: built.errorEl,
    triggerColumn: column,
  };
  resyncOpenPopover();
  positionPopover(built.el, anchor);

  // ARIA wiring on the trigger.
  anchor.setAttribute('aria-expanded', 'true');

  attachPopoverGlobalListeners();

  // Move focus into the popover for keyboard users.
  const firstFocusable = built.input
    ?? built.el.querySelector<HTMLInputElement>('input[type="checkbox"]');
  firstFocusable?.focus();
}

export function closePopover(restoreFocus = false): void {
  if (!openPopover) return;
  const { column, el } = openPopover;
  el.remove();
  openPopover = null;
  detachPopoverGlobalListeners();
  // ARIA cleanup on the trigger (if it still exists in the DOM).
  const trigger = getColumnTriggerButton(column);
  if (trigger) {
    trigger.setAttribute('aria-expanded', 'false');
    if (restoreFocus) trigger.focus();
  }
}

// Called by renderRecommendationsList after the table is rebuilt to re-anchor
// any open popover to the freshly-rendered trigger button. If the column was
// removed from the table somehow, close gracefully.
export function rebindOpenPopoverAnchor(): void {
  if (!openPopover) return;
  const trigger = getColumnTriggerButton(openPopover.column);
  if (!trigger) {
    closePopover();
    return;
  }
  trigger.setAttribute('aria-expanded', 'true');
  positionPopover(openPopover.el, trigger);
  resyncOpenPopover();
}

// rerenderRecommendations triggers a full re-render from the latest loaded
// state. Used by popover commits and the Clear-filters badge so the table
// reflects new column-filter state immediately.
//
// #272 (CR follow-up): the summary card is recomputed inside
// renderRecommendationsList itself (against the post-filter visible set),
// so this helper doesn't need to call renderRecommendationsSummary
// separately — every entry to renderRecommendationsList covers it.
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
    if (openPopover) {
      closePopover();
    }
    closeVisibilityPopover();
  });
  recommendationsTabObserver.observe(tab, { attributes: true, attributeFilter: ['class'] });
}
