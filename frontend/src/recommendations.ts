/**
 * Recommendations module for CUDly
 */

import * as api from './api';
import * as state from './state';
import { showToast } from './toast';
import { createIcon } from './icons';
import { canAccess } from './permissions';

import { recsState } from './recsflow/recsState';
import { loadRecommendations } from './recsflow/load';
import { startRecommendationsRefresh } from './recsflow/refresh';
// Re-export the shared primitives so existing consumers that import from
// recommendations.ts keep working without import-path churn (issue #166).
export { parseNumericFilter } from './lib/column-filters';
export type { ParsedNumericFilter } from './lib/column-filters';
export { getPurchaseModalSession, getPurchaseModalRecommendations, clearPurchaseModalRecommendations, resetExpandedCells } from './recsflow/recsState';
export { getAccountName } from './recsflow/shared';
export { effectiveMonthlySavings, effectiveSavingsPct, displaySavingsPct, weightedDisplaySavingsPct, onDemandMonthly, scaleCost, formatCostForPeriod, periodSuffix } from './recsflow/money';
export { cellSummary, pageLevelRange, formatSavingsRange } from './recsflow/ranges';
export type { CellRangeSummary, PageLevelRange } from './recsflow/ranges';
export { COLUMN_DEFS } from './recsflow/columnDefs';
export type { ColumnDef } from './recsflow/columnDefs';
export { TOGGLEABLE_COLUMNS, DEFAULT_HIDDEN_COLUMNS, loadColumnVisibility, saveColumnVisibility, loadColumnFilters, saveColumnFilters, resetColumnFiltersState, resetColumnVisibilityState } from './recsflow/columnPrefs';
export { applyColumnFilters, formatCapacity, displayPrecision } from './recsflow/cellValues';
export { groupRecsByCell, spGroupKey, groupSpCellKeys, pickBestVariantPerCell, scaleRecForCapacity } from './recsflow/groups';
export { fanOutBucketAccountLabel, getFanOutBuckets, clearFanOutBuckets, getExecuteMode, clearExecuteMode } from './recsflow/fanOutState';
export type { FanOutBucket } from './recsflow/fanOutState';
export { retainFanOutBuckets } from './recsflow/fanOutSummary';
export { openPurchaseModal } from './recsflow/purchaseModal';
export { renderUsageSparkline } from './recsflow/cells';
export { seedGlobalDefaults, resetCachedGlobalConfig, loadRecommendations } from './recsflow/load';
export { STALE_THRESHOLD_MS, resetAutoRefreshInFlight, describeSkippedRecommendations, describeExpiredPricingHolds, triggerAutoRefreshIfStale, startRecommendationsRefresh, refreshRecommendations } from './recsflow/refresh';

// Issue #909: the AWS Cost Explorer LookbackPeriodInDays enum. The only
// values the backend accepts; mirrors the Admin > Settings dropdown
// (#setting-recs-lookback-days) and config.Validate() in the Go layer.
const LOOKBACK_OPTIONS = [7, 30, 60] as const;
const DEFAULT_LOOKBACK_DAYS = 7;

// Issue #909: tooltip text for the Opportunities lookback control. The
// provider scope was verified against the recommendation collectors on
// origin/feat/multicloud-web-frontend: only the AWS collector
// (scheduler.fetchAndConvert -> RecommendationParams.LookbackPeriod)
// consumes recommendations_lookback_days. The Azure and GCP collectors
// call GetAllRecommendations() with no lookback parameter, so the window
// does not affect their recommendations. Keep this wording aligned with
// the Admin help text in index.html (#purchasing-recommendations-lookback).
const LOOKBACK_TOOLTIP =
  'Applies to AWS recommendations only (Cost Explorer lookback window); ' +
  'Azure and GCP are unaffected.';
// Issue #191: always-visible scope hint next to the lookback select.
export const LOOKBACK_SCOPE_HINT = 'Applies to AWS rows only';
const LOOKBACK_NO_PERMISSION_TOOLTIP =
  'Changing the lookback window requires admin (update:config) permission. ' +
  'Ask an administrator to adjust it in Settings.';

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

/**
 * Resolve the currently-effective lookback window (days) from the cached
 * GlobalConfig, falling back to the backend default when absent or not one
 * of the accepted enum values. Keeping the validation here means the
 * selector never renders a value the backend would reject.
 */
function currentLookbackDays(): number {
  const raw = recsState.cachedGlobalConfig?.recommendations_lookback_days;
  return typeof raw === 'number' && (LOOKBACK_OPTIONS as readonly number[]).includes(raw)
    ? raw
    : DEFAULT_LOOKBACK_DAYS;
}

/**
 * Issue #909: render the AWS lookback selector + scope tooltip into the
 * Opportunities toolbar (#recommendations-toolbar). Prefilled from the
 * cached config and gated by the update:config permission (the same
 * permission the backend enforces on the config PUT). Non-admin sessions
 * see the control disabled with an explanatory tooltip rather than an
 * editable control whose only outcome would be a 403.
 *
 * The container's innerHTML is rebuilt on every call (initial load and
 * after each successful change), so the previous <select> and its change
 * listener are discarded — no listener stacking (the build-DOM-fresh
 * analogue of removing the old listener before re-adding).
 */
