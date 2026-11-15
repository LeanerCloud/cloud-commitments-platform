import * as api from '../api';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { renderErrorCallout } from '../lib/error-callout';
import {
  coverageTargetsFromConfig,
} from '../lib/coverage-bar';
import * as state from '../state';
import { renderCoverageBreakdown } from './inventory-coverage-table';

let coverageGen = 0;

// ──────────────────────────────────────────────
// Coverage breakdown
// ──────────────────────────────────────────────

const COVERAGE_CONTAINER_ID = 'coverage-providers';
const COVERAGE_REFRESH_BTN_ID = 'coverage-refresh-btn';

/**
 * Fetch and render per-provider coverage breakdowns into #coverage-providers.
 * Shows a skeleton on entry, then either the rendered sections or an error.
 * Idempotent — safe to call on every sub-tab switch and on every refresh click.
 *
 * Reads the current provider/account chips from state so that changing a
 * chip while on this sub-tab re-fetches with the new scope (issue #866).
 */
export async function loadCoverageBreakdown(): Promise<void> {
  const container = document.getElementById(COVERAGE_CONTAINER_ID);
  if (!container) return;

  wireCoverageRefreshButton();

  const provider = state.getCurrentProvider();
  const accountIDs = state.getCurrentAccountIDs();
  const accountID = accountIDs.length === 1 ? accountIDs[0] : undefined;

  // One skeleton row per known provider while loading.
  showSkeletonRows(container, 3, 1);

  const gen = ++coverageGen;
  try {
    // The config supplies each service's coverage target for the bars
    // (issue #726). It is optional: a failed read only drops the target tick.
    const [data, cfg] = await Promise.all([
      api.getCoverageBreakdown({ provider: provider || undefined, accountID }),
      Promise.resolve().then(() => api.getConfig()).catch(() => null),
    ]);
    if (gen !== coverageGen) return;
    const targets = coverageTargetsFromConfig(cfg);
    renderCoverageBreakdown(container, data.providers, targets, provider || undefined, accountID);
  } catch (error) {
    if (gen !== coverageGen) return;
    teardownSkeleton(container);
    const err = error as Error;
    renderErrorCallout(container, {
      message: `Failed to load coverage data: ${err.message}`,
      onRetry: () => void loadCoverageBreakdown(),
    });
  }
}

function wireCoverageRefreshButton(): void {
  const btn = document.getElementById(COVERAGE_REFRESH_BTN_ID);
  if (!btn) return;
  if (btn.dataset['wired'] === '1') return;
  btn.addEventListener('click', () => {
    void loadCoverageBreakdown();
  });
  btn.dataset['wired'] = '1';
}
