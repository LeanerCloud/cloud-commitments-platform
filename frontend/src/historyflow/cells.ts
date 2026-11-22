import * as api from '../api';
import { formatCurrency, escapeHtml, NO_VALUE } from '../utils';
import type { HistoryPurchase } from '../types';
import { formatTermShort } from '../lib/table-format';
import { statusBadgeHtml } from '../lib/status-badge';
import { hasNoCommitments, isRealTerm, NO_RECOMMENDATIONS_LABEL, NO_RECOMMENDATIONS_TITLE } from '../lib/purchase-row-guards';

// Mirrors the concrete providers of backend validProviders in internal/api/validation.go;
// keep the two in sync.
export const VALID_PROVIDERS: api.Provider[] = ['aws', 'azure', 'gcp'];

export type StatusFilter = 'all' | 'pending' | 'completed' | 'failed' | 'expired' | 'cancelled';

// Rows that bought nothing must not show their estimate as a realized saving.
const NO_SAVINGS_STATUSES = new Set(['failed', 'cancelled', 'canceled', 'expired']);

// A revoked purchase keeps its "completed" status but no longer saves anything.
export function boughtNothing(p: HistoryPurchase): boolean {
  return Boolean(p.revoked_at) || NO_SAVINGS_STATUSES.has(normalizeStatus(p).toLowerCase());
}

export function noSavingsCell(p: HistoryPurchase, empty: boolean): string {
  if (empty || boughtNothing(p)) {
    return `<td class="num" data-label="Monthly Savings">${MUTED_DASH}</td>`;
  }
  return `<td class="num savings" data-label="Monthly Savings">${formatCurrency(p.estimated_savings)}</td>`;
}

// About what the 4-line clamp shows in the narrowest (phone card) layout;
// longer text also gets a disclosure, since a title tooltip is unreachable by
// keyboard and touch.
const STATUS_CLAMP_CHARS = 80;

export function statusFullTextHtml(text: string): string {
  if (text.length <= STATUS_CLAMP_CHARS) return '';
  return `<details class="history-status-full"><summary>Full message</summary><div>${escapeHtml(text)}</div></details>`;
}

export function normalizeStatus(p: HistoryPurchase): string {
  // Absent status → legacy DB row → counts as completed for filtering.
  return p.status || 'completed';
}

// isInFlight reports whether a status is an approved-but-not-yet-finalised
// execution (issue #621): the synchronous AWS purchase is mid-execution or got
// interrupted (Lambda timeout / crash). These rows MUST NOT render as the green
// "Completed" badge — doing so would tell the user a purchase finished when it
// may not have, tempting a re-approval / double-spend. They are grouped under
// the "Pending" filter chip (not "Completed") for the same reason.
//
// 'scheduled' (issue #239) is included here too: historyExecutionStatuses
// (internal/api/handler_history.go) deliberately keeps 'scheduled' rows in
// history so the inline Revoke button stays reachable, but the provider
// call for a delayed purchase has not fired yet -- it is exactly as
// unfinished as 'approved'/'running'/'paused'.
export function isInFlightStatus(s: string): boolean {
  return s === 'approved' || s === 'running' || s === 'paused' || s === 'scheduled';
}

// historyDisplayStatus folds the purchase lifecycle onto the statuses the
// Status column shows. Tone and text then come from lib/status-badge.ts, the
// same map every other page uses (issue #737).
function historyDisplayStatus(status: string): string {
  const normalized = (status || 'completed').toLowerCase();
  switch (normalized) {
    case 'pending':
    case 'notified':
      return 'pending';
    case 'approved':
    case 'running':
    case 'paused':
      // In-flight (issue #621): not finished — never show the green Completed
      // badge for these, or the user may think the purchase is done.
      return 'in_progress';
    case 'scheduled':
      // Issue #239: a delayed purchase whose provider call has not fired
      // yet — must never fall through to the default green Completed badge.
      return 'scheduled';
    case 'canceled':
    case 'cancelled':
      // Migration 000089 (expand-contract rename): the backend may return
      // either the new US spelling ('canceled') or the legacy British
      // spelling ('cancelled') during the rolling deploy window. Match both
      // so a row written by EITHER old or new code renders the muted Cancelled
      // badge instead of falling through to the green Completed default.
      // The CONTRACT migration (#1278) will normalize the data once the deploy
      // is stable; the British branch can be removed then.
      return 'cancelled';
    case 'partially_completed':
      // #642: some commitments succeeded, some failed. Not a clean success
      // and never "failed" (real commitments exist) — a distinct warning badge
      // so the user knows to read the description and reconcile the failures.
      return 'partially_completed';
    case 'failed':
    case 'expired':
      return normalized;
    default:
      return 'completed';
  }
}

export function statusBadgeHTML(status: string): string {
  return statusBadgeHtml(historyDisplayStatus(status));
}

