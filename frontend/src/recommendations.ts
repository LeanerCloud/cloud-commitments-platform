/**
 * Recommendations module for CUDly
 */

import * as api from './api';
import * as state from './state';
import { filterByEnabledProviders } from './lib/enabled-providers';
import { getRecommendationsFreshness, refreshRecommendations as refreshRecommendationsAPI } from './api/recommendations';
import type { RecommendationsFreshness } from './api/recommendations';
import { showToast } from './toast';
import { createIcon } from './icons';
import type { Payment as CompatPayment } from './lib/purchase-compatibility';
import type { RecommendationsResponse, LocalRecommendation } from './types';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { renderErrorCallout } from './lib/error-callout';
import { captureFocus, restoreFocus } from './lib/focus-restore';
import { canAccess } from './permissions';
import { buildFirstRunEmptyState, isScopeFiltered } from './first-run-empty-state';

import { expandedCells, expandedSpGroups, resetExpandedCells, recsState } from './recsflow/recsState';
import { canActOnRecommendations } from './recsflow/shared';
import { loadColumnVisibility, loadColumnFilters, visibleColumns } from './recsflow/columnPrefs';
import { applyColumnFilters } from './recsflow/cellValues';
import { cellKey, pickBestVariantPerCell } from './recsflow/groups';
import { defaultSortDirectionFor } from './recsflow/sort';
import { openColumnPopover, rebindOpenPopoverAnchor, clearAllColumnFilters, setOpportunitiesChromeHidden, ensureRecommendationsTabObserver } from './recsflow/popover';
import { renderFilterStatusBar } from './recsflow/statusBar';
import { buildListMarkup } from './recsflow/listMarkup';
import { renderRecommendationsSummary } from './recsflow/summary';
import { mountBottomActionBox, updateBottomActionBox, openCreatePlanFromBottomBox } from './recsflow/bottomBox';
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
