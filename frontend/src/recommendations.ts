/**
 * Recommendations module for CUDly
 */

import * as api from './api';
import { planSelectionLimit } from './lib/plan-groups';
import * as state from './state';
import { formatCurrency, escapeHtml, NO_VALUE } from './utils';
import { filterByEnabledProviders } from './lib/enabled-providers';
import { serviceLabel } from './lib/service-labels';
import { getRecommendationsFreshness, refreshRecommendations as refreshRecommendationsAPI } from './api/recommendations';
import type { RecommendationsFreshness } from './api/recommendations';
import { showToast } from './toast';
import { createIcon, iconSvg } from './icons';
import {
  savingsPlansBucketLabel,
  type Payment as CompatPayment,
} from './lib/purchase-compatibility';
import type { RecommendationsResponse, LocalRecommendation, RecommendationsSummary } from './types';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { renderErrorCallout } from './lib/error-callout';
import { renderKpiTiles } from './lib/kpi-tile';
import { captureFocus, restoreFocus } from './lib/focus-restore';
import { canAccess } from './permissions';
import { buildFirstRunEmptyState, isScopeFiltered } from './first-run-empty-state';

import { formatTermShort } from './lib/table-format';
import { expandedCells, expandedSpGroups, resetExpandedCells, recsState } from './recsflow/recsState';
import { providerDisplayName, providerBadgeClass, canActOnRecommendations } from './recsflow/shared';
import { scaleCost, formatCostForPeriod, periodSuffix, periodUnit, formatScaledRange } from './recsflow/money';
import { cellSummary, pageLevelRange, formatSavingsRange, formatPaybackRange, formatTermRange } from './recsflow/ranges';
import { type ColumnDef, COLUMN_DEFS, getColumnLabel, columnLabel } from './recsflow/columnDefs';
import { TOGGLEABLE_COLUMN_KEYS, loadColumnVisibility, loadColumnFilters, visibleColumns } from './recsflow/columnPrefs';
import { applyColumnFilters, categoricalDisplayLabel } from './recsflow/cellValues';
import { loadBulkPurchaseState, saveBulkPurchaseState } from './recsflow/bulkPrefs';
import { cellKey, groupRecsByCell, groupSpCellKeys, sortVariantsInCell, pickBestVariantPerCell } from './recsflow/groups';
import { groupsInSortOrder, defaultSortDirectionFor, sortIndicator } from './recsflow/sort';
import { handleBulkPurchaseClick } from './recsflow/bulkPurchase';
import { openColumnPopover, rebindOpenPopoverAnchor, clearAllColumnFilters, setOpportunitiesChromeHidden, ensureRecommendationsTabObserver } from './recsflow/popover';
import { renderFilterStatusBar } from './recsflow/statusBar';
import { buildVariantRowMarkup, cellIdentityLabel } from './recsflow/cells';
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

// #284 (CR follow-up): guard against concurrent stale-load refreshes. If a
// background refresh is already in flight, any subsequent stale detection
// skips kicking off a second request (and duplicate toasts).
let autoRefreshInFlight: Promise<void> | null = null;

// Runaway-loop guard: latch so the stale-on-open auto-refresh fires AT MOST
// once per page session. Without it, triggerAutoRefreshIfStale -> (stale)
// -> startRecommendationsRefresh -> onReload() (loadRecommendations /
// loadDashboard) re-enters triggerAutoRefreshIfStale, and when the backend
// collection never advances last_collected_at (e.g. it fails at a DB error,
// so freshness stays stale forever) the success path re-fires the collect
// endlessly — observed at ~4-10 POST /recommendations/refresh per second in
// production, each a paid Cost Explorer / pricing call once the collector
// reaches AWS. autoRefreshInFlight only dedups *concurrent* refreshes; it is
// cleared per cycle, so it cannot stop this sequential re-trigger. The latch
// is intentionally NOT reset on success/failure: the auto-refresh is a
// once-per-load convenience, and a hard page reload resets module state.
// Explicit user actions (lookback change, manual refresh) call
// startRecommendationsRefresh() directly and so are never gated by this latch.
let autoRefreshAttempted = false;

/**
 * Freshness budget for auto-refresh (#284). If the last successful collection
 * is older than this threshold (or there has never been one), loadRecommendations
 * fires a background refresh automatically so the user sees up-to-date data
 * without needing an explicit Refresh button.
 *
 * TODO: expose via the /api/public-info endpoint so operators can tune this
 * without a frontend redeploy.
 */
export const STALE_THRESHOLD_MS = 24 * 60 * 60 * 1000; // 24 hours

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

/**
 * Inject resolved GlobalConfig defaults into the module cache.
 * Exported for testing only — not part of the public API.
 */
export function seedGlobalDefaults(term: 1 | 3, payment: CompatPayment): void {
  recsState.cachedGlobalDefaultTerm = term;
  recsState.cachedGlobalDefaultPayment = payment;
}

/**
 * Reset the auto-refresh in-flight guard. Exported for testing only — not
 * part of the public API. Call in beforeEach so dedup tests start clean.
 */
export function resetAutoRefreshInFlight(): void {
  autoRefreshInFlight = null;
  autoRefreshAttempted = false;
}

/**
 * Reset the cached GlobalConfig (issue #909). Exported for testing only —
 * not part of the public API. Call in beforeEach so lookback-selector tests
 * start without a stale config from a prior test.
 */
