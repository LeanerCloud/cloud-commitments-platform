import type { LocalRecommendation } from '../types';
import type { FanOutBucket } from '../recommendations';

// Mutable state shared by the recommendations modules. One object, never
// re-created; modules read and write its fields directly (no destructured
// snapshots).
export const recsState: {
  currentPurchaseRecommendations: LocalRecommendation[];
  currentPurchaseCapacityPercent: number;
  checkedPurchaseIndices: Set<number>;
  checkedPurchaseModalInitialised: boolean;
  purchaseModalOpenToken: number;
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
