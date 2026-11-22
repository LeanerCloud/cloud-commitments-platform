import * as api from '../api';
import type { HistoryPurchase } from '../types';
import { getCurrentUser } from '../state';
import { canAccess } from '../permissions';
import { normalizeStatus } from './cells';

// _fourEyesMode mirrors GlobalConfig.require_different_approver (issue #1005).
// Gates the inline Approve button so a creator can't approve their own pending
// purchase when dual-control is on. Starts ON, matching the fail-closed
// default refreshFourEyesMode uses, so a render that runs before the first
// config load cannot offer the creator an Approve button (#320).
let _fourEyesMode = true;

/**
 * Refresh _fourEyesMode and the dual-control banner from GlobalConfig.
 *
 * Every path that renders the approval queue must call this first, or the
 * queue renders as though dual control were off and offers the creator an
 * Approve button the backend will reject.
 *
 * An unreadable config fails closed, to dual-control ON. The fetch never
 * throws, so a config blip cannot block the render; it can only make the UI
 * more restrictive than the backend, never less. The cost is a temporarily
 * hidden Approve button during an outage, which self-corrects on the next
 * successful load.
 */
export async function refreshFourEyesMode(): Promise<void> {
  const cfgResponse = await api.getConfig().catch(() => null);
  _fourEyesMode = cfgResponse?.global
    ? cfgResponse.global.require_different_approver === true
    : true;
  const banner = document.getElementById('four-eyes-banner');
  if (banner) banner.classList.toggle('hidden', !_fourEyesMode);
}

// canCancelPendingRow returns true when the current session is permitted
// to cancel the given pending/notified history row via the session-authed
// Cancel button (issue #46). UX gate only — the backend
// authorizeSessionCancel in internal/api/handler_purchases_cancel.go remains the
// security boundary; if this helper is wrong-positive the API surfaces
// 403 and the click handler turns that into a "Failed to cancel" toast.
//
// Heuristic:
//   * admin → always yes (canAccess('admin', '*'));
//   * cancel-any:purchases → yes (operator roles, issue #158);
//   * cancel-own:purchases + matching created_by_user_id → yes;
//   * anyone else, or legacy row with no created_by_user_id → no.
export function canCancelPendingRow(p: HistoryPurchase): boolean {
  const status = (p.status || '').toLowerCase();
  if (status !== 'pending' && status !== 'notified') return false;
  const user = getCurrentUser();
  if (!user) return false;
  if (canAccess('cancel-any', 'purchases')) return true;
  // cancel-own: only the original creator. Legacy rows with no
  // created_by_user_id can't be cancelled via this UI; the email-token
  // path remains the escape hatch.
  if (!p.created_by_user_id) return false;
  return canAccess('cancel-own', 'purchases') && p.created_by_user_id === user.id;
}

// canApproveUnder4Eyes returns true when the 4-eyes dual-control policy
// (issue #1005, GlobalConfig.require_different_approver) allows sessionUserId
// to approve a row created by row.created_by_user_id. Mirrors the backend's
// requireDifferentApprover in internal/api/handler_purchases_approve_session.go: mode off →
// always allowed; mode on → allowed only when the row has a recorded,
// different creator (a NULL/legacy creator is denied, matching the backend's
// fail-closed 403 for rows that predate dual-control). Scheduler-created rows
// (created_by_system, no creator) pass: any identified approver is the second
// pair of eyes, and rbacAllowsApprove still requires approve-any for them.
export function canApproveUnder4Eyes(row: HistoryPurchase, sessionUserId: string): boolean {
  if (_fourEyesMode === false) return true;
  if (row.created_by_user_id == null) return row.created_by_system === true;
  return row.created_by_user_id !== sessionUserId;
}

// rbacAllowsApprove is the approve-permission decision (issue #286 /
// #1407) WITHOUT the 4-eyes overlay, so renderPendingActionButtons can
// distinguish "no permission at all" (no button, no badge) from
// "permission would allow it but 4-eyes blocks it" (badge instead of button).
export function rbacAllowsApprove(p: HistoryPurchase, user: { id: string }): boolean {
  const status = (p.status || '').toLowerCase();
  if (status !== 'pending' && status !== 'notified') return false;
  // approve-any:purchases is carved out of admin:* (issue #923) and is
  // granted by the seeded Purchaser group OR any custom group that
  // explicitly lists the verb in effectivePermissions. Gate on the
  // verb directly so a non-seeded role with the same grant still
  // approves rows the backend would also let through.
  if (canAccess('approve-any', 'purchases')) return true;
  // Four-eyes RBAC (issue #1407): the session must hold an explicit
  // approve-own grant before ownership is consulted. Ownership alone never
  // grants approve.
  if (!canAccess('approve-own', 'purchases')) return false;
  if (!p.created_by_user_id) return false;
  return p.created_by_user_id === user.id;
}