export function resetCachedGlobalConfig(): void {
  recsState.cachedGlobalConfig = null;
}

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
 * Names the recommendations a provider dropped as invalid in the last
 * collection (issue #1472), or null when none were. The rest of that
 * provider's recommendations are shown normally.
 */
export function describeSkippedRecommendations(f: RecommendationsFreshness): string | null {
  const parts = (f.last_collection_partial?.providers ?? [])
    .filter((p) => (p.skipped_recommendations ?? 0) > 0)
    .map((p) => `${p.skipped_recommendations} ${p.provider.toUpperCase()}`);
  if (parts.length === 0) return null;
  return `${parts.join(', ')} recommendation(s) skipped as invalid (for example a non-USD price or an unrecognized term) in the last collection. The others are shown.`;
}

/**
 * Names the accounts whose Azure pricing hold passed the bound so their stale
 * recommendations were removed (issue #1900), or null when none were. Only the
 * count is shown; the server already scopes the ids to the caller.
 */
export function describeExpiredPricingHolds(f: RecommendationsFreshness): string | null {
  const ids = new Set<string>();
  for (const p of f.last_collection_partial?.providers ?? []) {
    for (const id of p.expired_hold_account_ids ?? []) ids.add(id);
  }
  if (ids.size === 0) return null;
  const noun = ids.size === 1 ? 'account' : 'accounts';
  return `Azure prices unavailable for over a day; stale recommendations removed for ${ids.size} ${noun}`;
}

const HOLD_BANNER_ID = 'recommendations-hold-banner';

/** Keeps the expired-hold notice on the Opportunities page after the toast fades. */
function renderExpiredHoldBanner(notice: string | null): void {
  const existing = document.getElementById(HOLD_BANNER_ID);
  const summary = document.getElementById('recommendations-summary');
  if (!notice || !summary) {
    existing?.remove();
    return;
  }
  const banner = existing ?? document.createElement('div');
  banner.id = HOLD_BANNER_ID;
  banner.className = 'warning-message';
  banner.setAttribute('role', 'status');
  banner.textContent = notice;
  if (!existing) summary.before(banner);
}

/**
 * Check freshness and fire a background refresh if the cache is stale or cold.
 * Fire-and-forget: does not block the table render that already happened.
 *
 * Three toast stages:
 *   1. In-flight: "Refreshing recommendations…" (sticky until resolved)
 *   2. Success:   "Recommendations refreshed"
 *   3. Failure:   "Recommendations refresh failed: <message>"
 *
 * If the freshness response itself carries a last_collection_error, that is
 * surfaced as an error toast regardless of whether a new refresh fires (the
 * freshness banner that previously showed this is being removed in #284).
 */
export async function triggerAutoRefreshIfStale(
  onReload: () => Promise<void> = loadRecommendations,
): Promise<void> {
  // Runaway-loop guard: the stale-on-open auto-refresh runs at most once per
  // page session. startRecommendationsRefresh's success path calls onReload()
  // (loadRecommendations / loadDashboard), which re-enters this function; when
  // the collector never advances last_collected_at the freshness stays stale
  // and we would re-fire the (paid) collect endlessly. Latch before the
  // freshness fetch so neither the GET nor the collect repeats. Explicit user
  // refreshes bypass this by calling startRecommendationsRefresh() directly.
  if (autoRefreshAttempted) return;
  autoRefreshAttempted = true;

  let freshness;
  try {
    freshness = await getRecommendationsFreshness();
  } catch (err) {
    // Network failures getting freshness are non-critical — the table is
    // already rendered with the cached data; swallow silently.
    console.error('Failed to fetch recommendations freshness:', err);
    return;
  }

  const skipNotice = describeSkippedRecommendations(freshness);
  if (skipNotice) showToast({ message: skipNotice, kind: 'warning' });
  const holdNotice = describeExpiredPricingHolds(freshness);
  if (holdNotice) showToast({ message: holdNotice, kind: 'warning' });
  renderExpiredHoldBanner(holdNotice);

  const lastCollectedMs =
    freshness.last_collected_at === null
      ? null
      : new Date(freshness.last_collected_at).getTime();

  const isStale =
    lastCollectedMs === null ||
    !Number.isFinite(lastCollectedMs) ||
    Date.now() - lastCollectedMs > STALE_THRESHOLD_MS;
  if (freshness.last_collection_error && isStale) {
    // The raw text is provider or database output; keep it for support (#748).
    console.warn('Last recommendations collection error:', freshness.last_collection_error);
    showToast({
      message: 'The last recommendations collection had errors. Refreshing may resolve it.',
      kind: 'error',
    });
  }

  if (!isStale) return;

  startRecommendationsRefresh(onReload, { quietForbidden: true });
}

// pollForCollectionToClear bounds how long startRecommendationsRefresh waits
// out a 409 (issue #106): POLL_INTERVAL_MS between checks,
// POLL_MAX_ATTEMPTS checks total, so the longest wait matches the backend's
// own detached-collection budget (5 min in internal/scheduler/scheduler.go).
const COLLECTION_POLL_INTERVAL_MS = 5_000;
const REFRESH_FAILED_MESSAGE =
  'Recommendations refresh failed: the collection reported errors. The previous recommendations are still shown.';
const COLLECTION_POLL_MAX_ATTEMPTS = 60; // 60 * 5s = 5 min

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Polls GET /api/recommendations/freshness until last_collection_started_at
 * clears (the collection someone else already started has finished), up to
 * COLLECTION_POLL_MAX_ATTEMPTS times. Returns the freshness response that
 * showed the marker cleared, or null on timeout. Network failures while polling are treated as "not yet cleared"
 * (same over-show-vs-fail philosophy as the rest of this module) rather than
 * aborting the wait early.
 */
async function pollUntilCollectionClears(): Promise<RecommendationsFreshness | null> {
  for (let attempt = 0; attempt < COLLECTION_POLL_MAX_ATTEMPTS; attempt++) {
    await delay(COLLECTION_POLL_INTERVAL_MS);
    try {
      const freshness = await getRecommendationsFreshness();
      if (!freshness.last_collection_started_at) return freshness;
    } catch (err) {
      console.error('Failed to poll recommendations freshness:', err);
    }
  }
  return null;
}

/**
 * Kick off a recommendations re-collect, surface the three-stage toast
 * (in-flight / success / failure), and reload the page-specific UI once the
 * collection finishes. Dedups against a refresh already in flight (#284) so the
 * stale-on-open path and the lookback-change path (#909) can never run
 * two concurrent collects. Returns the in-flight promise (or the existing
 * one when a refresh is already running) so callers that need to await
 * completion — e.g. re-enabling a control — can do so.
 *
 * A 409 ("collection already in progress") is NOT a failure: it means the
 * backend's cold-start marker (issue #106) or another caller already has a
 * collection running. Rather than showing a red toast for a refresh that is
 * actually happening, poll the freshness endpoint until the in-flight
 * marker clears, then reload -- same as if this call had started it.
 */
export function startRecommendationsRefresh(
  onReload: () => Promise<void> = loadRecommendations,
  opts: { quietForbidden?: boolean } = {},
): Promise<void> {
  // Dedup: if a refresh is already in flight, don't start a second one.
  if (autoRefreshInFlight) return autoRefreshInFlight;

  const inFlight = showToast({
    message: 'Refreshing recommendations…',
    kind: 'info',
    // No auto-dismiss timeout — the .then()/.catch() paths call
    // inFlight.dismiss() explicitly, so the toast stays visible until the
    // refresh actually settles (real refreshes can take 28 s+).
    timeout: null,
  });

  // The POST only starts the collection (202 in Lambda mode), and a 409 means
  // one is already running; either way the outcome is known only once the
  // in-flight marker clears (issue #1679).
  const settleWhenCollectionClears = async (): Promise<void> => {
    const cleared = await pollUntilCollectionClears();
    inFlight.dismiss();
    if (!cleared) {
      showToast({
        message: 'Recommendations refresh is taking longer than expected; try again shortly',
        kind: 'error',
      });
      return;
    }
    if (cleared.last_collection_error) {
      // last_collection_error can hold raw provider error text, so the toast
      // names the outcome only (cf. #748).
      showToast({
        message: REFRESH_FAILED_MESSAGE,
        kind: 'error',
      });
    } else {
      showToast({
        message: 'Recommendations refreshed',
        kind: 'success',
        timeout: 5_000,
      });
    }
    // Reload either way: a partially failed collect may still have persisted
    // rows. Callers pass a page-specific reload (loadDashboard from Home,
    // loadRecommendations from Opportunities).
    await onReload();
  };

  autoRefreshInFlight = refreshRecommendationsAPI()
    .then(settleWhenCollectionClears)
    .catch(async (err: unknown) => {
      const status = err !== null && typeof err === 'object' && 'status' in err
        ? (err as { status?: unknown }).status
        : undefined;
      if (status === 409) {
        await settleWhenCollectionClears();
        return;
      }
      inFlight.dismiss();
      // #619: the refresh needs unrestricted account scope, so a scoped user
      // gets a 403 for an action they never asked for. Stay quiet on the
      // automatic path only.
      if (status === 403 && opts.quietForbidden) {
        console.info('Skipped automatic recommendations refresh (not permitted):', err);
        return;
      }
      const message =
        err instanceof Error
          ? err.message
          : err !== null && err !== undefined
            ? String(err)
            : 'unknown error';
      console.error('Auto-refresh of recommendations failed:', err);
      showToast({
        message: `Recommendations refresh failed: ${message}`,
        kind: 'error',
      });
    })
    .finally(() => {
      autoRefreshInFlight = null;
    });
  return autoRefreshInFlight;
}

// Issue #481: URL <-> sort-state sync. State stays the source of truth;
// the URL is a serialised reflection so refresh / bookmark / share keep
// the user's chosen column + direction.
//
// VALID_SORT_COLUMNS gates URL params against the closed enumeration of
// column ids; invalid params are silently ignored (no toast, no crash).
const VALID_SORT_COLUMNS: ReadonlySet<state.RecommendationsColumnId> = new Set([
  'provider', 'account', 'service', 'resource_type', 'region',
  'count', 'term', 'payment', 'savings', 'upfront_cost',
  'monthly_cost', 'on_demand_monthly', 'effective_savings_pct',
]);

/**
 * If the URL carries `?sort=<col>&dir=<asc|desc>` with valid values, seed
 * state.setRecommendationsSort with the parsed pair. Idempotent: safe to
 * call on every loadRecommendations entry. Silently ignores anything
 * malformed so a stale bookmark with an old column name doesn't crash the
 * page or look broken.
 */
function readSortFromUrl(): void {
  if (typeof window === 'undefined' || !window.location) return;
  try {
    const params = new URLSearchParams(window.location.search);
    const col = params.get('sort');
    const dir = params.get('dir');
    if (!col || !dir) return;
    if (!VALID_SORT_COLUMNS.has(col as state.RecommendationsColumnId)) return;
    if (dir !== 'asc' && dir !== 'desc') return;
    state.setRecommendationsSort({
      column: col as state.RecommendationsSortColumn,
      direction: dir,
    });
  } catch {
    // URLSearchParams parsing should never throw on well-formed input, but
    // be defensive: a malformed location.search shouldn't break the page.
  }
}

/**
 * Mirror the active sort to the URL via history.replaceState so the user
 * can refresh / bookmark / share without losing it. Uses replaceState (not
 * pushState) so the back button isn't polluted by every header click.
 */
function writeSortToUrl(sort: state.RecommendationsSort): void {
  if (typeof window === 'undefined' || !window.location || !window.history) return;
  try {
    const url = new URL(window.location.href);
    url.searchParams.set('sort', sort.column);
    url.searchParams.set('dir', sort.direction);
    window.history.replaceState(window.history.state, '', url.toString());
  } catch {
    // history.replaceState can throw on file:// URLs / iframes with a
    // different origin; failing silently is the right call (sort still
    // applies in-memory, just not persisted across this refresh).
  }
}

// Request generation for loadRecommendations (#1957). A provider/account filter
// change starts a new load while an older one may still be in flight; only the
// latest call may touch caches, selection, the list or the error callout.
let recsLoadGeneration = 0;

/**
 * Load recommendations
 */
export async function loadRecommendations(): Promise<void> {
  const generation = ++recsLoadGeneration;
  // Issue #481: seed sort state from the URL before the first render so a
  // refreshed / bookmarked page renders with the user's chosen sort, not
  // the module default ("savings desc"). Idempotent on subsequent reloads.
  readSortFromUrl();

  // QA 4.13 (expand/collapse desync): clear expand state whenever the data
  // source is reloaded due to a provider/account Global filter change.
  // Column-filter and sort re-renders go through rerenderRecommendations()
  // instead, so they intentionally do NOT reach this path -- preserving the
  // "expand survives column-filter/sort" behavior documented near line 63.
  resetExpandedCells();

  // Issue #344 T3: skeleton rows for the recommendations table so the
  // panel reads as "loading" instead of staying blank while the
  // (potentially multi-second) Promise.all resolves. 5 rows ≈ above-
  // the-fold count for the typical viewport; the column count is
  // derived from the live visible-columns set + 1 (the leading checkbox
  // column the table renders), so toggling Columns ▾ keeps the
  // skeleton row shape aligned with the eventual table.
  const listEl = document.getElementById('recommendations-list');
  if (listEl) {
    const skeletonCols = 1 + visibleColumns().length;
    showSkeletonRows(listEl, 5, skeletonCols);
  }

  try {
    // Provider + account_ids are sent to the API as push-down hints so the
    // backend stays bounded for big multi-cloud tenants. Service and Region
    // single-value categorical filters are also pushed down (issue #162):
    // when the user has selected exactly one value in the service or region
    // column popover the API receives that value and the backend applies the
    // WHERE clause before returning rows, saving bandwidth on large tenants.
    // Multi-value or absent service/region filters fall through unchanged and
    // are applied client-side by applyColumnFilters below.
    const accountIDs = state.getCurrentAccountIDs();
    const columnFilters = state.getRecommendationsColumnFilters();
    const serviceFilter = columnFilters['service'];
    const regionFilter = columnFilters['region'];
    const pushedService =
      serviceFilter?.kind === 'set' && serviceFilter.values.length === 1
        ? serviceFilter.values[0]
        : undefined;
    const pushedRegion =
      regionFilter?.kind === 'set' && regionFilter.values.length === 1
        ? regionFilter.values[0]
        : undefined;
    const filters: api.RecommendationFilters = {
      provider: state.getCurrentProvider(),
      account_ids: accountIDs.length > 0 ? accountIDs : undefined,
      service: pushedService,
      region: pushedRegion,
    };

    const [data, accounts, cfgResponse] = await Promise.all([
      api.getRecommendations(filters) as unknown as RecommendationsResponse,
      api.listAccountsMinimal().catch(() => null),
      // issue #223: fetch GlobalConfig so per-cell variant selection and
      // bulk-toolbar defaults reflect the operator's configured preference.
      // Failure is siloed — a missing/unreachable config endpoint must not
      // block the recommendations load; we fall back to the module defaults.
      api.getConfig().catch(() => null),
    ]);
    if (generation !== recsLoadGeneration) return;
    // Populate the module-level GlobalConfig cache (issue #223).
    if (cfgResponse?.global) {
      const g = cfgResponse.global;
      // Issue #909: cache the full config so the lookback selector can
      // prefill and round-trip the complete config on save.
      recsState.cachedGlobalConfig = g;
      const t = g.default_term;
      if (t === 1 || t === 3) recsState.cachedGlobalDefaultTerm = t;
      const validPayments: CompatPayment[] = ['all-upfront', 'partial-upfront', 'no-upfront', 'monthly'];
      if (g.default_payment && (validPayments as string[]).includes(g.default_payment)) {
        recsState.cachedGlobalDefaultPayment = g.default_payment as CompatPayment;
      }
      // recsState.cachedGlobalDefaultPayment is now read directly by loadBulkPurchaseState()
      // (issue #282 dropped the toolbar dropdown; no longer need to seed
      // defaultBulkPurchaseState — the module-level cache is the source of truth).
    }
    recsState.accountNamesCache = new Map((accounts ?? []).map(a => [a.id, a.name]));
    recsState.lastHasAccounts = accounts ? accounts.length > 0 : null;

    // Issue #463: gate the Opportunities list by the Settings →
    // General → Enabled Providers preference. The backend currently
    // returns all collected recs irrespective of `enabled_providers`,
    // so a user who toggled Azure off in Settings still sees Azure
    // rows on Opportunities. Apply a strict client-side filter on the
    // provider field — Settings is the source of truth.
    //
    // Permissive default: if `enabled_providers` is empty/undefined
    // (older configs or a tenant that never touched the toggles),
    // fall through with no filter rather than surfacing a blank
    // Opportunities page. The Settings load path renders the same
    // permissive default on the checkboxes (line ~2510).
    const enabledProviders = cfgResponse?.global?.enabled_providers;
    const rawRecs = (data.recommendations || []) as unknown as api.Recommendation[];
    // Shared with Home's savings card (#180) so both screens sum the same rows.
    const visibleByPreference = filterByEnabledProviders(rawRecs, enabledProviders);

    state.setRecommendations(visibleByPreference);
    state.clearSelectedRecommendations();
    recsState.loadedServerFilters = [
      ...(pushedService !== undefined ? [{ column: 'service' as const, value: pushedService }] : []),
      ...(pushedRegion !== undefined ? [{ column: 'region' as const, value: pushedRegion }] : []),
    ];

    // Cache the API-derived summary so renderRecommendationsList can
    // recompute the savings card from the visible (post-filter) set on
    // every rerender — covers initial load, sort-header clicks, column-
    // filter commits, and the Clear-filters badge. Otherwise an active
    // filter would shrink the banner range under the table while leaving
    // the card pinned to the unfiltered totals — the same divergence
    // #272 was supposed to close.
    // M-5: propagate null/absent summary rather than collapsing to {} which
    // makes all summary fields read as undefined and silently coerce to 0/--
    // in downstream display code. Callers already guard for empty/null summary.
    recsState.lastRecommendationsSummary = data.summary ?? null;
    renderRecommendationsList(visibleByPreference as unknown as LocalRecommendation[]);

    // Issue #909: render the lookback selector + scope tooltip in the
    // Opportunities toolbar, prefilled from the cached config and gated
    // by the update:config permission.
    renderLookbackToolbar();

    // Auto-refresh on page open (#284): check freshness and trigger an
    // async background refresh if the cache is cold or older than 24h.
    void triggerAutoRefreshIfStale();
  } catch (error) {
    if (generation !== recsLoadGeneration) return;
    console.error('Failed to load recommendations:', error);
    const list = document.getElementById('recommendations-list');
    if (list) {
      teardownSkeleton(list);
      const err = error as Error;
      renderErrorCallout(list, {
        message: `Failed to load recommendations: ${err.message}`,
        onRetry: () => void loadRecommendations(),
      });
    }
  }
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
function renderLookbackToolbar(): void {
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

/**
 * #180: the savings card's scope line, or null when the card covers every
 * recommendation (the same scope as Home). "Filtered" is decided by whether a
 * column filter is active or the fetch itself was narrowed server-side, not by
 * comparing counts: a pushed-down service/region filter returns only matching
 * rows, so the visible and loaded counts are equal. In that case the
 * unfiltered total isn't known, so the line names the server-side filter
 * instead of an "of Y" figure.
 */
function savingsScopeText(visibleCount: number, loadedCount: number): string | null {
  const active = Object.keys(state.getRecommendationsColumnFilters());
  if (recsState.loadedServerFilters.length > 0) {
    const pushed = recsState.loadedServerFilters.map((f) => `${columnLabel(f.column)}: ${categoricalDisplayLabel(f.column, f.value)}`);
    const others = active.filter((col) => !recsState.loadedServerFilters.some((f) => f.column === col)).length;
    if (others > 0) pushed.push(`+${others} more ${others === 1 ? 'filter' : 'filters'}`);
    return `Filtered: ${visibleCount} recommendations (${pushed.join(', ')})`;
  }
  if (active.length === 0 && visibleCount >= loadedCount) return null;
  return `Filtered: ${visibleCount} of ${loadedCount} recommendations`;
}

function renderRecommendationsSummary(
  _summary: RecommendationsSummary | null,
  recommendations: readonly LocalRecommendation[],
  loadedCount: number = recommendations.length,
): void {
  const container = document.getElementById('recommendations-summary');
  if (!container) return;

  // All four summary cards (Total Recommendations, Potential Monthly
  // Savings, Total Upfront Cost, Payback Period) recompute client-side
  // from the same source on every list rerender (closes #279). The API-
  // derived `summary` is no longer read — it summed every (term, payment)
  // variant of every cell and overstated achievable totals by ~6× on a
  // typical fan-out, the same bug class #272 closed for the savings card
  // alone. Now extended to the full header.
  //
  // Selection narrowing: when the user has ticked ≥1 checkbox visible in
  // the table, the cards narrow to `selected ∩ visible`. Cleared selection
  // → cards snap back to the full visible set. The cards therefore reflect
  // exactly what a Purchase / Plan click would commit to.
  //
  // The unused `_summary` arg is retained so the call sites in
  // loadRecommendations / renderRecommendationsList don't have to change
  // shape; it can be removed when the field is also dropped from
  // RecommendationsSummary.
  const selected = state.getSelectedRecommendationIDs();
  // Narrow selection to visible rows only — if the user has ticked rows that
  // are currently hidden by a column filter, those rows must not drive the
  // summary cards (the cards should reflect what the user can actually see
  // and act on). An empty visible-intersection means no effective selection.
  const selectedVisible = recommendations.filter((r) => selected.has(r.id));
  const target: readonly LocalRecommendation[] = selectedVisible.length > 0
    ? selectedVisible
    : recommendations;
  const groups = groupRecsByCell(target);
  const plr = pageLevelRange(groups);
  const isSelectionView = selectedVisible.length > 0 && plr.cellCount > 0;

  // issue #319: scale savings by the active cost period.
  // M-1: use ?? null so absent savings propagate to NO_VALUE via formatCostForPeriod
  // rather than silently collapsing to $0 (scaleCost returns null only when input
  // is null/undefined; a genuine $0 saving returns 0, not null).
  const period = state.getCostPeriod();
  const scaledSavingsMin = scaleCost(plr.savingsMin, period) ?? null;
  const scaledSavingsMax = scaleCost(plr.savingsMax, period) ?? null;
  // Use target.length (variant count) for the guard conditions so they
  // stay consistent with the new KPI value below (closes #748).
  const hasRecs = target.length > 0;
  const savingsText = hasRecs && plr.savingsMax > 0 && scaledSavingsMin !== null && scaledSavingsMax !== null
    ? formatScaledRange(scaledSavingsMin, scaledSavingsMax, period)
    : formatCostForPeriod(0, period);
  const upfrontText = hasRecs && plr.upfrontMax > 0
    ? formatSavingsRange(plr.upfrontMin, plr.upfrontMax)
    : formatCurrency(0);
  // L-2: use NO_VALUE for absent/inapplicable payback rather than '0 months'
  // which falsely implies instantaneous payback.
  const paybackText = hasRecs && plr.paybackMonthsMax > 0
    ? formatPaybackRange(plr.paybackMonthsMin, plr.paybackMonthsMax)
    : NO_VALUE;
  // issue #748: count VARIANTS (= target.length), not cells (= plr.cellCount).
  // "Showing X of X" in the filter-status bar also counts variants, so both
  // numbers now agree on the same dataset.
  const variantCount = target.length;
  const countLabel = isSelectionView ? 'Selected Recommendations' : 'Total Recommendations';
  const savingsLabel = (() => {
    if (period === 'monthly') {
      return isSelectionView ? 'Selected Monthly Savings' : 'Potential Monthly Savings';
    }
    const sfx = periodSuffix(period);
    return isSelectionView ? `Selected Savings ${sfx}` : `Potential Savings ${sfx}`;
  })();
  // #180: Home's Potential Monthly Savings always covers every
  // recommendation, so when column filters narrow this page the card must say
  // so; otherwise the two same-named KPIs disagree with no visible reason.
  const scopeText = isSelectionView ? null : savingsScopeText(variantCount, loadedCount);
  const upfrontLabel = isSelectionView ? 'Selected Upfront Cost' : 'Total Upfront Cost';
  const paybackLabel = 'Payback Period';

  // Issue #743: the same .kpi-tile component as Home's summary, so the two
  // pages' KPI cards match. The #180 scope line is the savings tile's detail.
  renderKpiTiles(container, [
    { kpi: 'count', title: countLabel, value: String(variantCount) },
    {
      kpi: 'savings', title: savingsLabel, value: savingsText, valueSavings: true,
      ...(scopeText ? { detail: scopeText } : {}),
    },
    { kpi: 'upfront', title: upfrontLabel, value: upfrontText },
    { kpi: 'payback', title: paybackLabel, value: paybackText },
  ]);
  const scopeDetail = container.querySelector<HTMLElement>('[data-kpi="savings"] .kpi-tile-detail');
  if (scopeDetail) scopeDetail.dataset['testid'] = 'savings-scope';
}

function buildListMarkup(
  recommendations: LocalRecommendation[],
  selectedRecs: ReadonlySet<string>,
  visibleCols: readonly ColumnDef[] = COLUMN_DEFS,
  showCheckboxes = true,
): string {
  const sort = state.getRecommendationsSort();
  const filters = state.getRecommendationsColumnFilters();
  const period = state.getCostPeriod();
  const filterBtn = (column: state.RecommendationsColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = getColumnLabel(column, period);
    const label = filters[column] ? `Filter ${lbl} \u2014 currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${label}" title="${label}">${iconSvg('filter')}</button>`;
  };
  const colHeader = (col: ColumnDef): string => {
    const lbl = getColumnLabel(col.key, period);
    // Visual-only columns (e.g. usage_history sparkline) are not sortable and
    // have no column-filter button; render a plain non-interactive <th>.
    if (col.sortable === false) {
      return `<th>${lbl}</th>`;
    }
    // Issue #755: the <th> stays a plain column header (exposing aria-sort)
    // and holds two sibling buttons, sort and filter. A focusable
    // role=button <th> wrapping the filter button made Enter on the filter
    // bubble into the sort handler, and nested the two controls.
    // Money and count columns right-align so digits line up (issue #739).
    const numClass = col.kind === 'numeric' ? ' num' : '';
    const ariaSort = sort.column === col.key
      ? ` aria-sort="${sort.direction === 'asc' ? 'ascending' : 'descending'}"`
      : '';
    // .th-inner is a nowrap flex row: when the table is squeezed the sort
    // label wraps inside its button and the funnel stays in the cell.
    return `<th class="sortable${numClass}" data-sort="${col.key}"${ariaSort}><span class="th-inner"><button type="button" class="sort-btn"><span>${lbl}</span>${sortIndicator(col.key, sort.column, sort.direction)}</button>${filterBtn(col.key)}</span></th>`;
  };

  // issues #225 + #226: group by cell, sort groups, then render.
  const groups = groupRecsByCell(recommendations);
  // Issue #768: sort order is selection-independent; selectedRecs is used only
  // for per-row rendering (checked state, CSS class) and the summary cards.
  const sortedKeys = groupsInSortOrder(groups, sort);

  // Update module-level group key cache for the Expand-All button.
  // Only multi-variant cells count — single-variant cells render flat with no chevron.
  recsState.lastVisibleGroupKeys = sortedKeys.filter((k) => (groups.get(k)?.length ?? 0) > 1);

  // The "Recommended range" banner that used to live here was redundant
  // with the Potential Monthly Savings card after #272 / #279 brought
  // the same range to the summary header. Removed (closes #278).

  // Summary row colspan: the big cell covers resource_type (always shown) + all
  // visible toggleable columns. The 3 fixed identity cells (provider, account,
  // service) always render separately, so this colspan = 1 + visible toggleable count.
  const visibleToggleableCols = visibleCols.filter((c) => TOGGLEABLE_COLUMN_KEYS.has(c.key));
  const summaryColspan = 1 + visibleToggleableCols.length;
  const visibleKeys = new Set(visibleCols.map((c) => c.key));

  // issue #135: build SP plan-type group map for visual grouping.
  // spCellGroups maps spGroupKey -> cellKey[] for scopes with 2+ SP cell keys.
  const spCellGroups = groupSpCellKeys(sortedKeys, groups);
  // Reverse index: cellKey -> spGroupKey, for O(1) lookup during row iteration.
  const cellKeyToSpGroup = new Map<string, string>();
  for (const [sgk, cellKeys] of spCellGroups) {
    for (const ck of cellKeys) cellKeyToSpGroup.set(ck, sgk);
  }
  // Track which SP groups have already had their parent row emitted.
  const renderedSpGroups = new Set<string>();

  // Build tbody rows: grouped for multi-variant cells, flat for single-variant.
  // SP groups get an additional parent row that wraps the per-plan-type cells.
  const rows: string[] = [];
  for (const key of sortedKeys) {
    const variants = groups.get(key)!;
    const sgk = cellKeyToSpGroup.get(key);

    if (sgk !== undefined) {
      // This cell key belongs to an SP group with 2+ plan types.
      if (renderedSpGroups.has(sgk)) {
        // Already rendered the parent row on a previous iteration; skip.
        continue;
      }
      renderedSpGroups.add(sgk);

      // Render the SP group parent row, then (if expanded) all child cell rows.
      const childCellKeys = spCellGroups.get(sgk)!;
      const spRep = groups.get(childCellKeys[0]!)![0]!;
      const sgAccountName = spRep.cloud_account_id
        ? (recsState.accountNamesCache.get(spRep.cloud_account_id) || spRep.cloud_account_id)
        : '\u2014';
      const isSpExpanded = expandedSpGroups.has(sgk);
      const spChevron = iconSvg(isSpExpanded ? 'chevron-down' : 'chevron-right');
      const spSlugs = childCellKeys.map((ck) => groups.get(ck)![0]!.service);
      const spLabel = savingsPlansBucketLabel(spSlugs);
      const planTypeCount = childCellKeys.length;
      // Aggregate savings: sum the best-variant savings across all child cells.
      const sfxLabel = periodUnit(period);
      let spSavingsTotal = 0;
      for (const ck of childCellKeys) {
        const cv = groups.get(ck)!;
        spSavingsTotal += Math.max(...cv.map((r) => r.savings));
      }
      // formatCostForPeriod scales internally; passing the already-scaled
      // scaledSpSavings here double-applied the period factor (issue #252).
      const spSavingsText = `${formatCostForPeriod(spSavingsTotal, period)}${sfxLabel}`;
      const sgkAttr = escapeHtml(sgk);
      rows.push(`
  <tr class="rec-sp-group-row" data-sp-group-key="${sgkAttr}">
    <td class="checkbox-col">
      <button type="button" class="rec-sp-group-chevron" data-sp-group-key="${sgkAttr}" aria-expanded="${isSpExpanded}" aria-label="${isSpExpanded ? 'Collapse' : 'Expand'} Savings Plans plan types">
        ${spChevron}
      </button>
    </td>
    <td><span class="provider-badge ${providerBadgeClass(spRep.provider)}">${escapeHtml(providerDisplayName(spRep.provider))}</span></td>
    <td>${escapeHtml(sgAccountName)}</td>
    <td><span class="service-badge rec-sp-group-badge">${escapeHtml(spLabel)} <span class="rec-sp-plan-count">+${planTypeCount} plan types</span></span></td>
    <td colspan="${summaryColspan}" class="rec-sp-group-summary">
      <span class="rec-sp-group-savings">${spSavingsText}</span>
    </td>
  </tr>`);

      if (!isSpExpanded) continue;

      // Expanded: render each child cell (possibly itself multi-variant).
      for (const ck of childCellKeys) {
        const childVariants = groups.get(ck)!;
        if (childVariants.length === 1) {
          rows.push(buildVariantRowMarkup(childVariants[0]!, selectedRecs, 'sp-child', visibleCols, showCheckboxes));
          continue;
        }

        // Multi-variant child cell inside an expanded SP group.
        const isCellExpanded = expandedCells.has(ck);
        const childSummary = cellSummary(childVariants);
        const childRep = childVariants[0]!;
        const childAccountName = childRep.cloud_account_id
          ? (recsState.accountNamesCache.get(childRep.cloud_account_id) || childRep.cloud_account_id)
          : '\u2014';
        const selectedChildVariant = childVariants.find((r) => selectedRecs.has(r.id));
        const scaledChildSavingsMin = scaleCost(childSummary.savingsMin, period) ?? childSummary.savingsMin;
        const scaledChildSavingsMax = scaleCost(childSummary.savingsMax, period) ?? childSummary.savingsMax;
        const childSavingsDisplay = selectedChildVariant
          ? `${formatCostForPeriod(selectedChildVariant.savings, period)}${sfxLabel} <span class="rec-variants-count">(+${childVariants.length - 1} variants)</span>`
          : `${formatScaledRange(scaledChildSavingsMin, scaledChildSavingsMax, period)}${sfxLabel}`;
        const childUpfrontDisplay = selectedChildVariant
          ? formatCurrency(selectedChildVariant.upfront_cost)
          : formatSavingsRange(childSummary.upfrontMin, childSummary.upfrontMax);
        const childTermDisplay = selectedChildVariant
          ? formatTermShort(selectedChildVariant.term) || NO_VALUE
          : formatTermRange(childSummary.termMin, childSummary.termMax);
        const cellChevron = iconSvg(isCellExpanded ? 'chevron-down' : 'chevron-right');
        const childIdentityParts = [cellIdentityLabel(childRep)];
        if (visibleKeys.has('region')) childIdentityParts.push(escapeHtml(childRep.region));
        childIdentityParts.push(`${childVariants.length} variants`);
        const childRangeParts: string[] = [];
        if (visibleKeys.has('savings')) childRangeParts.push(childSavingsDisplay);
        if (visibleKeys.has('upfront_cost')) childRangeParts.push(`upfront: ${childUpfrontDisplay}`);
        if (visibleKeys.has('term')) childRangeParts.push(`term: ${childTermDisplay}`);
        const ckAttr = escapeHtml(ck);
        rows.push(`
  <tr class="rec-cell-summary-row rec-sp-group-child" data-cell-key="${ckAttr}">
    <td class="checkbox-col">
      <button type="button" class="rec-cell-chevron" data-cell-key="${ckAttr}" aria-expanded="${isCellExpanded}" aria-label="${isCellExpanded ? 'Collapse' : 'Expand'} cell variants">
        ${cellChevron}
      </button>
    </td>
    <td><span class="provider-badge ${providerBadgeClass(childRep.provider)}">${escapeHtml(providerDisplayName(childRep.provider))}</span></td>
    <td>${escapeHtml(childAccountName)}</td>
    <td><span class="service-badge">${escapeHtml(serviceLabel(childRep.service))}</span></td>
    <td colspan="${summaryColspan}" class="rec-cell-summary-content">
      <div class="rec-cell-summary-inner">
        <span class="rec-cell-identity">${childIdentityParts.filter(Boolean).join(' &mdash; ')}</span>
        ${childRangeParts.length > 0 ? `<span class="rec-cell-range">${childRangeParts.join(' &middot; ')}</span>` : ''}
      </div>
    </td>
  </tr>`);
        if (isCellExpanded) {
          const sortedChildVariants = sortVariantsInCell(childVariants);
          for (const v of sortedChildVariants) {
            rows.push(buildVariantRowMarkup(v, selectedRecs, 'variant', visibleCols, showCheckboxes));
          }
        }
      }
      continue;
    }

    // Non-SP cell (or SP singleton scope -- only one plan type in scope).
    if (variants.length === 1) {
      // Single-variant: render flat, no group header, no indent.
      rows.push(buildVariantRowMarkup(variants[0]!, selectedRecs, 'flat', visibleCols, showCheckboxes));
      continue;
    }

    // Multi-variant cell (non-SP).
    const isExpanded = expandedCells.has(key);
    const summary = cellSummary(variants);
    const rep = variants[0]!;
    const accountName = rep.cloud_account_id ? (recsState.accountNamesCache.get(rep.cloud_account_id) || rep.cloud_account_id) : '\u2014';

    // Selected variant in this cell (if any) -- used to show selected values on summary row.
    const selectedVariant = variants.find((r) => selectedRecs.has(r.id));

    // Summary row savings display: selected variant value if one is selected;
    // otherwise the range across all variants.
    // issue #319: scale savings by the active period.
    const sfxLabel = periodUnit(period);
    const scaledSavingsMin = scaleCost(summary.savingsMin, period) ?? summary.savingsMin;
    const scaledSavingsMax = scaleCost(summary.savingsMax, period) ?? summary.savingsMax;
    const savingsDisplay = selectedVariant
      ? `${formatCostForPeriod(selectedVariant.savings, period)}${sfxLabel} <span class="rec-variants-count">(+${variants.length - 1} variants)</span>`
      : `${formatScaledRange(scaledSavingsMin, scaledSavingsMax, period)}${sfxLabel}`;

    const upfrontDisplay = selectedVariant
      ? formatCurrency(selectedVariant.upfront_cost)
      : formatSavingsRange(summary.upfrontMin, summary.upfrontMax);

    const termDisplay = selectedVariant
      ? formatTermShort(selectedVariant.term) || NO_VALUE
      : formatTermRange(summary.termMin, summary.termMax);

    const chevron = iconSvg(isExpanded ? 'chevron-down' : 'chevron-right');
    const identityParts = [cellIdentityLabel(rep)];
    if (visibleKeys.has('region')) {
      identityParts.push(escapeHtml(rep.region));
    }
    identityParts.push(`${variants.length} variants`);

    const rangeParts: string[] = [];
    if (visibleKeys.has('savings')) {
      rangeParts.push(savingsDisplay);
    }
    if (visibleKeys.has('upfront_cost')) {
      rangeParts.push(`upfront: ${upfrontDisplay}`);
    }
    if (visibleKeys.has('term')) {
      rangeParts.push(`term: ${termDisplay}`);
    }

    const chevronButton = `<button type="button" class="rec-cell-chevron" data-cell-key="${escapeHtml(key)}" aria-expanded="${isExpanded}" aria-label="${isExpanded ? 'Collapse' : 'Expand'} cell variants">
        ${chevron}
      </button>`;
    // Issue #1006: the expand chevron always lives in the leading td.checkbox-col,
    // before the Provider column, for all roles including readonly viewers.
    // This matches the SP-group rows (which always emitted a leading cell) and
    // the owner decision to keep the control at the table's far-left edge.
    const chevronCell = `<td class="checkbox-col">${chevronButton}</td>`;

    rows.push(`
  <tr class="rec-cell-summary-row" data-cell-key="${escapeHtml(key)}">
    ${chevronCell}
    <td><span class="provider-badge ${providerBadgeClass(rep.provider)}">${escapeHtml(providerDisplayName(rep.provider))}</span></td>
    <td>${escapeHtml(accountName)}</td>
    <td><span class="service-badge">${escapeHtml(serviceLabel(rep.service))}</span></td>
    <td colspan="${summaryColspan}" class="rec-cell-summary-content">
      <div class="rec-cell-summary-inner">
        <span class="rec-cell-identity">${identityParts.filter(Boolean).join(' &mdash; ')}</span>
        ${rangeParts.length > 0 ? `<span class="rec-cell-range">${rangeParts.join(' &middot; ')}</span>` : ''}
      </div>
    </td>
  </tr>`);

    if (isExpanded) {
      const sortedVariants = sortVariantsInCell(variants);
      for (const v of sortedVariants) {
        rows.push(buildVariantRowMarkup(v, selectedRecs, 'variant', visibleCols, showCheckboxes));
      }
    }
  }

  // Issue #479: select-all header checkbox renders as a proper tri-state
  // reflecting current selection vs. the set of best-variant-per-cell
  // recommendations (the set that selectAll's onChange actually populates,
  // see openColumnPopover wiring + #224). Indeterminate is set via JS in
  // renderRecommendationsList's post-render hook because HTML attributes
  // can't express the indeterminate state.
  // Issue #869: skip the tri-state computation entirely for viewer sessions
  // to avoid dead-code paths when showCheckboxes is false.
  // Issue #1006: the leading th.checkbox-col is always present (empty for viewers)
  // so the column aligns with the chevron cells in every row type.
  let checkboxColHeader: string;
  if (showCheckboxes) {
    const bestVariants = pickBestVariantPerCell(recommendations);
    const bestVariantIds = new Set(bestVariants.map((r) => r.id));
    let selectedBestCount = 0;
    selectedRecs.forEach((id) => { if (bestVariantIds.has(id)) selectedBestCount++; });
    const allSelected = bestVariants.length > 0 && selectedBestCount === bestVariants.length;
    const selectAllCheckedAttr = allSelected ? ' checked' : '';
    const selectAllIndeterminate = selectedBestCount > 0 && selectedBestCount < bestVariants.length;
    const selectAllDataIndeterminate = ` data-indeterminate="${selectAllIndeterminate ? 'true' : 'false'}"`;
    checkboxColHeader = `<th class="checkbox-col"><input type="checkbox" id="select-all-recs" aria-label="Select all recommendations"${selectAllCheckedAttr}${selectAllDataIndeterminate}></th>`;
  } else {
    checkboxColHeader = `<th class="checkbox-col"></th>`;
  }

  return `
    <table class="table-compact table-sticky-start">
      <thead>
        <tr>
          ${checkboxColHeader}
          ${visibleCols.map((c) => colHeader(c)).join('')}
        </tr>
      </thead>
      <tbody>
        ${rows.join('')}
      </tbody>
    </table>
  `;
}

//
// Mount-once-then-update lifecycle for the sticky bottom action box.
// mountBottomActionBox builds the DOM (input/select/button identities) and
// wires listeners exactly once. updateBottomActionBox refreshes only the
// mutable surface — button labels, .disabled, the selection-summary text —
// leaving the input/select elements (and their in-progress values) alone.
//
// IDs preserved for backward compatibility:
//   #bulk-purchase-capacity (Capacity % input — read by app.ts:307)
//   #bulk-purchase-btn      (Purchase one-off button)
//   #create-plan-btn        (Create Purchase Plan button — relocated from
//                            the old top filter bar)
//
// Issue #282: the bulk Payment dropdown (#bulk-purchase-payment) is removed.
// Each rec carries its own payment_option from the API fan-out; the per-cell
// radio enforcement caps purchase to one variant per cell. A global override
// was misleading and is redundant.
function mountBottomActionBox(): HTMLElement | null {
  const recsTab = document.getElementById('opportunities-tab');
  if (!recsTab) return null;

  let box = document.getElementById('recommendations-action-box');
  if (box) return box;

  box = document.createElement('div');
  box.id = 'recommendations-action-box';
  box.className = 'recommendations-action-box';
  box.setAttribute('role', 'toolbar');
  box.setAttribute('aria-label', 'Recommendations actions');

  const tbState = loadBulkPurchaseState();

  // Selection summary text (e.g. "(3 selected of 19 visible)" or "(All 19 visible)")
  const summary = document.createElement('span');
  summary.id = 'recommendations-action-summary';
  summary.className = 'recommendations-action-summary';
  box.appendChild(summary);

  // Capacity % input — preserved ID (app.ts:307 reads this)
  const capacityLabel = document.createElement('label');
  capacityLabel.textContent = 'Capacity % ';
  const capacityInput = document.createElement('input');
  capacityInput.id = 'bulk-purchase-capacity';
  capacityInput.type = 'number';
  capacityInput.min = '1';
  capacityInput.max = '100';
  capacityInput.step = '1';
  capacityInput.value = String(tbState.capacity);
  capacityLabel.appendChild(capacityInput);
  box.appendChild(capacityLabel);

  // Purchase one-off (preserved ID). Issue #365: hidden for sessions
  // that lack `execute:purchases` (issue #923: Purchaser-group membership,
  // not Administrators alone). The element stays in the DOM so the click
  // handler stays wired; syncBottomActionBoxPermissions toggles `.hidden`
  // on every render. Starts hidden so the box fails closed until then.
  const purchaseBtn = document.createElement('button');
  purchaseBtn.type = 'button';
  purchaseBtn.className = 'btn btn-primary';
  purchaseBtn.id = 'bulk-purchase-btn';
  purchaseBtn.textContent = 'Purchase';
  purchaseBtn.title = 'Buy these reservations now (one-off, processed immediately)';
  purchaseBtn.hidden = true;
  box.appendChild(purchaseBtn);

  // The no-Purchaser banner (issue #923) is inserted after purchaseBtn by
  // syncBottomActionBoxPermissions, not here.

  // Create Purchase Plan (relocated from old top bar). Issue #365:
  // hidden for sessions that lack `create:plans` (readonly loses it;
  // admin + user keep it). Synced per render like the Purchase button.
  const planBtn = document.createElement('button');
  planBtn.type = 'button';
  planBtn.className = 'btn btn-secondary';
  planBtn.id = 'create-plan-btn';
  planBtn.textContent = 'Create Plan';
  planBtn.title = 'Schedule a recurring plan that will purchase these recommendations on a defined cadence';
  planBtn.hidden = true;
  box.appendChild(planBtn);

  // a11y hint for the disabled-button state (#273 CR follow-up).
  // Disabled <button> elements are non-focusable per HTML spec and
  // browsers don't reliably show their `title` tooltips, so a sibling
  // hint with aria-describedby is the discoverable channel for both
  // mouse and keyboard users. The element starts hidden; updateBottom-
  // ActionBox toggles its visibility and links the buttons via
  // aria-describedby when they're disabled.
  const disabledHint = document.createElement('span');
  disabledHint.id = 'recommendations-action-disabled-hint';
  disabledHint.className = 'recommendations-action-disabled-hint';
  disabledHint.setAttribute('role', 'status');
  disabledHint.setAttribute('aria-live', 'polite');
  disabledHint.hidden = true;
  box.appendChild(disabledHint);

  const persist = (): void => {
    saveBulkPurchaseState({
      payment: tbState.payment,
      capacity: Math.max(1, Math.min(100, parseInt(capacityInput.value, 10) || 100)),
    });
  };
  capacityInput.addEventListener('change', persist);

  purchaseBtn.addEventListener('click', () => {
    const target = resolvePurchaseTarget();
    if (target.length === 0) return;
    handleBulkPurchaseClick(target);
  });

  planBtn.addEventListener('click', () => {
    const target = resolvePurchaseTarget();
    if (target.length === 0) return;
    // Pass the resolved target through to the plan modal as a snapshot
    // (#273 CR follow-up). Without this, savePlan would re-derive the
    // target from state.getVisibleRecommendations() / getSelectedRecommendation
    // IDs() at Save time — racing Refresh, filter changes, and
    // deselections that happen while the modal is open. The Purchase
    // path already captures the target at click time via handleBulkPurchase
    // Click(target); the Plan path now mirrors that.
    void openCreatePlanFromBottomBox(target);
  });

  recsTab.appendChild(box);
  return box;
}

// Resolve the action target: selected ∩ visible. Returns an empty slice when
// no rows are selected — the action buttons are disabled in that state by
// updateBottomActionBox (closes #273), so callers should never reach this
// helper without a selection. The empty-return is defence-in-depth: if a
// caller bypasses the disabled UI (programmatic invocation, future code path,
// regression on the gating), no purchase happens.
//
// Historical context: prior to #273 this fell back to
// pickBestVariantPerCell(visible) when no rows were selected, so misclicking
// a "Purchase visible" button could trigger an irreversible bulk purchase.
// The fallback was removed because Refresh and filter changes silently
// mutate the visible set, making the no-selection path structurally unsafe.
function resolvePurchaseTarget(): LocalRecommendation[] {
  const visible = state.getVisibleRecommendations() as unknown as LocalRecommendation[];
  const selected = state.getSelectedRecommendationIDs();
  return visible.filter((r) => selected.has(r.id));
}

const NO_PURCHASER_BANNER_ID = 'recommendations-no-purchaser-banner';

// syncBottomActionBoxPermissions applies the permission-gated parts of the
// action box: Purchase and Create Plan visibility and the no-Purchaser
// banner. mountBottomActionBox builds the box once and returns early on
// every later render, so gating evaluated there was a snapshot taken on the
// first render; a permission change made in-session (e.g. an admin adding
// themselves to the Purchaser group) never showed up until a reload (#20).
// Re-evaluating here, on every render, keeps it current.
function syncBottomActionBoxPermissions(): void {
  const purchaseBtn = document.getElementById('bulk-purchase-btn');
  const planBtn = document.getElementById('create-plan-btn');
  const canPurchase = canAccess('execute', 'purchases');
  if (purchaseBtn) purchaseBtn.hidden = !canPurchase;
  if (planBtn) planBtn.hidden = !canAccess('create', 'plans');

  // Issue #923: an informational banner for sessions that can view
  // recommendations but cannot execute purchases. The predicate MUST mirror
  // the Purchase CTA's gate (canAccess('execute', 'purchases')) so a
  // custom-role user who holds execute:purchases via a non-seeded group
  // sees the live button without a contradictory "you can view but not
  // execute" notice.
  const existing = document.getElementById(NO_PURCHASER_BANNER_ID);
  if (canPurchase) {
    existing?.remove();
    return;
  }
  if (existing || !purchaseBtn) return;
  const banner = document.createElement('div');
  banner.id = NO_PURCHASER_BANNER_ID;
  banner.className = 'info-banner';
  banner.setAttribute('role', 'note');
  // Scope the message to the execute-purchases capability only (row 550):
  // Standard users CAN create plans, so the banner must not imply otherwise.
  // The nav tab is "Admin" (renamed from Settings in #340), and Admin → Users
  // is admin-only, so a non-admin can't self-serve there — only an admin can
  // add them to the Purchaser group.
  banner.textContent =
    'You can view and plan, but not execute purchases directly. ' +
    'Ask an admin to add you to the Purchaser group (Admin → Users) to execute purchases.';
  purchaseBtn.after(banner);
}

// updateBottomActionBox refreshes labels and disabled state on every
// renderRecommendationsList call without rebuilding the input/select DOM,
// preserving any in-progress typing in the Capacity input.
function updateBottomActionBox(visibleCount: number, loadedCount: number): void {
  const box = document.getElementById('recommendations-action-box');
  if (!box) return;

  // Before the disabled-hint logic below, which reads planBtn.hidden.
  syncBottomActionBoxPermissions();

  const selected = state.getSelectedRecommendationIDs();
  // Count only selections that are currently visible.
  const visible = state.getVisibleRecommendations() as unknown as LocalRecommendation[];
  const selectedVisibleCount = visible.reduce(
    (n, r) => n + (selected.has(r.id) ? 1 : 0),
    0,
  );

  // Action-box summary line surfaces the *financial* impact of the
  // current action target, not just selection counts (closes #281). The
  // selected-vs-visible count is the least useful info at this point —
  // the user can already see selection state from row checkboxes — and
  // the action box is prime real estate for the dollar figures the user
  // is about to authorise. Source-of-truth matches the summary cards
  // above: selection ∩ visible if ≥1 selected, else the visible set.
  const target: readonly LocalRecommendation[] = selectedVisibleCount > 0
    ? visible.filter((r) => selected.has(r.id))
    : visible;
  const targetGroups = groupRecsByCell(target);
  const targetRange = pageLevelRange(targetGroups);

  const summary = document.getElementById('recommendations-action-summary');
  if (summary) {
    if (loadedCount === 0) {
      summary.textContent = '(No recommendations loaded)';
    } else if (visibleCount === 0) {
      summary.textContent = '(0 visible — adjust filters)';
    } else if (selectedVisibleCount === 0) {
      summary.textContent = '(Select rows to act on)';
    } else if (targetRange.cellCount > 0) {
      const savingsText = targetRange.savingsMax > 0
        ? formatSavingsRange(targetRange.savingsMin, targetRange.savingsMax)
        : formatCurrency(0);
      const upfrontText = targetRange.upfrontMax > 0
        ? formatSavingsRange(targetRange.upfrontMin, targetRange.upfrontMax)
        : formatCurrency(0);
      const rowWord = targetRange.cellCount === 1 ? 'row' : 'rows';
      summary.textContent = `(${savingsText}/mo · ${upfrontText} upfront across ${targetRange.cellCount} ${rowWord})`;
    } else {
      // Shouldn't happen given the gating above, but defensive.
      summary.textContent = `(${selectedVisibleCount} selected)`;
    }
  }

  const purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement | null;
  const planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement | null;
  const disabledHint = document.getElementById('recommendations-action-disabled-hint');
  const hasSelection = selectedVisibleCount > 0;
  const disabledMessage = loadedCount === 0
    ? 'No recommendations loaded'
    : visibleCount === 0
      ? 'No rows visible — adjust filters'
      : 'Select at least one row to enable';

  // Compute the selected-visible slice once; both the plan-button gating and
  // the hint span need it.
  const selectedVisible = visible.filter((r) => selected.has(r.id));
  const planLimit = planSelectionLimit(selectedVisible as unknown as api.Recommendation[]);
  const planEnabled = hasSelection && planLimit === null;

  if (purchaseBtn) {
    purchaseBtn.disabled = !hasSelection;
    purchaseBtn.textContent = hasSelection
      ? `Purchase ${selectedVisibleCount} selected`
      : 'Purchase';
    if (hasSelection) {
      purchaseBtn.title = 'Buy these reservations now (one-off, processed immediately)';
      purchaseBtn.removeAttribute('aria-describedby');
    } else {
      // Drop the title — title on disabled buttons is unreliable; the
      // sibling hint carries the message.
      purchaseBtn.removeAttribute('title');
      purchaseBtn.setAttribute('aria-describedby', 'recommendations-action-disabled-hint');
    }
  }
  if (planBtn) {
    planBtn.disabled = !planEnabled;
    planBtn.textContent = hasSelection
      ? `Plan from ${selectedVisibleCount} selected`
      : 'Create Plan';
    if (planEnabled) {
      planBtn.title = 'Schedule a recurring plan that will purchase these recommendations on a defined cadence';
      planBtn.removeAttribute('aria-describedby');
    } else {
      planBtn.removeAttribute('title');
      planBtn.setAttribute('aria-describedby', 'recommendations-action-disabled-hint');
    }
  }

  // a11y: the disabled-state explanation lives on a sibling hint span, not on
  // the buttons' `title` attribute. Disabled <button> elements are non-focusable
  // per HTML spec and don't reliably surface `title` tooltips across browsers, so
  // keyboard users would never see the hint and mouse users only sometimes would.
  // The sibling element + aria-describedby pattern works for both (#273 CR follow-up).
  // The hint also carries the size-limit explanation for the plan button: a
  // selection beyond what one plan can bind disables it (#1746).
  if (disabledHint) {
    const planBlocked = hasSelection && planBtn != null && !planBtn.hidden && planLimit !== null;
    if (!hasSelection) {
      disabledHint.hidden = false;
      disabledHint.textContent = disabledMessage;
    } else if (planBlocked) {
      disabledHint.hidden = false;
      disabledHint.textContent = planLimit;
    } else {
      disabledHint.hidden = true;
      disabledHint.textContent = '';
    }
  }
}

// openCreatePlanFromBottomBox opens the plan-creation modal. plans.ts'
// savePlan reads state.getVisibleRecommendations() (Bundle B's plumbing
// addition in Step 8c) so the plan only includes selected ∩ visible (or
// all visible if no selection).
async function openCreatePlanFromBottomBox(snapshot: LocalRecommendation[]): Promise<void> {
  const { openCreatePlanModal } = await import('./plans');
  // Cast: api.Recommendation and LocalRecommendation share the persisted
  // wire shape; the modal stores a copy and savePlan submits it as
  // api.Recommendation[]. The snapshot was already passed through
  // resolvePurchaseTarget() / Set membership, both of which treat the
  // shape as opaque.
  openCreatePlanModal(snapshot as unknown as readonly api.Recommendation[]);
}

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

/**
 * Refresh recommendations from API
 */
export async function refreshRecommendations(): Promise<void> {
  try {
    await api.refreshRecommendations();
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: 'Recommendation refresh started. This may take a few minutes.', kind: 'success', timeout: 8_000 });
    setTimeout(() => void loadRecommendations(), 5000);
  } catch (error) {
    console.error('Failed to refresh recommendations:', error);
    showToast({ message: 'Failed to start recommendation refresh', kind: 'error' });
  }
}
