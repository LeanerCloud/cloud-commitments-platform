import * as api from '../api';
import * as state from '../state';
import { filterByEnabledProviders } from '../lib/enabled-providers';
import type { Payment as CompatPayment } from '../lib/purchase-compatibility';
import type { RecommendationsResponse, LocalRecommendation } from '../types';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { renderErrorCallout } from '../lib/error-callout';
import { resetExpandedCells, recsState } from './recsState';
import { visibleColumns } from './columnPrefs';
import { renderRecommendationsList } from './renderList';
import { triggerAutoRefreshIfStale } from './refresh';
import { renderLookbackToolbar } from './lookback';

/**
 * Inject resolved GlobalConfig defaults into the module cache.
 * Exported for testing only — not part of the public API.
 */
export function seedGlobalDefaults(term: 1 | 3, payment: CompatPayment): void {
  recsState.cachedGlobalDefaultTerm = term;
  recsState.cachedGlobalDefaultPayment = payment;
}

/**
 * Reset the cached GlobalConfig (issue #909). Exported for testing only —
 * not part of the public API. Call in beforeEach so lookback-selector tests
 * start without a stale config from a prior test.
 */
export function resetCachedGlobalConfig(): void {
  recsState.cachedGlobalConfig = null;
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
export function writeSortToUrl(sort: state.RecommendationsSort): void {
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
