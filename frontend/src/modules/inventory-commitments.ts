import * as api from '../api';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { renderErrorCallout } from '../lib/error-callout';
import * as state from '../state';
import { renderActiveCommitmentsTable } from './inventory-commitments-table';

// ──────────────────────────────────────────────
// Active commitments
// ──────────────────────────────────────────────

const ACTIVE_COMMITMENTS_LIST_ID = 'active-commitments-list';
const ACTIVE_COMMITMENTS_REFRESH_BTN_ID = 'active-commitments-refresh-btn';
const ACTIVE_COMMITMENTS_COLS = 11;

/**
 * Fetch and render the active-commitments table. Replaces #active-commitments-list
 * children with a shimmer skeleton on entry, then either the rendered
 * table or an empty-state / error paragraph on completion. Idempotent —
 * safe to call on every sub-tab switch and on every refresh click.
 *
 * Reads the current provider/account chips from state so that changing a
 * chip while on this sub-tab re-fetches with the new scope (issue #866).
 */
// Request generations: a chip change re-invokes the loaders, and a slow older
// response must not overwrite the newer scope's render (same pattern as
// populateAccountOptions in topbar-filters.ts).
let activeCommitmentsGen = 0;

export async function loadActiveCommitments(): Promise<void> {
  const container = document.getElementById(ACTIVE_COMMITMENTS_LIST_ID);
  if (!container) return;

  wireRefreshButton();
  wireAmortizeSubscription();

  const provider = state.getCurrentProvider();
  const accountIDs = state.getCurrentAccountIDs();
  // account chip is single-select; forward only when exactly one is active.
  const accountID = accountIDs.length === 1 ? accountIDs[0] : undefined;

  // 5 rows × 11 cols matches the rendered table shape (see
  // renderActiveCommitmentsTable). The renderer wipes the container's
  // children for a clean handoff from the skeleton.
  showSkeletonRows(container, 5, ACTIVE_COMMITMENTS_COLS);

  const gen = ++activeCommitmentsGen;
  try {
    const commitments = await api.listActiveCommitments({ provider: provider || undefined, accountID });
    if (gen !== activeCommitmentsGen) return;
    // Cache for amortize-toggle re-renders (issue #1112).
    lastCommitments = commitments;
    lastCommitmentsProvider = provider || undefined;
    lastCommitmentsAccountID = accountID;
    renderActiveCommitmentsTable(container, commitments, provider, accountID);
  } catch (error) {
    if (gen !== activeCommitmentsGen) return;
    teardownSkeleton(container);
    const err = error as Error;
    renderErrorCallout(container, {
      message: `Failed to load active commitments: ${err.message}`,
      onRetry: () => void loadActiveCommitments(),
    });
  }
}

function wireRefreshButton(): void {
  // Idempotency is tracked on the element itself rather than a
  // module-level flag — when the section is re-rendered (e.g. between
  // tests, or after a hot-swap in dev), the new button is unwired and
  // a stale flag would block the rebind. The dataset marker travels
  // with the element so it can't drift out of sync.
  const btn = document.getElementById(ACTIVE_COMMITMENTS_REFRESH_BTN_ID);
  if (!btn) return;
  if (btn.dataset['wired'] === '1') return;
  btn.addEventListener('click', () => {
    void loadActiveCommitments();
  });
  btn.dataset['wired'] = '1';
}

// Cache of the last-fetched commitments so the amortize toggle can
// re-render without a round-trip to the API (issue #1112). Also caches
// the provider/accountID context so the empty-state message stays accurate.
let lastCommitments: api.InventoryCommitment[] | null = null;
let lastCommitmentsProvider: string | undefined;
let lastCommitmentsAccountID: string | undefined;

// Wired once; tracks whether the amortize subscriber has been registered
// for this module so repeated loadInventory() calls don't stack listeners.
let amortizeUnsubscribe: (() => void) | null = null;

function wireAmortizeSubscription(): void {
  if (amortizeUnsubscribe) return; // already wired
  amortizeUnsubscribe = state.subscribeAmortizeUpfront(() => {
    const container = document.getElementById(ACTIVE_COMMITMENTS_LIST_ID);
    if (!container || lastCommitments === null) return;
    renderActiveCommitmentsTable(
      container,
      lastCommitments,
      lastCommitmentsProvider,
      lastCommitmentsAccountID,
    );
  });
}
