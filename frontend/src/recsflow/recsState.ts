import * as state from '../state';
import type { Payment as CompatPayment } from '../lib/purchase-compatibility';
import type { LocalRecommendation, RecommendationsSummary, GlobalConfig } from '../types';
import type { FanOutBucket } from './fanOutState';

// Mutable state shared by the recommendations modules. One object, never
// re-created; modules read and write its fields directly (no destructured
// snapshots).
export const recsState: {
  currentPurchaseRecommendations: LocalRecommendation[];
  currentPurchaseCapacityPercent: number;
  checkedPurchaseIndices: Set<number>;
  checkedPurchaseModalInitialised: boolean;
  purchaseModalOpenToken: number;
  accountNamesCache: Map<string, string>;
  lastHasAccounts: boolean | null;
  lastVisibleGroupKeys: string[];
  lastRecommendationsSummary: RecommendationsSummary | null;
  loadedServerFilters: ReadonlyArray<{ column: state.RecommendationsColumnId; value: string }>;
  cachedGlobalDefaultPayment: CompatPayment;
  cachedGlobalDefaultTerm: 1 | 3;
  cachedGlobalConfig: GlobalConfig | null;
  columnFiltersSeeded: boolean;
  columnVisibilitySeeded: boolean;
  currentFanOutBuckets: FanOutBucket[] | null;
  currentExecuteMode: '' | 'direct';
} = {
  // Module state for current purchase modal recommendations
  currentPurchaseRecommendations: [],
  // Capacity % the modal's recommendations were scaled at (issue #1903). Used
  // by pricedCellVariant to re-scale a sibling variant swapped in on a
  // Term/Payment change so the row keeps the same capacity as the rest of the
  // modal. Reset to 100 on modal close.
  currentPurchaseCapacityPercent: 100,
  // Tracks which row indices in currentPurchaseRecommendations the user
  // has kept included (checked). Initialised to all indices on modal open;
  // toggled by per-row checkboxes and the select-all header checkbox.
  // Cleared with currentPurchaseRecommendations on modal close.
  checkedPurchaseIndices: new Set(),
  // True once openPurchaseModal has been called and initialised the
  // checkedPurchaseIndices. Used by getPurchaseModalRecommendations to
  // distinguish "all deselected by user" from "modal never opened".
  checkedPurchaseModalInitialised: false,
  // Bumped by every openPurchaseModal/openFanOutModal call. Each opener checks
  // it after its override fetch so only the newest open renders and sets
  // submit state (issue #430).
  purchaseModalOpenToken: 0,
  // Cache of account ID → name for column display
  accountNamesCache: new Map(),
  // Whether the last account-list fetch returned any accounts; null when it
  // failed, so the first-run empty state (issue #756) does not claim "no
  // accounts connected" on a transient error.
  lastHasAccounts: null,
  // Last computed group keys for the visible filtered set — used by the
  // Expand-All button handler to populate expandedCells without re-computing.
  lastVisibleGroupKeys: [],
  // #272 (CR follow-up): cache of the most-recent API-derived summary so
  // rerenders triggered by column-filter changes can keep total_count /
  // total_upfront_cost / avg_payback_months stable while the savings card
  // itself is recomputed client-side from the *visible* recs (so it stays
  // in sync with the per-cell banner range under the table).
  lastRecommendationsSummary: null,
  // #180: the single-value column filters the last successful fetch sent to the
  // server (#162 pushdown). The loaded list is already narrowed by these, so the
  // unfiltered total is unknown and the savings card must still say "Filtered"
  // even though the visible and loaded counts match.
  loadedServerFilters: [],
  // issue #223: resolved GlobalConfig defaults, seeded on page load by
  // loadRecommendations(). Initial values mirror the historical hardcoded
  // defaults so the module is usable before the first API round-trip.
  cachedGlobalDefaultPayment: 'no-upfront',
  cachedGlobalDefaultTerm: 1,
  // Issue #909: the most-recent full GlobalConfig from /api/config, cached so
  // the Opportunities lookback selector can prefill its value. null until the
  // first successful load.
  cachedGlobalConfig: null,
  // Seed flag: set to true once column filters are loaded from localStorage
  // on first render so subsequent renders don't overwrite in-session changes.
  columnFiltersSeeded: false,
  // Seed flag: set to true once column visibility is loaded from localStorage
  // on first render so subsequent renders don't overwrite in-session toggles.
  columnVisibilitySeeded: false,
  // Fan-out modal state. app.ts's Send-for-Approval click reads these
  // via getFanOutBuckets() to fire one POST per bucket. Cleared when
  // the modal closes.
  currentFanOutBuckets: null,
  // currentExecuteMode holds the mode selected by the execute-mode toggle in
  // the purchase modal (issue #289). "direct" means the session holder has
  // execute-any/execute-own and explicitly chose to bypass the approval email.
  // "" (empty) is the default approval-required path. Cleared when the modal
  // closes. Read by app.ts handleExecutePurchase to set execute_mode in the
  // POST body.
  currentExecuteMode: '',
};

