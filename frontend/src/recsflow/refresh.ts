import * as api from '../api';
import { getRecommendationsFreshness, refreshRecommendations as refreshRecommendationsAPI } from '../api/recommendations';
import type { RecommendationsFreshness } from '../api/recommendations';
import { showToast } from '../toast';
import { loadRecommendations } from './load';

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

/**
 * Reset the auto-refresh in-flight guard. Exported for testing only — not
 * part of the public API. Call in beforeEach so dedup tests start clean.
 */
export function resetAutoRefreshInFlight(): void {
  autoRefreshInFlight = null;
  autoRefreshAttempted = false;
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
