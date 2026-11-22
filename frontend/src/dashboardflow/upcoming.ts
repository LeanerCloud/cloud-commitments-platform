import * as api from '../api';
import * as state from '../state';
import { formatCurrency, getDateParts, providerBadgeClass } from '../utils';
import type { UpcomingPurchase } from '../types';
import { showToast } from '../toast';
import { confirmDialog } from '../confirmDialog';
import { openModal, closeModal, createModalHeader } from '../modal';
import { canCancelScheduledExecution } from '../lib/purchase-cancel-permission';
import { serviceLabel } from '../lib/service-labels';
import { showSkeletonBlock, teardownSkeleton } from '../lib/skeleton';
import { upcomingEmptyMessage } from '../lib/upcoming-scope';
import { dashboardState } from './dashboardState';
import { loadDashboard } from './load';

// In-memory index of the currently-rendered upcoming purchases, keyed by
// execution_id. The "View Details" affordance renders from this — the
// /api/dashboard/upcoming response already carries every field the
// dialog displays, so no extra roundtrip is needed. The Cancel button
// uses the same execution_id to call DELETE /api/purchases/planned/{id}
// so cancelling removes JUST this scheduled step (the plan template
// stays in place). Earlier iterations: PR #207 keyed by plan_id which
// caused the cancel-then-delete-the-whole-plan bug; PR #213 (this one)
// enumerates real pending executions to surface execution_id properly.
let upcomingPurchasesIndex: Map<string, UpcomingPurchase> = new Map();

// canCancelUpcomingPurchase returns true when the current session is
// permitted to cancel the given upcoming purchase via the Dashboard
// widget (issues #950, #1400). UX gate only -- the backend
// authorizePlannedPurchaseCancel in internal/api/handler_purchases_cancel_planned.go
// remains the security boundary; a false-positive surfaces as a 403
// toast rather than a successful mutation.
//
// Full-scope (any row): admin (admin:*), cancel-any:purchases, or
//   update-any:purchases.
// Creator-scope (own row only): delete:purchases OR cancel-own:purchases,
//   PLUS non-null created_by_user_id matching the current user.
//   delete:purchases mirrors the original ownership gate (issue #950).
//   cancel-own:purchases enables Standard users to cancel their own
//   upcoming purchases from the Home page (issue #1400).
// Legacy / scheduler-tick rows with undefined created_by_user_id ->
//   no Cancel button for non-privileged users (out of reach without a
//   full-scope verb).
export function canCancelUpcomingPurchase(purchase: UpcomingPurchase): boolean {
  return canCancelScheduledExecution(purchase.created_by_user_id, purchase.plan_id);
}

// A failed fetch must not read as "nothing scheduled" (#713): show an inline
// error with a Retry that refetches only this widget.
export function renderUpcomingError(): void {
  const container = document.getElementById('upcoming-list');
  if (!container) return;
  teardownSkeleton(container);
  container.textContent = '';
  const msg = document.createElement('p');
  msg.className = 'error';
  msg.setAttribute('role', 'alert');
  msg.textContent = 'Failed to load upcoming purchases.';
  const retry = document.createElement('button');
  retry.type = 'button';
  retry.className = 'btn btn-secondary';
  retry.dataset['action'] = 'retry-upcoming';
  retry.textContent = 'Retry';
  retry.addEventListener('click', () => { void retryUpcomingPurchases(); });
  container.append(msg, retry);
}

async function retryUpcomingPurchases(): Promise<void> {
  const container = document.getElementById('upcoming-list');
  if (container) {
    // The Retry button is about to be replaced; keep focus in the widget.
    container.tabIndex = -1;
    container.focus();
    showSkeletonBlock(container, '100%', '6rem');
  }
  const gen = ++dashboardState.upcomingGen;
  try {
    const data = await api.getUpcomingPurchases(state.getCurrentProvider(), state.getCurrentAccountIDs());
    if (gen !== dashboardState.upcomingGen) return;
    // `?.`: apiRequest resolves null for an empty 2xx body (#304).
    renderUpcomingPurchases(data?.purchases || []);
  } catch (error) {
    if (gen !== dashboardState.upcomingGen) return;
    console.error('Failed to load upcoming purchases:', error);
    renderUpcomingError();
  }
}