// canApprovePendingRow returns true when the current session is permitted
// to approve the given pending history row via the inline Approve button
// (issue #286). UX gate only — the backend authorizeSessionApprove in
// internal/api/handler_purchases_approve.go remains the security boundary; a
// false-positive here surfaces as a 403 toast on click rather than a
// successful approve.
//
// Heuristic (four-eyes RBAC — issue #1407; 4-eyes dual-control — issue #1005):
//   * status must be "pending" or "notified";
//   * any session with approve-any:purchases (carved-out admin verb,
//     seeded on Purchaser group; can also come from a custom group via
//     effectivePermissions) → approve-any; shows Approve on every pending row;
//   * session must also hold approve-own:purchases before ownership is
//     even evaluated (four-eyes: ownership alone does NOT grant approve);
//   * only then: the row's created_by_user_id must match the current user;
//   * legacy rows with NULL created_by_user_id → no (the email-token
//     path remains the escape hatch);
//   * finally, canApproveUnder4Eyes must allow it: when dual-control mode is
//     on, the session cannot approve a row it created itself.
export function canApprovePendingRow(p: HistoryPurchase): boolean {
  const user = getCurrentUser();
  if (!user) return false;
  if (!rbacAllowsApprove(p, user)) return false;
  return canApproveUnder4Eyes(p, user.id);
}

// canRetryFailedRow returns true when the current session is permitted
// to retry the given failed history row via the inline Retry button
// (issue #47). UX gate only — the backend authorizeSessionRetry in
// internal/api/handler_purchases_retry.go remains the security boundary.
//
// Heuristic:
//   * status must be "failed";
//   * row must NOT carry an ops_hint (persistent failure → no retry,
//     show the hint instead);
//   * row must NOT already have a retry_execution_id (we don't allow
//     retrying the same failure twice — the user should retry the
//     latest descendant in the chain);
//   * any session with retry-any:purchases (carved-out admin verb,
//     seeded on Purchaser group; can also come from a custom group via
//     effectivePermissions) → retry-any;
//   * otherwise the row's created_by_user_id must match the current
//     user (retry-own).
export function canRetryFailedRow(p: HistoryPurchase): boolean {
  const status = (p.status || '').toLowerCase();
  if (status !== 'failed') return false;
  if (p.ops_hint) return false;
  if (p.retry_execution_id) return false; // already retried — user should act on the descendant
  const user = getCurrentUser();
  if (!user) return false;
  // retry-any:purchases is carved out of admin:* (issue #923) and is
  // granted by the seeded Purchaser group OR any custom group that
  // explicitly lists the verb in effectivePermissions. Gate on the
  // verb directly so a non-seeded role with the same grant still
  // retries rows the backend would also let through.
  if (canAccess('retry-any', 'purchases')) return true;
  // Mirror canCancelPendingRow / canApprovePendingRow (issue #1418): require
  // the retry-own permission explicitly before checking creator match.
  // Without this gate a role that holds NO retry permission at all (e.g. a
  // Plan Authors group with only plan verbs) would still see the Retry button
  // on rows they created, because the creator check alone was a sufficient
  // condition. The backend authorizeSessionRetry is the real security boundary;
  // this closes the UX gap.
  if (!canAccess('retry-own', 'purchases')) return false;
  if (!p.created_by_user_id) return false;
  return p.created_by_user_id === user.id;
}

// canRevokeCompletedRow returns true when the current session may revoke the
// given purchase row via the inline Revoke button (issue #290).
// UX gate only -- the backend authorizeSessionRevoke remains the real
// security boundary.
//
// Conditions:
//   * status must be "completed", "" (legacy blank), or "scheduled"
//     (pre-fire delay: the cloud SDK has not been called yet -- free cancel);
//   * completed (and legacy blank) rows must be provider "azure": only Azure
//     reservations can be returned after purchase, AWS and GCP have no direct
//     cancel API. Scheduled rows are allowed for every provider because the
//     backend cancels them via CancelScheduledExecutionAtomic before any
//     cloud call (issue #924);
//   * revocation_window_closes_at must be in the future;
//     for "scheduled" rows this field is populated with scheduled_execution_at
//     by the backend (issue #290, second-wave CR Finding E);
//   * row must not already be revoked (revoked_at absent);
//   * session must have revoke-any:purchases or revoke-own:purchases. Without
//     this the button rendered for every signed-in user and the backend just
//     403d, replicating the same UX-vs-RBAC drift PR #995 caught for the
//     approve / delete paths. Mirror the peer predicates (canCancelPendingRow,
//     canApprovePendingRow, canRetryFailedRow) which all check canAccess.
export function canRevokeCompletedRow(p: HistoryPurchase): boolean {
  const status = (p.status || '').toLowerCase();
  if (status !== 'completed' && status !== '' && status !== 'scheduled') return false;
  if (status !== 'scheduled' && (p.provider || '').toLowerCase() !== 'azure') return false;
  if (p.revoked_at) return false; // already revoked
  if (!p.revocation_window_closes_at) return false;
  if (new Date(p.revocation_window_closes_at) <= new Date()) return false;
  const user = getCurrentUser();
  if (!user) return false;
  // RBAC: admin or revoke-any always; otherwise revoke-own. The backend
  // scopes revoke-own differently per row kind, so mirror both:
  //   * scheduled rows are executions: authorizeRevokeOwnExecution requires
  //     the creator, like the cancel/approve/retry predicates (#320);
  //   * completed purchase_history rows carry no creator, so revoke-own is
  //     account-scoped server-side (see ActionRevokeOwn in auth/types.go).
  if (canAccess('revoke-any', 'purchases')) return true;
  if (!canAccess('revoke-own', 'purchases')) return false;
  if (status !== 'scheduled') return true;
  return !!p.created_by_user_id && p.created_by_user_id === user.id;
}

