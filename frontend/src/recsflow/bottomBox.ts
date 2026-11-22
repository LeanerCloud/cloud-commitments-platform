import * as api from '../api';
import { planSelectionLimit } from '../lib/plan-groups';
import * as state from '../state';
import { formatCurrency } from '../utils';
import type { LocalRecommendation } from '../types';
import { canAccess } from '../permissions';
import { pageLevelRange, formatSavingsRange } from './ranges';
import { loadBulkPurchaseState, saveBulkPurchaseState } from './bulkPrefs';
import { groupRecsByCell } from './groups';
import { handleBulkPurchaseClick } from './bulkPurchase';

//
// Mount-once-then-update lifecycle for the sticky bottom action box.
// mountBottomActionBox builds the DOM (input/select/button identities) and
// wires listeners exactly once. updateBottomActionBox refreshes only the
// mutable surface — button labels, .disabled, the selection-summary text —
// leaving the input/select elements (and their in-progress values) alone.
//
// IDs preserved for backward compatibility:
//   #bulk-purchase-capacity (Capacity % input — read by app.ts:307)
//   #bulk-purchase-btn      (Purchase one-off button)
//   #create-plan-btn        (Create Purchase Plan button — relocated from
//                            the old top filter bar)
//
// Issue #282: the bulk Payment dropdown (#bulk-purchase-payment) is removed.
// Each rec carries its own payment_option from the API fan-out; the per-cell
// radio enforcement caps purchase to one variant per cell. A global override
// was misleading and is redundant.
export function mountBottomActionBox(): HTMLElement | null {
  const recsTab = document.getElementById('opportunities-tab');
  if (!recsTab) return null;

  let box = document.getElementById('recommendations-action-box');
  if (box) return box;

  box = document.createElement('div');
  box.id = 'recommendations-action-box';
  box.className = 'recommendations-action-box';
  box.setAttribute('role', 'toolbar');
  box.setAttribute('aria-label', 'Recommendations actions');

  const tbState = loadBulkPurchaseState();

  // Selection summary text (e.g. "(3 selected of 19 visible)" or "(All 19 visible)")
  const summary = document.createElement('span');
  summary.id = 'recommendations-action-summary';
  summary.className = 'recommendations-action-summary';
  box.appendChild(summary);

  // Capacity % input — preserved ID (app.ts:307 reads this)
  const capacityLabel = document.createElement('label');
  capacityLabel.textContent = 'Capacity % ';
  const capacityInput = document.createElement('input');
  capacityInput.id = 'bulk-purchase-capacity';
  capacityInput.type = 'number';
  capacityInput.min = '1';
  capacityInput.max = '100';
  capacityInput.step = '1';
  capacityInput.value = String(tbState.capacity);
  capacityLabel.appendChild(capacityInput);
  box.appendChild(capacityLabel);

  // Purchase one-off (preserved ID). Issue #365: hidden for sessions
  // that lack `execute:purchases` (issue #923: Purchaser-group membership,
  // not Administrators alone). The element stays in the DOM so the click
  // handler stays wired; syncBottomActionBoxPermissions toggles `.hidden`
  // on every render. Starts hidden so the box fails closed until then.
  const purchaseBtn = document.createElement('button');
  purchaseBtn.type = 'button';
  purchaseBtn.className = 'btn btn-primary';
  purchaseBtn.id = 'bulk-purchase-btn';
  purchaseBtn.textContent = 'Purchase';
  purchaseBtn.title = 'Buy these reservations now (one-off, processed immediately)';
  purchaseBtn.hidden = true;
  box.appendChild(purchaseBtn);

  // The no-Purchaser banner (issue #923) is inserted after purchaseBtn by
  // syncBottomActionBoxPermissions, not here.

  // Create Purchase Plan (relocated from old top bar). Issue #365:
  // hidden for sessions that lack `create:plans` (readonly loses it;
  // admin + user keep it). Synced per render like the Purchase button.
  const planBtn = document.createElement('button');
  planBtn.type = 'button';
  planBtn.className = 'btn btn-secondary';
  planBtn.id = 'create-plan-btn';
  planBtn.textContent = 'Create Plan';
  planBtn.title = 'Schedule a recurring plan that will purchase these recommendations on a defined cadence';
  planBtn.hidden = true;
  box.appendChild(planBtn);

  // a11y hint for the disabled-button state (#273 CR follow-up).
  // Disabled <button> elements are non-focusable per HTML spec and
  // browsers don't reliably show their `title` tooltips, so a sibling
  // hint with aria-describedby is the discoverable channel for both
  // mouse and keyboard users. The element starts hidden; updateBottom-
  // ActionBox toggles its visibility and links the buttons via
  // aria-describedby when they're disabled.
  const disabledHint = document.createElement('span');
  disabledHint.id = 'recommendations-action-disabled-hint';
  disabledHint.className = 'recommendations-action-disabled-hint';
  disabledHint.setAttribute('role', 'status');
  disabledHint.setAttribute('aria-live', 'polite');
  disabledHint.hidden = true;
  box.appendChild(disabledHint);

  const persist = (): void => {
    saveBulkPurchaseState({
      payment: tbState.payment,
      capacity: Math.max(1, Math.min(100, parseInt(capacityInput.value, 10) || 100)),
    });
  };
  capacityInput.addEventListener('change', persist);

  purchaseBtn.addEventListener('click', () => {
    const target = resolvePurchaseTarget();
    if (target.length === 0) return;
    handleBulkPurchaseClick(target);
  });

  planBtn.addEventListener('click', () => {
    const target = resolvePurchaseTarget();
    if (target.length === 0) return;
    // Pass the resolved target through to the plan modal as a snapshot
    // (#273 CR follow-up). Without this, savePlan would re-derive the
    // target from state.getVisibleRecommendations() / getSelectedRecommendation
    // IDs() at Save time — racing Refresh, filter changes, and
    // deselections that happen while the modal is open. The Purchase
    // path already captures the target at click time via handleBulkPurchase
    // Click(target); the Plan path now mirrors that.
    void openCreatePlanFromBottomBox(target);
  });

  recsTab.appendChild(box);
  return box;
}

