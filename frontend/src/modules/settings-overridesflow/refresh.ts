import { loadRecommendations } from '../../recommendations';

/**
 * Issue #196 — after any mutation to an `account_service_overrides` row
 * (create, edit, reset/delete), refresh the cached recommendations list so
 * the read-time filters (`enabled`, include/exclude lists) and the
 * dashboard's coverage cap reflect the new state without a manual reload.
 *
 * Errors are swallowed: the override mutation that just succeeded shouldn't
 * be reported as a failure because the secondary refresh fails. The recs
 * page re-fetches on the next nav event, so a stale list is recoverable.
 */
export async function refreshRecommendationsAfterOverrideChange(): Promise<void> {
  try {
    await loadRecommendations();
  } catch (err) {
    console.warn('Failed to refresh recommendations after override change:', err);
  }
}