export function renderUpcomingPurchases(purchases: UpcomingPurchase[]): void {
  const container = document.getElementById('upcoming-list');
  if (!container) return;

  // The endpoint already applied the topbar chips (#713).
  const provider = state.getCurrentProvider();

  // Refresh the in-memory index so viewPurchaseDetails can render its
  // dialog from local data — there is no execution row to look up yet
  // (the upcoming list shows plans whose next execution hasn't fired).
  upcomingPurchasesIndex = new Map(purchases.map(p => [p.execution_id, p]));

  container.textContent = '';
  if (purchases.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = upcomingEmptyMessage(provider);
    container.appendChild(empty);
    return;
  }

  for (const p of purchases) {
    const dateParts = getDateParts(p.scheduled_date);

    const card = document.createElement('div');
    card.className = 'upcoming-card';

    // Info block
    const info = document.createElement('div');
    info.className = 'upcoming-info';

    const dateDiv = document.createElement('div');
    dateDiv.className = 'upcoming-date';
    const dayDiv = document.createElement('div');
    dayDiv.className = 'day';
    dayDiv.textContent = String(dateParts.day);
    const monthDiv = document.createElement('div');
    monthDiv.className = 'month';
    monthDiv.textContent = dateParts.month;
    dateDiv.appendChild(dayDiv);
    dateDiv.appendChild(monthDiv);

    const details = document.createElement('div');
    details.className = 'upcoming-details';
    const h4 = document.createElement('h4');
    h4.textContent = p.plan_name;
    const descP = document.createElement('p');
    const badge = document.createElement('span');
    badge.className = 'provider-badge';
    // Use shared whitelist helper (providerBadgeClass) for consistency with
    // plans.ts and history.ts (D1 / L4 -- aligns on aws|azure|gcp set).
    const safeProvider = providerBadgeClass(p.provider);
    if (safeProvider) badge.classList.add(safeProvider);
    badge.textContent = (p.provider || '').toUpperCase();
    descP.appendChild(badge);
    descP.appendChild(document.createTextNode(` ${serviceLabel(p.service)} - Step ${p.step_number} of ${p.total_steps}`));
    details.appendChild(h4);
    details.appendChild(descP);

    info.appendChild(dateDiv);
    info.appendChild(details);

    // Savings block
    const savings = document.createElement('div');
    savings.className = 'upcoming-savings';
    const amountDiv = document.createElement('div');
    amountDiv.className = 'amount';
    // platform#609: a step with no recommendations has nothing to buy, and
    // its savings is a placeholder. Say so rather than render $0.
    const hasNoRecommendations = p.has_recommendations === false;
    amountDiv.textContent = hasNoRecommendations ? '—' : formatCurrency(p.estimated_savings);
    const labelDiv = document.createElement('div');
    labelDiv.className = 'label';
    labelDiv.textContent = hasNoRecommendations ? 'No recommendations attached' : 'Est. monthly savings';
    savings.appendChild(amountDiv);
    savings.appendChild(labelDiv);

    // Actions block
    const actions = document.createElement('div');
    actions.className = 'upcoming-actions';
    const viewBtn = document.createElement('button');
    viewBtn.dataset['action'] = 'view-purchase';
    viewBtn.dataset['id'] = String(p.execution_id);
    viewBtn.textContent = 'View Details';
    actions.appendChild(viewBtn);
    // Issue #950: Cancel routes to DELETE /api/purchases/planned/{id},
    // which the backend now gates on creator-scope ownership. Hide the
    // button when the current session is not authorised so the operator
    // doesn't get a 403 toast on click. Plans page applies the same gate
    // via canManageScheduledPurchase + canAccess('delete','purchases').
    if (canCancelUpcomingPurchase(p)) {
      const cancelBtn = document.createElement('button');
      cancelBtn.dataset['action'] = 'cancel-purchase';
      cancelBtn.dataset['id'] = String(p.execution_id);
      // Outlined destructive (issue #695): the filled red `danger` style made
      // Cancel the loudest element on every row. It still reads as
      // destructive and fills on hover; the confirm dialog guards the action.
      cancelBtn.className = 'btn-danger';
      cancelBtn.textContent = 'Cancel';
      actions.appendChild(cancelBtn);
    }

    card.appendChild(info);
    card.appendChild(savings);
    card.appendChild(actions);
    container.appendChild(card);
  }

  // Add event listeners
  container.querySelectorAll<HTMLButtonElement>('[data-action="view-purchase"]').forEach(btn => {
    btn.addEventListener('click', () => viewPurchaseDetails(btn.dataset['id'] || ''));
  });
  container.querySelectorAll<HTMLButtonElement>('[data-action="cancel-purchase"]').forEach(btn => {
    btn.addEventListener('click', () => void cancelScheduledPurchase(btn.dataset['id'] || '', btn));
  });
}