export function renderLookbackToolbar(): void {
  const container = document.getElementById('recommendations-toolbar');
  if (!container) return;

  // Rebuild from scratch so the prior control + listener are dropped.
  container.replaceChildren();

  const canEdit = canAccess('update', 'config');

  const wrap = document.createElement('div');
  wrap.className = 'lookback-control';

  const label = document.createElement('label');
  label.setAttribute('for', 'recs-lookback-days');
  label.textContent = 'AWS lookback';

  const select = document.createElement('select');
  select.id = 'recs-lookback-days';
  select.disabled = !canEdit;
  // Native title gives a hover hint on the disabled control too (disabled
  // selects don't always relay pointer events to a wrapping tooltip span).
  select.title = canEdit ? LOOKBACK_TOOLTIP : LOOKBACK_NO_PERMISSION_TOOLTIP;

  const selected = currentLookbackDays();
  for (const days of LOOKBACK_OPTIONS) {
    const opt = document.createElement('option');
    opt.value = String(days);
    opt.textContent = `${days} days`;
    if (days === selected) opt.selected = true;
    select.appendChild(opt);
  }

  if (canEdit) {
    select.addEventListener('change', () => {
      void onLookbackChange(select.value);
    });
  }

  // Info tooltip. DOM-built only (static icon, tooltip via textContent,
  // never API text) so there is no innerHTML injection surface.
  const info = document.createElement('span');
  info.className = 'info-icon';
  info.appendChild(createIcon('info'));
  const tip = document.createElement('span');
  tip.className = 'tooltip-text';
  tip.textContent = canEdit ? LOOKBACK_TOOLTIP : LOOKBACK_NO_PERMISSION_TOOLTIP;
  info.appendChild(tip);

  // Issue #191: the tooltip above is hover-only, and the control sits over a
  // multi-provider table, so it read as a global setting. A short visible
  // hint states the scope for everyone; aria-describedby announces it too.
  const scope = document.createElement('span');
  scope.id = 'recs-lookback-scope';
  scope.className = 'lookback-scope-hint';
  scope.textContent = LOOKBACK_SCOPE_HINT;
  select.setAttribute('aria-describedby', scope.id);

  wrap.append(label, select, scope, info);
  container.appendChild(wrap);
}

/**
 * Issue #909: persist a new lookback window to the global config (the same
 * endpoint Admin > Settings uses) and trigger a recommendations re-collect
 * for the new window, then reload the Opportunities list. The selector is
 * disabled while the change is in flight so rapid changes can't race; a
 * failed persist reverts the selector to the last-known value and toasts
 * the error so the user never silently keeps a stale view.
 */
async function onLookbackChange(rawValue: string): Promise<void> {
  // feedback_strict_int_parse: reject non-decimal-integer strings (e.g.
  // "0x3c", "7e0", " 7", "") before converting, then constrain to the
  // accepted enum so a tampered DOM can't push an out-of-range value.
  const previous = currentLookbackDays();
  const select = document.getElementById('recs-lookback-days') as HTMLSelectElement | null;

  if (!/^\d+$/.test(rawValue) || !Number.isInteger(Number(rawValue)) || !(LOOKBACK_OPTIONS as readonly number[]).includes(Number(rawValue))) {
    // toast renders via textContent, so no escaping is needed (escaping here
    // would double-encode and show literal entities).
    showToast({ message: `Invalid lookback window: ${rawValue}`, kind: 'error' });
    if (select) select.value = String(previous);
    return;
  }
  const parsed = Number(rawValue);
  if (parsed === previous) return;

  // The backend config PUT preserves every omitted field, so send only the
  // lookback: re-sending a stale cached config would revert newer global
  // values (#530). Block the save until the config has loaded, since the
  // selector's current value is unknown before then.
  if (!recsState.cachedGlobalConfig) {
    showToast({
      message: 'Settings are still loading. Please retry once configuration has loaded.',
      kind: 'error',
    });
    if (select) select.value = String(previous);
    return;
  }
  if (select) select.disabled = true;
  try {
    await api.updateConfig({ recommendations_lookback_days: parsed });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    console.error('Failed to persist lookback window:', err);
    showToast({ message: `Failed to update lookback window: ${message}`, kind: 'error' });
    if (select) {
      select.value = String(previous);
      select.disabled = false;
    }
    return;
  }

  // Reflect the persisted value in the cache so currentLookbackDays() and
  // the next renderLookbackToolbar() stay consistent even before reload.
  // Merge onto the current cache, not a pre-await copy: a refresh that landed
  // while the PUT was in flight must not be rolled back.
  if (recsState.cachedGlobalConfig) {
    recsState.cachedGlobalConfig = { ...recsState.cachedGlobalConfig, recommendations_lookback_days: parsed };
  }

  // Re-collect for the new window, then reload the list. loadRecommendations
  // rebuilds the toolbar (re-enabling the select) on success; on a refresh
  // failure the toast surfaces the error and we re-enable here so the
  // control isn't left stuck disabled.
  try {
    await startRecommendationsRefresh();
  } finally {
    const el = document.getElementById('recs-lookback-days') as HTMLSelectElement | null;
    if (el) el.disabled = !canAccess('update', 'config');
  }
}