export function buildStatusChipRowHTML(purchases: HistoryPurchase[], active: StatusFilter): string {
  const counts: Record<StatusFilter, number> = {
    all: purchases.length,
    pending: 0,
    completed: 0,
    failed: 0,
    expired: 0,
    cancelled: 0,
  };
  for (const p of purchases) {
    const s = normalizeStatus(p).toLowerCase();
    if (s === 'pending' || s === 'notified' || isInFlightStatus(s)) counts.pending++;
    // Migration 000089 (expand-contract rename): the backend may return
    // either spelling during the rolling deploy window. Counting only the
    // British spelling would silently bucket new 'canceled' rows into the
    // Completed total, hiding them from the user.
    else if (s === 'canceled' || s === 'cancelled') counts.cancelled++;
    else if (s === 'failed') counts.failed++;
    else if (s === 'expired') counts.expired++;
    else counts.completed++;
  }
  // Only render Failed / Expired / Cancelled chips when there's something in
  // them — keeps the row uncluttered on healthy deployments that have never
  // seen one. All / Pending / Completed always render so the user has a
  // consistent filter affordance even on an empty or fresh dataset.
  const allChips: Array<{ key: StatusFilter; label: string }> = [
    { key: 'all', label: 'All' },
    { key: 'pending', label: 'Pending' },
    { key: 'completed', label: 'Completed' },
    { key: 'failed', label: 'Failed' },
    { key: 'expired', label: 'Expired' },
    { key: 'cancelled', label: 'Cancelled' },
  ];
  const chips = allChips.filter(c => c.key === 'all' || c.key === 'pending' || c.key === 'completed' || counts[c.key] > 0);
  return `
    <div class="status-chip-row" role="tablist" aria-label="Filter history by status">
      ${chips.map(c => `
        <button type="button"
          class="status-chip${active === c.key ? ' active' : ''}"
          data-history-status="${c.key}"
          role="tab"
          aria-selected="${active === c.key}"
        >${c.label} (${counts[c.key]})</button>
      `).join('')}
    </div>
  `;
}

export const MUTED_DASH = `<span class="muted">${NO_VALUE}</span>`;

export const NO_RECS_HTML = `<span class="no-recommendations" title="${NO_RECOMMENDATIONS_TITLE}">${NO_RECOMMENDATIONS_LABEL}</span>`;

// #181: a row with no commitments has placeholder count/term/costs (0, "0
// Years", $0) and a "0 commitment(s)" type; render dashes and say why
// instead. A 0 term on a real row (a mixed-term basket) is also a dash.
export function termCell(p: HistoryPurchase): string {
  return isRealTerm(p.term) ? escapeHtml(formatTermShort(p.term)) : MUTED_DASH;
}

// historyDate is the Date cell: the same locale numeric form as the From and
// To date inputs (issue #1681).
export function historyDate(timestamp: string): string {
  const d = new Date(timestamp);
  return isNaN(d.getTime())
    ? ''
    : d.toLocaleDateString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit' });
}

export function providerCell(p: HistoryPurchase): string {
  // A no-commitment row (#181) still knows its provider when plan-originated
  // (#1681); only the unattributable "multiple" fallback is meaningless there.
  const unknown = !p.provider || p.provider === 'multiple';
  if (hasNoCommitments(p) && unknown) return MUTED_DASH;
  if (unknown) return '<span class="provider-badge">Multiple</span>';
  // Whitelist provider to prevent stored XSS via class attribute injection (#443).
  const safeProvider = (VALID_PROVIDERS as string[]).includes(p.provider) ? p.provider : 'unknown';
  return `<span class="provider-badge ${safeProvider}">${safeProvider.toUpperCase()}</span>`;
}

// upfrontCell renders the Upfront Cost cell. With amortize on, the upfront is
// already spread into the Monthly Cost column, so the cell is muted and
// labeled to stop Upfront + 12 x Monthly being read as additive (#318).
export function upfrontCell(p: HistoryPurchase, amortize: boolean): string {
  const amount = formatCurrency(p.upfront_cost);
  const folded = amortize && p.monthly_cost != null && p.upfront_cost != null && p.upfront_cost > 0 && p.term > 0;
  if (!folded) return `<td class="num" data-label="Upfront Cost">${amount}</td>`;
  return `<td class="num upfront-in-monthly" data-label="Upfront Cost" title="Included in the amortized monthly cost">${amount}<div class="upfront-in-monthly-note">(in monthly)</div></td>`;
}

// Round to display precision so typed values match the rendered cell value
// (formatCurrency default of 0 fraction digits, formatTerm renders the
// integer term unchanged).
export function roundForDisplay(n: number): number {
  if (!Number.isFinite(n)) return n;
  return Number(n.toFixed(0));
}