function viewPurchaseDetails(executionId: string): void {
  // The widget enumerates pending purchase_executions, so each row in the
  // upcoming list maps to a real execution row. We still render the dialog
  // from the in-memory snapshot to avoid an extra roundtrip — every field
  // the dialog displays already came down with the upcoming list.
  const purchase = upcomingPurchasesIndex.get(executionId);
  if (!purchase) {
    showToast({ message: 'Failed to load purchase details: not in current view', kind: 'error' });
    return;
  }

  // Remove any existing details modal
  document.getElementById('purchase-details-modal')?.remove();

  const modal = buildUpcomingDetailsModal(purchase, executionId);
  document.body.appendChild(modal);
  // Focus moves into the dialog, Tab is trapped, Escape closes it and focus
  // returns to View Details (#731). Every close path removes the element.
  // Initial focus goes to Close, not the destructive Cancel Purchase (#1653).
  openModal(modal, { initialFocus: '#close-purchase-details-btn', onClose: () => modal.remove() });
}

// buildUpcomingDetailsModal constructs the dialog via DOM-API, not
// innerHTML, so user-controlled fields (plan_name, service) can never be
// interpreted as HTML — closes a class of XSS regressions that the
// previous escapeHtml-+-innerHTML pattern only avoided by convention.
function buildUpcomingDetailsModal(p: UpcomingPurchase, executionId: string): HTMLDivElement {
  const modal = document.createElement('div');
  modal.id = 'purchase-details-modal';
  modal.className = 'modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'purchase-details-modal-title');

  const content = document.createElement('div');
  content.className = 'modal-content modal-wide';
  modal.appendChild(content);

  const h2 = document.createElement('h2');
  h2.id = 'purchase-details-modal-title';
  h2.textContent = 'Upcoming Purchase Details';
  content.appendChild(createModalHeader(h2, { dismissTargetId: 'close-purchase-details-btn' }));

  const section = document.createElement('div');
  section.className = 'form-section';
  content.appendChild(section);

  const sectionH3 = document.createElement('h3');
  sectionH3.textContent = 'Plan Info';
  section.appendChild(sectionH3);

  const table = document.createElement('table');
  const tbody = document.createElement('tbody');
  table.appendChild(tbody);
  section.appendChild(table);

  const addRow = (label: string, value: string): void => {
    const tr = document.createElement('tr');
    const tdLabel = document.createElement('td');
    const strong = document.createElement('strong');
    strong.textContent = label;
    tdLabel.appendChild(strong);
    const tdValue = document.createElement('td');
    tdValue.textContent = value;
    tr.appendChild(tdLabel);
    tr.appendChild(tdValue);
    tbody.appendChild(tr);
  };

  addRow('Plan name', p.plan_name);
  addRow('Plan ID', p.plan_id);
  addRow('Execution ID', executionId);
  addRow('Scheduled', p.scheduled_date);
  addRow('Provider', p.provider.toUpperCase());
  addRow('Service', p.service);
  addRow('Step', `${p.step_number} of ${p.total_steps}`);
  addRow(
    'Est. monthly savings',
    p.has_recommendations === false ? 'No recommendations attached' : formatCurrency(p.estimated_savings),
  );

  // Buttons row
  const btnRow = document.createElement('div');
  btnRow.className = 'modal-buttons';
  content.appendChild(btnRow);

  // Issue #950: gate the Cancel button on the same ownership check as
  // the card-level button above. Users land here from "View Details",
  // which is visible to everyone; only the destructive action needs the
  // creator-scope gate.
  if (canCancelUpcomingPurchase(p)) {
    const cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.id = 'cancel-purchase-detail-btn';
    // Outlined, not filled: the confirm dialog it opens carries the solid red action.
    cancelBtn.className = 'btn-danger';
    cancelBtn.textContent = 'Cancel Purchase';
    // Same confirm copy and request path as the card's Cancel button (#731).
    cancelBtn.addEventListener('click', () => {
      void cancelScheduledPurchase(executionId, cancelBtn, () => closeModal(modal));
    });
    btnRow.appendChild(cancelBtn);
  }

  const closeBtn = document.createElement('button');
  closeBtn.type = 'button';
  closeBtn.id = 'close-purchase-details-btn';
  closeBtn.textContent = 'Close';
  closeBtn.addEventListener('click', () => closeModal(modal));
  btnRow.appendChild(closeBtn);

  return modal;
}

