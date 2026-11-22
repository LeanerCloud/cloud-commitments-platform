import type { HistoryPurchase } from '../types';

// Mutable state shared by the history modules. One object, never re-created;
// modules read and write its fields directly (no destructured snapshots).
export const historyState: {
  lastPurchases: HistoryPurchase[];
  historyMutationEpoch: number;
  lastPendingForQueue: HistoryPurchase[];
} = {
  // Cache of the last-rendered purchase list so the status-chip click handler
  // can re-render without re-fetching. Cleared on each loadHistory / viewPlanHistory.
  lastPurchases: [],
  historyMutationEpoch: 0,
  // Cache of the last pre-column-filter pending list so the popover-driven
  // re-render path can rebuild the table without re-fetching.
  lastPendingForQueue: [],
};
