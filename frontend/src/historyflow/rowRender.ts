import { escapeHtml, escapeHtmlAttr, NO_VALUE } from '../utils';
import type { HistoryPurchase } from '../types';
import { iconSvg } from '../icons';
import { renderApprovalQueueLink } from '../history-queue-link';
import { getCurrentUser } from '../state';
import { MUTED_DASH } from './cells';
import { canCancelPendingRow, canApproveUnder4Eyes, rbacAllowsApprove, canApprovePendingRow, canRetryFailedRow, canRevokeCompletedRow, retryThresholdReached, canSellOnMarketplace, canCancelMarketplaceListing } from './rowGuards';

// shortExecID renders the first 8 chars of a UUID so inline lineage
// links ("Retried as #abc12345") stay readable in the table cell. The
// full ID is preserved in the data-history-status attribute so the
// click handler can deep-link without truncation surprises.
function shortExecID(id: string): string {
  return (id || '').replace(/^urn:.*?:/, '').slice(0, 8);
}

// renderPendingActionButtons returns the inline Approve / Cancel
// button HTML for a pending|notified row, or "" when neither verb is
// available to the current session. Extracted from renderActionCell
// so the approval-queue card can emit identical buttons without
// duplicating the predicate logic or the DOM contract. Each predicate
// is checked independently so a custom role with only one of the
// verbs renders just that button; Approve sits to the left as the
// affirmative action.
export function renderPendingActionButtons(p: HistoryPurchase): string {
  if (!p.purchase_id) return '';
  const buttons: string[] = [];
  const user = getCurrentUser();
  if (canApprovePendingRow(p)) {
    buttons.push(`<button type="button" class="btn-small icon-btn history-approve-btn" data-approve-id="${escapeHtmlAttr(p.purchase_id)}" aria-label="Approve" title="Approve purchase">${iconSvg('check')}</button>`);
  } else if (user && rbacAllowsApprove(p, user) && !canApproveUnder4Eyes(p, user.id)) {
    // RBAC would allow Approve, but 4-eyes dual-control (issue #1005) blocks
    // this session from approving its own row. Surface the reason inline
    // instead of silently hiding the action. A row with no recorded creator
    // can never get a different approver, so say who can unblock it (#233).
    const reason = p.created_by_user_id == null
      ? 'No recorded creator - an admin must disable 4-eyes mode'
      : 'Awaiting different approver';
    buttons.push(`<span class="badge badge-muted">${reason}</span>`);
  }
  if (canCancelPendingRow(p)) {
    buttons.push(`<button type="button" class="btn-small btn-danger icon-btn history-cancel-btn" data-cancel-id="${escapeHtmlAttr(p.purchase_id)}" aria-label="Cancel" title="Cancel purchase">${iconSvg('x')}</button>`);
  }
  // Compact icon buttons side by side (aria-label + title carry the verb).
  return buttons.length ? `<span class="row-actions">${buttons.join('')}</span>` : '';
}

// renderActionCell returns the HTML for the Plan / action column on a
// single history row. The column doubles as the per-row action surface
// because pending / failed rows rarely have a meaningful plan_name (the
// pending plan info already shows in StatusDescription) and reusing the
// existing column keeps table width unchanged.
//
// Decision tree (status-driven, mutually exclusive):
//   * pending|notified + any action → "Review in approval queue" link (issue
//                                     #183; Approve / Cancel live in the queue)
//   * failed + ops_hint set         → ⚠ ops-hint badge (issue #47, Q3 — no retry),
//                                     plus any lineage link (issue #1040)
//   * failed + threshold reached    → "Retried 5× — confirm to override" Retry button (Q2)
//   * failed + canRetry             → standard ↻ Retry button
//   * any row with retry lineage    → inline ↻ Retried as / ↻ Retry of link
//   * else                          → plan_name or NO_VALUE
// renderLineageLinks returns the inline retry-chain cross-links for a row: a
// row that was retried (retry_execution_id set) or is itself a retry
// (retry_attempt_n > 0) links to the other end of the chain so the user can
// navigate without scrolling. Both can be true simultaneously on a
// middle-of-chain row (failed_v2 was retried into v3 AND is itself a retry
// of v1).
function renderLineageLinks(p: HistoryPurchase): string[] {
  const lineage: string[] = [];
  if (p.retry_execution_id) {
    lineage.push(`<a href="#history?execution=${encodeURIComponent(p.retry_execution_id)}" class="history-retry-link" title="Jump to the retry execution">${iconSvg('rotate-cw')} Retried as #${escapeHtml(shortExecID(p.retry_execution_id))}</a>`);
  }
  if ((p.retry_attempt_n ?? 0) > 0) {
    // We don't carry a back-pointer field — a future enhancement
    // could surface the predecessor's exec ID via the API. For now
    // we render a static badge (no link target) so users at least
    // see "this is a retry" provenance.
    // Coerce to a number: the history response is cast, not validated
    // (api.getHistory), so the TS number type is not a runtime guarantee
    // and this value sits inside a title="..." attribute.
    const attemptN = Number(p.retry_attempt_n) || 0;
    lineage.push(`<span class="history-retry-link history-retry-of" title="This row is retry #${attemptN} in its chain">${iconSvg('rotate-cw')} Retry #${attemptN}</span>`);
  }
  return lineage;
}

