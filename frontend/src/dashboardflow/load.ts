import * as api from '../api';
import * as state from '../state';
import type { DashboardSummary, LocalRecommendation } from '../types';
import { triggerAutoRefreshIfStale, startRecommendationsRefresh } from '../recommendations';
import { getRecommendationsFreshness } from '../api/recommendations';
import { buildFirstRunEmptyState, isScopeFiltered } from '../first-run-empty-state';
import { showSkeletonTiles, showSkeletonBlock, teardownSkeleton } from '../lib/skeleton';
import { buildErrorCallout } from '../lib/error-callout';
import { filterByEnabledProviders } from '../lib/enabled-providers';
import { dashboardState } from './dashboardState';
import { SAVINGS_TREND_LOADING_ID, TREND_LOAD_FAILED_TEXT, showChartSkeleton, showChartLoadFailed, buildFilterDesc } from './chartShared';
import { renderDashboardSummary } from './summary';
import { renderSavingsByService } from './byService';
import { loadSavingsTrendChart, loadYtdSparkline, setupSavingsTrendHandlers } from './trend';
import { renderUpcomingError, renderUpcomingPurchases } from './upcoming';

/**
 * True when the Home tab is the currently-visible tab. Used by the
 * reload-on-filter-change subscriptions below to skip the fetch (and
 * the resulting skeleton flash) when the user is on another tab —
 * switchTab('home') calls loadDashboard() on entry anyway.
 * Mirrors the isPurchasesTabActive() guard in modules/savings-history.ts.
 */
function isHomeTabActive(): boolean {
  return document.getElementById('home-tab')?.classList.contains('active') === true;
}

/**
 * Setup dashboard event handlers
 */
export function setupDashboardHandlers(): void {
  // Filter source-of-truth lives in state.ts (mutated by the global
  // topbar chips). Subscribe to filter changes and reload the dashboard;
  // the issue #185 ordering rule (clear accounts before refetching for a
  // new provider) is enforced by topbar-filters.ts at the source so the
  // dashboard's loadDashboard() always sees consistent state.
  //
  // Coalescing: the provider-change handler in topbar-filters.ts fires
  // BOTH the account subscriber (setCurrentAccountIDs([])) AND the
  // provider subscriber (setCurrentProvider(newProv)) synchronously.
  // Without coalescing, two loadDashboard() calls race back-to-back.
  // queueMicrotask defers the actual fetch to after the current call
  // stack clears, so the two fires collapse into one reload.
  // Active-tab guard: skip the fetch when the Home tab is not visible;
  // switchTab('home') triggers loadDashboard() on entry.
  // Mirrors the scheduleReload pattern in modules/savings-history.ts.
  let dashboardReloadQueued = false;
  const scheduleDashboardReload = (): void => {
    if (!isHomeTabActive() || dashboardReloadQueued) return;
    dashboardReloadQueued = true;
    queueMicrotask(() => {
      dashboardReloadQueued = false;
      if (isHomeTabActive()) void loadDashboard();
    });
  };
  state.subscribeProvider(scheduleDashboardReload);
  state.subscribeAccount(scheduleDashboardReload);

  setupSavingsTrendHandlers();
}

/**
 * Load dashboard data
 */