// retryThresholdReached returns true when the row has hit the soft-
// block threshold (5 attempts). The frontend shows a confirm-with-
// warning dialog and forwards force=true on confirmation.
//
// Kept in sync with retryThreshold in internal/api/handler_purchases_retry.go;
// the backend remains authoritative — if this client predicate disagrees
// with the server, the API surfaces a structured 409 with retry_attempt_n
// + threshold and the toast falls back to the server message.
const RETRY_THRESHOLD = 5;

export function retryThresholdReached(p: HistoryPurchase): boolean {
  return (p.retry_attempt_n ?? 0) >= RETRY_THRESHOLD;
}

// canSellOnMarketplace returns true when the current session is permitted
// to list the given completed history row on the AWS RI Marketplace
// (issue #292). UX gate only -- the backend authorizeSessionSell in
// internal/api/handler_marketplace.go remains the security boundary; a
// false-positive here surfaces as a 403 toast on click.
//
// Conditions:
//   * row must be a completed purchase (status "completed" or absent);
//   * row must be an AWS EC2 RI (marketplace is EC2-only);
//   * offering_class must be "standard" OR still unknown (empty). CUDly stamps the
//     configured class on its own EC2 purchases; externally-created RIs
//     (and pre-migration rows) have an empty offering_class until the
//     backend lazily populates it from AWS on the list call. The backend
//     definitively gates -- it 400s a fetched "convertible" -- so we show the
//     button for unknown-class EC2 rows and let the backend decide, otherwise
//     the feature is unreachable end-to-end for the very case it targets;
//   * no active listing already (listing_state != "active"); and
//   * admin, or non-admin user (sell-own covers their own accounts --
//     we can't efficiently check per-account ownership client-side, so
//     we show the button for all non-admin users and let the backend 403
//     when the account is out of scope).
export function canSellOnMarketplace(p: HistoryPurchase): boolean {
  const status = normalizeStatus(p).toLowerCase();
  if (status !== 'completed') return false;
  // Marketplace listing is AWS EC2 Standard-RI only. Gate on provider/service
  // so unknown-class rows for non-EC2 providers never show the Sell button.
  if ((p.provider || '').toLowerCase() !== 'aws') return false;
  if ((p.service || '').toLowerCase() !== 'ec2') return false;
  const offeringClass = (p.offering_class || '').toLowerCase();
  if (offeringClass !== 'standard' && offeringClass !== '') return false;
  if ((p.listing_state || '').toLowerCase() === 'active') return false;
  const user = getCurrentUser();
  if (!user) return false;
  // Gate on the sell verbs, not bare sign-in, so we don't show Sell to a
  // role that lacks sell-own/sell-any and avoid frontend/backend auth drift
  // (the backend authorizeSessionSell would 403 anyway). admin:* satisfies
  // sell-own here since it is not carved out of admin.
  if (
    !canAccess('admin', '*') &&
    !canAccess('sell-any', 'purchases') &&
    !canAccess('sell-own', 'purchases')
  ) {
    return false;
  }
  // Guard against listing a matured RI: compute remaining months from the
  // purchase timestamp and the total term. purchase_history.term is stored in
  // YEARS (1 or 3), so convert to months before comparing against elapsed
  // months (mirrors the row.Term * 12 conversion in handler_marketplace.go).
  // Without the conversion a 3-year RI was treated as 3 months and the Sell
  // button vanished after ~3 months. We require at least 1 full month remaining.
  const termYears = typeof p.term === 'number' ? p.term : Number(p.term) || 0;
  if (termYears <= 0) return false;
  const termMonths = termYears * 12;
  const purchaseMs = new Date(p.timestamp).getTime();
  if (!Number.isFinite(purchaseMs)) return false;
  const elapsedMonths = (Date.now() - purchaseMs) / (1000 * 60 * 60 * 24 * 30.4375);
  const remainingMonths = termMonths - elapsedMonths;
  if (remainingMonths < 1) return false;
  return true;
}

// canCancelMarketplaceListing returns true when there is an active listing
// that the current session can cancel.
export function canCancelMarketplaceListing(p: HistoryPurchase): boolean {
  if ((p.listing_state || '').toLowerCase() !== 'active') return false;
  const user = getCurrentUser();
  if (!user) return false;
  // Same sell-verb gate as canSellOnMarketplace: cancelling a listing is a
  // marketplace write, so require sell-own/sell-any (or admin) rather than
  // bare sign-in to keep the UX gate aligned with authorizeSessionSell.
  if (
    !canAccess('admin', '*') &&
    !canAccess('sell-any', 'purchases') &&
    !canAccess('sell-own', 'purchases')
  ) {
    return false;
  }
  return true;
}