// opsHintHtml renders a failed row's operator hint (issue #47) as a note
// under the status pill, where the other status details already go. It
// used to fill the narrow, sticky action column with long provider error
// text. Empty for rows without a hint.
export function opsHintHtml(p: HistoryPurchase): string {
  if ((p.status || '').toLowerCase() !== 'failed' || !p.ops_hint) return '';
  const title = p.redrive_unsafe
    ? 'Permanent: this execution can never be retried; check the account, then start a new purchase if needed'
    : 'Operator action required — Retry will not help until this is fixed';
  return `<div class="history-ops-hint" title="${title}">${iconSvg('alert-triangle')} ${escapeHtml(p.ops_hint)}</div>`;
}

export function renderActionCell(p: HistoryPurchase): string {
  // Pending → the Approval queue card is the one place to approve or cancel
  // (issue #183). Where the session has a pending action (or a 4-eyes reason
  // it can't), the table used to repeat the queue's controls; it now links
  // to the row's queue entry instead.
  if (p.purchase_id && renderPendingActionButtons(p)) {
    return renderApprovalQueueLink(p.purchase_id);
  }

  // Failed → either ops-hint (no retry possible), Retry (with optional
  // threshold-confirm flag), plus lineage link to the successor if it
  // exists. We never show ops-hint AND Retry on the same row — the
  // hint replaces the action entirely because retrying a persistent
  // misconfig is guaranteed to fail again.
  if ((p.status || '').toLowerCase() === 'failed') {
    if (p.ops_hint) {
      // The hint itself sits under the status pill (opsHintHtml); this
      // column keeps only the lineage links (issue #1040: a legacy row
      // retried before the re-drive gate (#967) carries the permanent hint
      // and the operator still needs the link to its successor).
      const lineage = renderLineageLinks(p);
      return lineage.length > 0 ? lineage.join(' ') : MUTED_DASH;
    }
    if (canRetryFailedRow(p) && p.purchase_id) {
      const overThreshold = retryThresholdReached(p);
      const label = overThreshold
        ? `${iconSvg('alert-triangle')}<span>Retried ${Number(p.retry_attempt_n) || 0}× — click to override</span>`
        : `${iconSvg('rotate-cw')}<span>Retry</span>`;
      const cls = overThreshold
        ? 'btn-small icon-label history-retry-btn history-retry-over-threshold'
        : 'btn-small icon-label history-retry-btn';
      return `<button type="button" class="${cls}" data-retry-id="${escapeHtmlAttr(p.purchase_id)}">${label}</button>`;
    }
  }

  const lineage = renderLineageLinks(p);
  // Build the trailing action buttons so they compose with lineage links
  // — a retry-descendant can also have an active listing that needs a
  // Cancel button, or be an Azure row still inside its revoke window.
  const trailingActions: string[] = [];
  if (p.purchase_id) {
    // Revoke button (issue #290): completed Azure rows within the refund
    // window, and scheduled rows on any provider before they fire (#924).
    // Completed AWS and GCP rows have no cancel API, so they get no button.
    if (canRevokeCompletedRow(p)) {
      trailingActions.push(`<button type="button" class="btn-link history-revoke-btn" data-revoke-id="${escapeHtml(p.purchase_id)}" data-revoke-scheduled="${(p.status || '').toLowerCase() === 'scheduled'}">Revoke</button>`);
    }
    // Completed Standard RI rows (AWS): Cancel listing / Sell on Marketplace
    // (issue #292). Mutually exclusive with revoke in practice (revoke on a
    // completed row is Azure-only, marketplace is AWS Standard-RI-only and
    // never applies to a scheduled row).
    if (canCancelMarketplaceListing(p)) {
      trailingActions.push(`<button type="button" class="btn-link history-marketplace-cancel-btn" data-marketplace-cancel-id="${escapeHtml(p.purchase_id)}">Cancel listing ${escapeHtml(p.listing_id || '')}</button>`);
    } else if (canSellOnMarketplace(p)) {
      trailingActions.push(`<button type="button" class="btn-link history-marketplace-sell-btn" data-marketplace-sell-id="${escapeHtml(p.purchase_id)}">Sell on Marketplace</button>`);
    }
  }

  if (lineage.length > 0 || trailingActions.length > 0) {
    return [...lineage, ...trailingActions].join(' ');
  }

  return p.plan_name
    ? `<span class="cell-truncate" title="${escapeHtmlAttr(p.plan_name)}">${escapeHtml(p.plan_name)}</span>`
    : NO_VALUE;
}
