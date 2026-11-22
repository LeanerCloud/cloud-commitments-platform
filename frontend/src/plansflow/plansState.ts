import type { PlannedPurchase, Recommendation } from '../api';

// Mutable state shared by the plans modules. One object, never re-created;
// modules read and write its fields directly (no destructured snapshots).
export const plansState: {
  pendingPlanRecommendations: Recommendation[];
  editingBoundPlan: boolean;
  planAccountsLoad: Promise<void>;
  planModalEpoch: number;
  planDraftTimer: ReturnType<typeof setTimeout> | null;
  planSelectedAccounts: Array<{ id: string; name: string; external_id: string }>;
  planModalSession: number;
  editPlanFailureStatus: number | undefined;
  planAutomationRead: number;
  lastLoadedPurchases: PlannedPurchase[];
} = {
  // pendingPlanRecommendations holds the resolved plan target captured at
  // "Plan from N selected" button-click time. The Plan flow used to re-derive
  // its target from state.getVisibleRecommendations() + getSelectedRecommendation
  // IDs() at savePlan time, but state mutations between modal-open and
  // modal-Save (Refresh, filter changes, deselections) could silently shrink
  // or replace the planned set. This snapshot is stamped by openCreatePlan-
  // Modal(snapshot) and consumed by savePlan; openNewPlanModal() clears it
  // (the New-Plan-from-scratch path has no pre-resolved target). See #273
  // CR follow-up.
  pendingPlanRecommendations: [],
  // editingBoundPlan is true while the modal edits a plan bound to selected
  // recommendations (#1746). Its service fields are read-only and never sent.
  editingBoundPlan: false,
  // Settles when the account chips of the most recently opened plan modal are loaded.
  planAccountsLoad: Promise.resolve(),
  // Bumped by every open, close and user edit of the plan form, so a restore can tell the user moved on.
  planModalEpoch: 0,
  planDraftTimer: null,
  // Selected accounts for the plan modal
  planSelectedAccounts: [],
  // Monotonically incrementing counter scoped to the plan modal lifecycle.
  // Incremented each time the create modal opens so that async callbacks
  // from a previous session (stale promises) can detect they're out-of-date
  // and discard their results rather than mutating state in the new session.
  planModalSession: 0,
  // HTTP status of the last failed editPlan load (undefined: none, or not an HTTP error).
  editPlanFailureStatus: undefined,
  planAutomationRead: 0,
  // Cached last-fetched planned purchases. The filter popover commits re-render
  // without re-fetching, so we hold the unfiltered set in module scope. Reset
  // on every successful loadPlannedPurchases() and consumed by
  // rerenderPlannedPurchases() (called by popover commits + Clear button).
  lastLoadedPurchases: [],
};