export async function loadDashboard(): Promise<void> {
  // Issue #344 T3: render skeletons synchronously before kicking off the
  // fetch so the panels show "loading" intent instead of staying blank.
  // The success render replaces children for a clean handoff; the catch
  // block calls teardownSkeleton before rendering the error.
  const summaryEl = document.getElementById('summary');
  if (summaryEl) showSkeletonTiles(summaryEl, 4);
  const upcomingEl = document.getElementById('upcoming-list');
  if (upcomingEl) showSkeletonBlock(upcomingEl, '100%', '6rem');
  // #1686: the two chart cards share the same loading treatment. The trend
  // loader clears its own; the per-service one is cleared in finally below.
  const trendLoadingEl = showChartSkeleton(SAVINGS_TREND_LOADING_ID, 'savings-trend-chart', 'savings-trend-empty', dashboardState.savingsTrendChart);
  const byServiceLoadingEl = showChartSkeleton('savings-by-service-loading', 'savings-by-service-chart', 'savings-by-service-empty', dashboardState.savingsByServiceChart);

  try {
    const currentProvider = state.getCurrentProvider();
    const currentAccountIDs = state.getCurrentAccountIDs();

    // Fetch summary, upcoming, and recommendations concurrently.
    // Recommendations are fetched here (frontend-only approach) because
    // /api/dashboard/summary still returns a flat potential_monthly_savings
    // which overcounts by summing every variant of every cell (~6x inflation).
    // The recs endpoint is Postgres-cached and cheap; a future backend PR can
    // move the range computation server-side if needed.
    // Promise.allSettled ensures the dashboard still renders if any individual
    // fetch fails -- each card falls back gracefully.
    const gen = ++dashboardState.upcomingGen;
    const [summaryResult, upcomingResult, recsResult, enabledProviders, accountsResult] = await Promise.allSettled([
      api.getDashboardSummary(currentProvider, currentAccountIDs),
      api.getUpcomingPurchases(currentProvider, currentAccountIDs),
      api.getRecommendations({ provider: currentProvider, account_ids: currentAccountIDs }),
      loadEnabledProviders(),
      // Only feeds the first-run onboarding card (issue #756); wrapped so a
      // missing or failing endpoint degrades to "unknown" instead of
      // breaking the dashboard.
      Promise.resolve().then(() => api.listAccountsMinimal()),
    ]);

    const summaryData = summaryResult.status === 'fulfilled' ? (summaryResult.value as DashboardSummary) : null;
    const upcomingData = upcomingResult.status === 'fulfilled' ? upcomingResult.value : null;
    // api.Recommendation and LocalRecommendation are structurally identical
    // except for provider: string vs provider: Provider. The provider values
    // from the API are always the union members at runtime, so this cast is safe.
    // Defensive extraction: the backend always returns the envelope shape
    //   { recommendations: [...], summary: {...}, regions: [...] }
    // so the real runtime value is never a flat array. We unwrap it here to
    // match what the Opportunities page does (cast to RecommendationsResponse
    // and read .recommendations). A flat-array result is also accepted so
    // test fixtures that resolve with a plain array continue to work.
    // #304: apiRequest resolves `null` for a 204 or content-length 0 response
    // (a malformed 2xx body rejects, #273); guard against null / unexpected
    // shapes to avoid "X is not iterable".
    const rawRecs = recsResult.status === 'fulfilled' ? recsResult.value : null;
    const recsArray = Array.isArray(rawRecs)
      ? rawRecs
      : (rawRecs != null && typeof rawRecs === 'object' && Array.isArray((rawRecs as { recommendations?: unknown }).recommendations))
        ? (rawRecs as { recommendations: unknown[] }).recommendations
        : [];
    // #180: apply the same Settings -> Enabled Providers filter as the
    // Opportunities page, so the two Potential Monthly Savings cards are
    // computed from the same rows. A failed recs fetch stays distinguishable
    // (null) so the tile can fall back to the backend count.
    const recs: readonly LocalRecommendation[] = filterByEnabledProviders(
      recsArray as unknown as LocalRecommendation[],
      enabledProviders.status === 'fulfilled' ? enabledProviders.value : null,
    );
    const recsCount = recsResult.status === 'fulfilled' ? recs.length : null;

    if (summaryResult.status === 'rejected') {
      throw summaryResult.reason as Error;
    }
    if (summaryData == null) {
      throw new Error('Dashboard summary response was empty');
    }

    // Build a human-readable filter description for filter-aware empty states
    // on the Home chart. Mirrors the pattern from loadSavingsTrendChart (#747).
    const filterDesc = buildFilterDesc(currentProvider, currentAccountIDs);

    renderDashboardSummary(summaryData, recs, recsCount);
    // `recs` is already narrowed to Enabled Providers (#180), matching the
    // Opportunities first-run check, which also runs on the preference-
    // filtered list.
    renderHomeOnboarding(
      recsResult.status === 'fulfilled' && recs.length === 0 && !summaryData.active_commitments,
      accountsResult.status === 'fulfilled' && Array.isArray(accountsResult.value)
        ? accountsResult.value.length > 0
        : null,
      isScopeFiltered(currentProvider, currentAccountIDs),
    );
    // Single merged per-service chart (#908): potential range from recs +
    // current-savings underlay from the summary's by_service map.
    renderSavingsByService(recs, summaryData.by_service || {}, filterDesc);
    if (gen !== dashboardState.upcomingGen) {
      // A newer upcoming request owns the widget.
    } else if (upcomingResult.status === 'rejected') {
      console.error('Failed to load upcoming purchases:', upcomingResult.reason);
      renderUpcomingError();
    } else {
      renderUpcomingPurchases(upcomingData?.purchases || []);
    }
    // Load the savings-over-time widget independently -- failure shouldn't
    // block the rest of the dashboard (e.g. analytics not configured).
    void loadSavingsTrendChart();
    void loadYtdSparkline();

    // Auto-refresh the recommendations cache if the last collection
    // is older than 24h (or never ran). The dashboard's KPI tiles +
    // savings card derive from recs; without this, a user who only
    // opens Home would never trigger the refresh cycle that the
    // Opportunities page used to own exclusively. The helper surfaces
    // collection errors as toasts and dedups concurrent refreshes; on
    // success it calls loadDashboard() to repaint this page with the
    // fresh data instead of forcing a recs render.
    void triggerAutoRefreshIfStale(loadDashboard);
  } catch (error) {
    console.error('Failed to load dashboard:', error);
    const summary = document.getElementById('summary');
    if (summary) {
      teardownSkeleton(summary);
      const err = error as Error;
      while (summary.firstChild) summary.removeChild(summary.firstChild);
      summary.appendChild(buildErrorCallout({
        message: `Failed to load dashboard: ${err.message}`,
        onRetry: () => void loadDashboard(),
      }));
    }
    // Clear the upcoming-list skeleton too — shimmer next to a dashboard
    // error reads as a fresh fetch in-flight, which is misleading.
    const upcoming = document.getElementById('upcoming-list');
    if (upcoming) teardownSkeleton(upcoming);
    // The trend loader never runs on this path, so clear its skeleton here.
    if (trendLoadingEl) teardownSkeleton(trendLoadingEl);
    if (!dashboardState.savingsTrendChart) showChartLoadFailed('savings-trend-empty', TREND_LOAD_FAILED_TEXT);
    if (!dashboardState.savingsByServiceChart) {
      showChartLoadFailed('savings-by-service-empty', 'Potential savings could not be loaded. Reload the page to try again.');
    }
  } finally {
    if (byServiceLoadingEl) teardownSkeleton(byServiceLoadingEl);
  }
}

/**
 * Settings -> Enabled Providers, or null when the config can't be read (a
 * missing permission or network error must not block Home; null means "no
 * filter", the same permissive fallback the Opportunities page uses).
 */
async function loadEnabledProviders(): Promise<readonly string[] | null> {
  try {
    const cfg = await api.getConfig();
    return cfg?.global?.enabled_providers ?? null;
  } catch {
    return null;
  }
}

/**
 * Issue #756: first-run onboarding card at the top of Home. Shown when no
 * recommendations loaded and nothing is committed yet, so a new user gets a
 * pointer to connecting an account (or refreshing) instead of a page of
 * "--" tiles and empty charts.
 */
function renderHomeOnboarding(show: boolean, hasAccounts: boolean | null, scopeFiltered: boolean): void {
  const host = document.getElementById('home-onboarding');
  if (!host) return;
  host.classList.toggle('hidden', !show);
  if (!show) {
    host.replaceChildren();
    return;
  }
  host.replaceChildren(buildFirstRunEmptyState({
    hasAccounts,
    scopeFiltered,
    onRefresh: () => startRecommendationsRefresh(loadDashboard),
    loadFreshness: getRecommendationsFreshness,
  }));
}
