import * as state from '../state';
import { formatDate, escapeHtml, escapeHtmlAttr, formatCurrency, NO_VALUE, providerBadgeHtml } from '../utils';
import { iconSvg } from '../icons';
import type { PlannedPurchase } from '../api';
import { canAccess } from '../permissions';
import { canCancelScheduledExecution } from '../lib/purchase-cancel-permission';
import { formatPaymentOption, formatTermShort } from '../lib/table-format';
import { badgeHtml, statusBadgeHtml } from '../lib/status-badge';
import { isPastScheduledDate } from '../lib/purchase-row-guards';
import { serviceLabel } from '../lib/service-labels';

// canManageScheduledPurchase returns true when the current session is
// permitted to act on the given scheduled purchase's row buttons (issue #950).
// UX gate only -- the backend authorizeExecutionManagement in
// internal/api/handler_purchases_scope.go remains the security boundary; a
// false-positive here surfaces as a 403 toast on click rather than a
// successful mutation.
//
// Heuristic (mirrors the creator-scope model on History rows):
//   * admin (admin:* wildcard) or update-any:purchases -> manage anyone's row;
//   * otherwise the row's created_by_user_id must match the current user;
//   * legacy rows with a NULL created_by_user_id -> no buttons for non-
//     privileged users (out of reach without update-any).
function canManageScheduledPurchase(purchase: PlannedPurchase): boolean {
  if (canAccess('admin', '*') || canAccess('update-any', 'purchases')) return true;
  const user = state.getCurrentUser();
  if (!user) return false;
  if (!purchase.created_by_user_id) return false;
  return purchase.created_by_user_id === user.id;
}

/**
 * Render a single planned purchase row
 */