// Resolve the action target: selected ∩ visible. Returns an empty slice when
// no rows are selected — the action buttons are disabled in that state by
// updateBottomActionBox (closes #273), so callers should never reach this
// helper without a selection. The empty-return is defence-in-depth: if a
// caller bypasses the disabled UI (programmatic invocation, future code path,
// regression on the gating), no purchase happens.
//
// Historical context: prior to #273 this fell back to
// pickBestVariantPerCell(visible) when no rows were selected, so misclicking
// a "Purchase visible" button could trigger an irreversible bulk purchase.
// The fallback was removed because Refresh and filter changes silently
// mutate the visible set, making the no-selection path structurally unsafe.
function resolvePurchaseTarget(): LocalRecommendation[] {
  const visible = state.getVisibleRecommendations() as unknown as LocalRecommendation[];
  const selected = state.getSelectedRecommendationIDs();
  return visible.filter((r) => selected.has(r.id));
}

const NO_PURCHASER_BANNER_ID = 'recommendations-no-purchaser-banner';

// syncBottomActionBoxPermissions applies the permission-gated parts of the
// action box: Purchase and Create Plan visibility and the no-Purchaser
// banner. mountBottomActionBox builds the box once and returns early on
// every later render, so gating evaluated there was a snapshot taken on the
// first render; a permission change made in-session (e.g. an admin adding
// themselves to the Purchaser group) never showed up until a reload (#20).
// Re-evaluating here, on every render, keeps it current.
function syncBottomActionBoxPermissions(): void {
  const purchaseBtn = document.getElementById('bulk-purchase-btn');
  const planBtn = document.getElementById('create-plan-btn');
  const canPurchase = canAccess('execute', 'purchases');
  if (purchaseBtn) purchaseBtn.hidden = !canPurchase;
  if (planBtn) planBtn.hidden = !canAccess('create', 'plans');

  // Issue #923: an informational banner for sessions that can view
  // recommendations but cannot execute purchases. The predicate MUST mirror
  // the Purchase CTA's gate (canAccess('execute', 'purchases')) so a
  // custom-role user who holds execute:purchases via a non-seeded group
  // sees the live button without a contradictory "you can view but not
  // execute" notice.
  const existing = document.getElementById(NO_PURCHASER_BANNER_ID);
  if (canPurchase) {
    existing?.remove();
    return;
  }
  if (existing || !purchaseBtn) return;
  const banner = document.createElement('div');
  banner.id = NO_PURCHASER_BANNER_ID;
  banner.className = 'info-banner';
  banner.setAttribute('role', 'note');
  // Scope the message to the execute-purchases capability only (row 550):
  // Standard users CAN create plans, so the banner must not imply otherwise.
  // The nav tab is "Admin" (renamed from Settings in #340), and Admin → Users
  // is admin-only, so a non-admin can't self-serve there — only an admin can
  // add them to the Purchaser group.
  banner.textContent =
    'You can view and plan, but not execute purchases directly. ' +
    'Ask an admin to add you to the Purchaser group (Admin → Users) to execute purchases.';
  purchaseBtn.after(banner);
}