// Execution IDs with a cancel in progress. A second click, or a click on the
// other Cancel button for the same step (card vs details dialog), must not
// open another confirm or send a second DELETE (#731).
const cancelsInFlight = new Set<string>();

async function cancelScheduledPurchase(
  executionId: string,
  trigger?: HTMLButtonElement,
  onCancelled?: () => void,
): Promise<void> {
  // Cancel just THIS scheduled instance via DELETE /api/purchases/planned/{id}
  // (the deletePlannedPurchase backend handler operates on
  // purchase_executions). The plan template stays intact — the next
  // scheduler tick re-creates the next instance for the plan. Earlier
  // iterations got this wrong by either:
  //   - sending plan_id to deletePlannedPurchase (404 because
  //     deletePlannedPurchase expects an execution_id), or
  //   - falling back to deletePlan (which deleted the WHOLE plan).
  // The widget now enumerates real pending executions and surfaces
  // execution_id, so neither workaround is needed.
  if (cancelsInFlight.has(executionId)) return;
  cancelsInFlight.add(executionId);
  try {
    const ok = await confirmDialog({
      title: 'Cancel this scheduled purchase?',
      body: 'Cancelling removes this upcoming step from the plan. The plan itself stays in place; the next scheduled step (if any) is unaffected.',
      confirmLabel: 'Cancel purchase',
      destructive: true,
    });
    if (!ok) return;

    // Disabled only after the confirm closes, so the confirm can hand focus
    // back to the button first.
    if (trigger) trigger.disabled = true;
    await api.deletePlannedPurchase(executionId);
    onCancelled?.();
    await loadDashboard();
    showToast({ message: 'Purchase cancelled successfully', kind: 'success', timeout: 5_000 });
  } catch (error) {
    console.error('Failed to cancel purchase:', error);
    showToast({ message: 'Failed to cancel purchase', kind: 'error' });
  } finally {
    cancelsInFlight.delete(executionId);
    if (trigger) trigger.disabled = false;
  }
}