/** Identifies the current purchase-modal session; changes on every open. */
export function getPurchaseModalSession(): number {
  return recsState.purchaseModalOpenToken;
}

// issues #225 + #226: expand/collapse state for cell grouping.
// Contains the cellKey strings of cells the user has explicitly expanded.
// Cleared on every loadRecommendations() entry (page load, tab switch back
// to Opportunities, provider/account Global filter change, manual refresh,
// lookback change, stale auto-refresh — see resetExpandedCells() call at
// the top of loadRecommendations). Survives per-column filter/sort/period
// re-renders, which go through rerenderRecommendations() instead.
export const expandedCells = new Set<string>();

// issue #135: expand/collapse state for SP plan-type group rows.
// Contains the spGroupKey strings the user has explicitly expanded.
// Cleared together with expandedCells on every loadRecommendations() entry
// (see resetExpandedCells); survives column-filter/sort/period re-renders.
export const expandedSpGroups = new Set<string>();

/**
 * Reset cell and SP-group expand/collapse state. Exported for testing only.
 * Call in beforeEach to ensure tests don't share module-level state.
 */
export function resetExpandedCells(): void {
  expandedCells.clear();
  recsState.lastVisibleGroupKeys = [];
  expandedSpGroups.clear();
}

/**
 * Get the recommendations currently loaded in the purchase modal,
 * filtered to only those rows the user has kept "included" (i.e., checked).
 * Rows the user unchecked via the per-row checkbox are excluded from the
 * returned set, so the execute-purchase code path automatically honours
 * the user's per-row skip decisions without any callers needing to change.
 *
 * Returns all recs when the modal state is pre-#320 (recsState.checkedPurchaseModalInitialised
 * is false), which can happen if a caller invokes this without opening the modal
 * first (e.g. a legacy test fixture that sets recsState.currentPurchaseRecommendations
 * directly). Once the modal opens, the filter is always authoritative.
 */
export function getPurchaseModalRecommendations(): LocalRecommendation[] {
  if (!recsState.checkedPurchaseModalInitialised) {
    // Pre-open / legacy path: no checkbox state initialised — return everything.
    return [...recsState.currentPurchaseRecommendations];
  }
  return recsState.currentPurchaseRecommendations.filter((_, idx) => recsState.checkedPurchaseIndices.has(idx));
}

/**
 * Clear purchase modal recommendations and checked-indices state
 * (called when modal closes).
 */
export function clearPurchaseModalRecommendations(): void {
  recsState.currentPurchaseRecommendations = [];
  recsState.checkedPurchaseIndices = new Set();
  recsState.checkedPurchaseModalInitialised = false;
  recsState.currentPurchaseCapacityPercent = 100;
}