// updateBottomActionBox refreshes labels and disabled state on every
// renderRecommendationsList call without rebuilding the input/select DOM,
// preserving any in-progress typing in the Capacity input.
export function updateBottomActionBox(visibleCount: number, loadedCount: number): void {
  const box = document.getElementById('recommendations-action-box');
  if (!box) return;

  // Before the disabled-hint logic below, which reads planBtn.hidden.
  syncBottomActionBoxPermissions();

  const selected = state.getSelectedRecommendationIDs();
  // Count only selections that are currently visible.
  const visible = state.getVisibleRecommendations() as unknown as LocalRecommendation[];
  const selectedVisibleCount = visible.reduce(
    (n, r) => n + (selected.has(r.id) ? 1 : 0),
    0,
  );

  // Action-box summary line surfaces the *financial* impact of the
  // current action target, not just selection counts (closes #281). The
  // selected-vs-visible count is the least useful info at this point —
  // the user can already see selection state from row checkboxes — and
  // the action box is prime real estate for the dollar figures the user
  // is about to authorise. Source-of-truth matches the summary cards
  // above: selection ∩ visible if ≥1 selected, else the visible set.
  const target: readonly LocalRecommendation[] = selectedVisibleCount > 0
    ? visible.filter((r) => selected.has(r.id))
    : visible;
  const targetGroups = groupRecsByCell(target);
  const targetRange = pageLevelRange(targetGroups);

  const summary = document.getElementById('recommendations-action-summary');
  if (summary) {
    if (loadedCount === 0) {
      summary.textContent = '(No recommendations loaded)';
    } else if (visibleCount === 0) {
      summary.textContent = '(0 visible — adjust filters)';
    } else if (selectedVisibleCount === 0) {
      summary.textContent = '(Select rows to act on)';
    } else if (targetRange.cellCount > 0) {
      const savingsText = targetRange.savingsMax > 0
        ? formatSavingsRange(targetRange.savingsMin, targetRange.savingsMax)
        : formatCurrency(0);
      const upfrontText = targetRange.upfrontMax > 0
        ? formatSavingsRange(targetRange.upfrontMin, targetRange.upfrontMax)
        : formatCurrency(0);
      const rowWord = targetRange.cellCount === 1 ? 'row' : 'rows';
      summary.textContent = `(${savingsText}/mo · ${upfrontText} upfront across ${targetRange.cellCount} ${rowWord})`;
    } else {
      // Shouldn't happen given the gating above, but defensive.
      summary.textContent = `(${selectedVisibleCount} selected)`;
    }
  }

  const purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement | null;
  const planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement | null;
  const disabledHint = document.getElementById('recommendations-action-disabled-hint');
  const hasSelection = selectedVisibleCount > 0;
  const disabledMessage = loadedCount === 0
    ? 'No recommendations loaded'
    : visibleCount === 0
      ? 'No rows visible — adjust filters'
      : 'Select at least one row to enable';

  // Compute the selected-visible slice once; both the plan-button gating and
  // the hint span need it.
  const selectedVisible = visible.filter((r) => selected.has(r.id));
  const planLimit = planSelectionLimit(selectedVisible as unknown as api.Recommendation[]);
  const planEnabled = hasSelection && planLimit === null;

  if (purchaseBtn) {
    purchaseBtn.disabled = !hasSelection;
    purchaseBtn.textContent = hasSelection
      ? `Purchase ${selectedVisibleCount} selected`
      : 'Purchase';
    if (hasSelection) {
      purchaseBtn.title = 'Buy these reservations now (one-off, processed immediately)';
      purchaseBtn.removeAttribute('aria-describedby');
    } else {
      // Drop the title — title on disabled buttons is unreliable; the
      // sibling hint carries the message.
      purchaseBtn.removeAttribute('title');
      purchaseBtn.setAttribute('aria-describedby', 'recommendations-action-disabled-hint');
    }
  }
  if (planBtn) {
    planBtn.disabled = !planEnabled;
    planBtn.textContent = hasSelection
      ? `Plan from ${selectedVisibleCount} selected`
      : 'Create Plan';
    if (planEnabled) {
      planBtn.title = 'Schedule a recurring plan that will purchase these recommendations on a defined cadence';
      planBtn.removeAttribute('aria-describedby');
    } else {
      planBtn.removeAttribute('title');
      planBtn.setAttribute('aria-describedby', 'recommendations-action-disabled-hint');
    }
  }

  // a11y: the disabled-state explanation lives on a sibling hint span, not on
  // the buttons' `title` attribute. Disabled <button> elements are non-focusable
  // per HTML spec and don't reliably surface `title` tooltips across browsers, so
  // keyboard users would never see the hint and mouse users only sometimes would.
  // The sibling element + aria-describedby pattern works for both (#273 CR follow-up).
  // The hint also carries the size-limit explanation for the plan button: a
  // selection beyond what one plan can bind disables it (#1746).
  if (disabledHint) {
    const planBlocked = hasSelection && planBtn != null && !planBtn.hidden && planLimit !== null;
    if (!hasSelection) {
      disabledHint.hidden = false;
      disabledHint.textContent = disabledMessage;
    } else if (planBlocked) {
      disabledHint.hidden = false;
      disabledHint.textContent = planLimit;
    } else {
      disabledHint.hidden = true;
      disabledHint.textContent = '';
    }
  }
}

// openCreatePlanFromBottomBox opens the plan-creation modal. plans.ts'
// savePlan reads state.getVisibleRecommendations() (Bundle B's plumbing
// addition in Step 8c) so the plan only includes selected ∩ visible (or
// all visible if no selection).
export async function openCreatePlanFromBottomBox(snapshot: LocalRecommendation[]): Promise<void> {
  const { openCreatePlanModal } = await import('../plans');
  // Cast: api.Recommendation and LocalRecommendation share the persisted
  // wire shape; the modal stores a copy and savePlan submits it as
  // api.Recommendation[]. The snapshot was already passed through
  // resolvePurchaseTarget() / Set membership, both of which treat the
  // shape as opaque.
  openCreatePlanModal(snapshot as unknown as readonly api.Recommendation[]);
}
