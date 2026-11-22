import * as state from '../state';
import { loadRecommendations } from './load';

// populateRecommendationsAccountFilter / populateRegionFilter / the legacy
// service-filter helpers were removed in Bundle B — those DOM elements are
// gone from index.html. Provider / Account values now drive an API re-fetch
// via the column-header popovers (see openColumnPopover wiring). Service and
// Region single-value categorical commits also trigger a re-fetch (issue
// #162) so the backend WHERE clause prunes rows before they hit the wire;
// multi-value and numeric column filters remain purely client-side.

/**
 * True when the Opportunities tab is the currently-visible tab. The reload-
 * on-filter-change subscriptions below skip the fetch when this is false so
 * we don't burn an API call (and a skeleton flash) for a section the user
 * isn't looking at — `switchTab('opportunities')` will run loadRecommend-
 * ations() on next entry anyway.
 */
function isOpportunitiesTabActive(): boolean {
  return document.getElementById('opportunities-tab')?.classList.contains('active') === true;
}

/**
 * Setup recommendations event handlers (issue #477).
 *
 * The legacy per-section provider/account `<select>` elements were retired
 * in issue #344 in favour of the global topbar chips. Each section reloads
 * itself by subscribing to state.subscribeProvider / state.subscribeAccount;
 * Bundle B had removed the legacy listeners without adding the new
 * subscriptions, so Opportunities only re-queried on a full route-enter
 * (issue #477 repro: change filter, list doesn't update).
 *
 * Mirrors the dashboard.ts pattern — except we guard on the active-tab
 * check so the fetch only fires when the user is actually looking at
 * Opportunities. The provider-change ordering invariant (#185 — clear
 * accounts before refetching the account list) is enforced upstream in
 * topbar-filters.ts.
 *
 * Per-column header-mounted popovers (Bundle B) continue to handle the
 * in-page filter UX; their listeners are attached per-render inside
 * renderRecommendationsList.
 */
export function setupRecommendationsHandlers(): void {
  // Coalesce duplicate reloads. The topbar provider-change handler in
  // topbar-filters.ts updates BOTH state slots in sequence (clear accounts
  // then set provider, per the #185 ordering rule), which fires the
  // account-subscriber AND the provider-subscriber from a single user
  // action. Without coalescing we'd kick off two loadRecommendations()
  // calls back-to-back — extra API load plus a stale-overwrite risk if
  // the first response lands after the second.
  //
  // Microtask scheduling: both subscriber fires are synchronous within
  // the same setCurrentProvider/setCurrentAccountIDs call chain, so a
  // microtask runs once after the chain settles. setTimeout(_, 0) would
  // also work but adds a macrotask delay the user could perceive on
  // slow machines.
  let reloadQueued = false;
  const scheduleReload = (): void => {
    if (!isOpportunitiesTabActive() || reloadQueued) return;
    reloadQueued = true;
    queueMicrotask(() => {
      reloadQueued = false;
      if (isOpportunitiesTabActive()) void loadRecommendations();
    });
  };
  state.subscribeProvider(scheduleReload);
  state.subscribeAccount(scheduleReload);
}