export function renderPlannedPurchaseRow(purchase: PlannedPurchase): string {
  const statusClass = getPlannedPurchaseStatusClass(purchase.status);
  const isPaused = purchase.status === 'paused';
  const isPending = purchase.status === 'pending' || purchase.status === 'notified';
  const canRun = isPending || isPaused;
  // Term and payment are separate columns, each with its own filter (the
  // two funnels used to share one header).
  const termCell = purchase.term > 0 ? formatTermShort(purchase.term) : '—';
  const paymentCell = purchase.term > 0
    ? escapeHtml(formatPaymentOption(purchase.payment))
    : '—';
  // Show an em-dash for upfront=0 unless the plan truly is all-upfront;
  // $0 upfront on "partial" or "no-upfront" is informative ($0 is real).
  // A zero on an all-upfront term almost always means missing data.
  const upfrontCell = purchase.upfront_cost > 0 || purchase.payment !== 'all-upfront'
    ? formatCurrency(purchase.upfront_cost)
    : '—';

  // platform#609: a step with no recommendations has nothing to buy, and its
  // count, resource type, upfront and savings are placeholders. Say so rather
  // than render them as zeros.
  const hasNoRecommendations = purchase.has_recommendations === false;
  // An estimate is not attached: it shows figures but Run now stays disabled.
  const isEstimate = purchase.estimate === true;
  const showFigures = !hasNoRecommendations || isEstimate;
  const estimateLabel = isEstimate
    ? ' <span class="cell-sub estimate-label" title="Current matching recommendations; the real figures are fixed when the step runs">Estimate</span>'
    : '';
  const unattachedText = purchase.estimate_note || 'No recommendations attached';
  const resourceCell = showFigures
    ? `${escapeHtml(purchase.resource_type)} <span class="cell-sub">(${escapeHtml(purchase.region)})</span>${estimateLabel}`
    : `<span class="no-recommendations" title="This step has no recommendations attached, so it cannot buy anything.">${escapeHtml(unattachedText)}</span>`;
  const placeholder = `<span class="cell-placeholder">${NO_VALUE}</span>`;
  const countCell = showFigures ? escapeHtml(String(purchase.count ?? '')) : placeholder;
  const upfrontShown = !showFigures || upfrontCell === NO_VALUE ? placeholder : upfrontCell;
  const savingsCell = showFigures ? `${formatCurrency(purchase.estimated_savings)}/mo` : placeholder;

  // Issue #365: gate row actions by the same plan-management permissions
  // a click on each button would require. Readonly users see no buttons
  // (status badge only); user role sees Run/Pause/Resume/Edit but not
  // Disable; admins see everything.
  //
  // Issue #950: AND in creator-scope ownership. A non-creator who lacks
  // update-any:purchases (a standard user looking at someone else's row)
  // sees NO action buttons, mirroring the backend ownership gate. This is
  // a UX gate; the backend authorizePlannedPurchaseCancel is the real
  // boundary.
  //
  // Disable (cancel) uses the shared canCancelScheduledExecution so it agrees
  // with the Home page Cancel and the backend: cancel-any / update-any act on
  // any row, cancel-own / delete only on the creator's own row, and a plan-bearing row
  // also needs update:plans (#1442, #1957).
  const canManagePurchase = canManageScheduledPurchase(purchase);
  const canRunPurchase = canManagePurchase && canAccess('execute', 'purchases') && canRun;
  const canPauseOrResumePurchase = canManagePurchase && canAccess('update', 'purchases');
  const canEditPlan = canManagePurchase && canAccess('update', 'plans');
  const canDisablePlan = canCancelScheduledExecution(purchase.created_by_user_id, purchase.plan_id);

  // #181: a pending step whose date has passed did not run; "pending" alone
  // reads as on schedule. Flag it and name the next action the user can take.
  const runLabel = hasNoRecommendations ? 'Run now: this step has no recommendations, so there is nothing to run' : 'Run now';
  const overdue = isPending && isPastScheduledDate(purchase.scheduled_date);
  const overdueHint = canRunPurchase && !hasNoRecommendations ? 'Not run on its date. Run now or pause.' : 'Not run on its date.';
  const overdueBadge = overdue
    ? badgeHtml('danger', 'Overdue', 'Scheduled date has passed')
    : '';
  const overdueHintHtml = overdue ? `<div class="overdue-hint">${overdueHint}</div>` : '';

  return `
    <tr class="planned-purchase-row ${statusClass}">
      <td>
        <span class="plan-name plan-name-cell" title="${escapeHtmlAttr(purchase.plan_name)}">${escapeHtml(purchase.plan_name)}</span>
        <span class="step-info">Step ${purchase.step_number}/${purchase.total_steps}</span>
      </td>
      <td>${formatDate(purchase.scheduled_date)}</td>
      <td>${providerBadgeHtml(purchase.provider)}</td>
      <td>${escapeHtml(serviceLabel(purchase.service, true))}</td>
      <td>${resourceCell}</td>
      <td class="num">${countCell}</td>
      <td>${termCell}</td>
      <td>${paymentCell}</td>
      <td class="num">${upfrontShown}</td>
      <td class="num savings">${savingsCell}</td>
      <td><span class="status-badges">${statusBadgeHtml(purchase.status)}${overdueBadge}</span>${overdueHintHtml}</td>
      <td class="actions"><span class="row-actions">
        ${canRunPurchase ? `<button data-action="run" data-id="${escapeHtmlAttr(purchase.id)}" class="btn-small primary icon-label row-action-run${hasNoRecommendations ? ' is-disabled' : ''}"${hasNoRecommendations ? ' aria-disabled="true"' : ''} title="${runLabel}" aria-label="${runLabel}">${iconSvg('play')}<span>Run now</span></button>` : ''}
        ${canPauseOrResumePurchase && isPending ? `<button data-action="pause" data-id="${escapeHtmlAttr(purchase.id)}" class="btn-small icon-btn" title="Pause" aria-label="Pause">${iconSvg('pause')}</button>` : ''}
        ${canPauseOrResumePurchase && isPaused ? `<button data-action="resume" data-id="${escapeHtmlAttr(purchase.id)}" class="btn-small icon-btn" title="Resume" aria-label="Resume">${iconSvg('play')}</button>` : ''}
        ${canEditPlan ? `<button data-action="edit" data-id="${escapeHtmlAttr(purchase.id)}" data-plan-id="${escapeHtmlAttr(purchase.plan_id)}" class="btn-small icon-btn" title="Edit Plan" aria-label="Edit Plan">${iconSvg('edit')}</button>` : ''}
        ${canDisablePlan ? `<button data-action="disable" data-id="${escapeHtmlAttr(purchase.id)}" class="btn-small danger icon-btn" title="Cancel this purchase and disable its plan" aria-label="Cancel this purchase and disable its plan">${iconSvg('x')}</button>` : ''}
      </span></td>
    </tr>
  `;
}

/**
 * Get CSS class for planned purchase status
 */
function getPlannedPurchaseStatusClass(status: string): string {
  switch (status) {
    case 'pending':
    case 'notified': return 'status-pending';
    case 'paused': return 'status-paused';
    case 'running': return 'status-running';
    case 'completed': return 'status-completed';
    case 'failed': return 'status-failed';
    default: return '';
  }
}
