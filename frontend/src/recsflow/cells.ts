import * as state from '../state';
import type { CostPeriod } from '../state';
import { formatCurrency, formatTerm, escapeHtml, NO_VALUE } from '../utils';
import { serviceLabel } from '../lib/service-labels';
import { iconSvg } from '../icons';
import type { LocalRecommendation } from '../types';
import { canAccess } from '../permissions';
import { formatTermShort } from '../lib/table-format';
import { ownValue } from '../lib/own-property';
import { recsState } from './recsState';
import { providerDisplayName, providerBadgeClass, PAYMENT_DISPLAY_LABELS, formatPayment } from './shared';
import { displaySavingsPct, onDemandMonthly, formatCostForPeriod } from './money';
import { type ColumnDef, COLUMN_DEFS } from './columnDefs';
import { formatCapacity } from './cellValues';

// renderUsageSparkline returns an inline SVG polyline representing the
// usage_history coverage percentages (0-100, oldest-to-newest). Returns
// the em-dash character when pcts is null, undefined, or empty so the cell
// degrades gracefully for non-AWS providers and pre-#239 cached rows.
//
// The SVG is 56x20px. The polyline plots each point at x = i/(n-1) * 56
// and y = (1 - pct/100) * 18 + 1 so 0% maps to the bottom (y=19) and
// 100% maps to the top (y=1). A single-point series renders as a filled
// circle rather than a line so a "1 day" history doesn't look broken.
//
// No user content is interpolated into the SVG; all values are numbers
// so XSS is structurally impossible here.
export function renderUsageSparkline(pcts: number[] | null | undefined): string {
  if (!pcts || pcts.length === 0) return '—';
  const clamped = pcts.map((v) => {
    if (!Number.isFinite(v)) return 0;
    return Math.max(0, Math.min(100, v));
  });
  const w = 56;
  const h = 20;
  const pad = 1;
  const innerH = h - 2 * pad;
  const n = clamped.length;
  const aria = `RI coverage last ${n} days: ${clamped.map((v) => `${v.toFixed(1)}%`).join(', ')}`;
  if (n === 1) {
    const cy = pad + innerH * (1 - clamped[0]! / 100);
    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="${aria}" class="usage-sparkline"><circle cx="${(w / 2).toFixed(1)}" cy="${cy.toFixed(1)}" r="2.5" fill="currentColor"/></svg>`;
  }
  const points = clamped
    .map((p, i) => {
      const x = (i / (n - 1)) * w;
      const y = pad + innerH * (1 - p / 100);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
  return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="${aria}" class="usage-sparkline"><polyline points="${points}" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>`;
}

// renderColumnCell renders a single <td> for the given column key.
// All column cell rendering is centralised here so buildVariantRowMarkup
// can iterate over COLUMN_DEFS (or a visibility-filtered subset in
// Commit 2) without knowing each column's HTML shape.
function renderColumnCell(key: state.RecommendationsColumnId, rec: LocalRecommendation, ctx: {
  accountName: string;
  badge: string;
  pct: number | null;
  pctClass: string;
  pctText: string;
  period: CostPeriod;
}): string {
  switch (key) {
    case 'provider':
      return `<td><span class="provider-badge ${providerBadgeClass(rec.provider)}">${escapeHtml(providerDisplayName(rec.provider))}</span></td>`;
    case 'account':
      return `<td><span class="cell-truncate cell-truncate--sm" title="${escapeHtml(ctx.accountName)}">${escapeHtml(ctx.accountName)}</span></td>`;
    case 'service':
      return `<td><span class="service-badge" title="${escapeHtml(serviceLabel(rec.service))}">${escapeHtml(serviceLabel(rec.service))}</span></td>`;
    case 'resource_type':
      return `<td title="${escapeHtml(rec.resource_type)}"><span class="cell-truncate cell-truncate--sm" title="${escapeHtml(rec.engine ? `${rec.resource_type} (${rec.engine})` : rec.resource_type)}">${escapeHtml(rec.resource_type)}${rec.engine ? ` (${escapeHtml(rec.engine)})` : ''}</span>${ctx.badge}</td>`;
    case 'capacity': {
      const cap = formatCapacity(rec.vcpu, rec.memory_gb);
      return `<td>${cap !== null ? escapeHtml(cap) : '—'}</td>`;
    }
    case 'region':
      return `<td>${escapeHtml(rec.region)}</td>`;
    case 'count':
      return `<td class="num">${rec.count}</td>`;
    case 'term':
      return `<td>${formatTermShort(rec.term) || NO_VALUE}</td>`;
    case 'payment':
      return `<td>${escapeHtml(formatPayment(rec.payment))}</td>`;
    case 'savings':
      // issue #319: savings display value scales with the active cost period.
      return `<td class="num savings">${formatCostForPeriod(rec.savings, ctx.period)}</td>`;
    case 'upfront_cost':
      // upfront_cost is one-time, not recurring \u2014 period-invariant.
      return `<td class="num">${formatCurrency(rec.upfront_cost)}</td>`;
    case 'monthly_cost':
      // issue #319: monthly_cost display scales with period; null still renders as em-dash.
      return `<td class="num">${formatCostForPeriod(rec.monthly_cost, ctx.period)}</td>`;
    case 'on_demand_monthly':
      // issue #319: on-demand baseline scales with period to stay consistent
      // with the savings + monthly_cost columns.
      return `<td class="num">${formatCostForPeriod(onDemandMonthly(rec), ctx.period)}</td>`;
    case 'effective_savings_pct':
      return `<td${ctx.pctClass}>${ctx.pctText}</td>`;
    case 'usage_history':
      // Render a 7-point inline SVG sparkline of daily RI-coverage pcts.
      // renderUsageSparkline returns "—" for null/absent so the cell
      // degrades cleanly for non-AWS providers and pre-#239 cached rows.
      // No escaping needed: renderUsageSparkline only interpolates numbers.
      return `<td class="usage-sparkline-cell" title="RI coverage last 7 days">${renderUsageSparkline(rec.usage_history)}</td>`;
  }
}

// 'flat' is a single-variant cell, 'sp-child' a single-variant cell under a
// Savings Plans group row, and 'variant' one of several variants of a cell
// whose summary row already shows its identity (issue #1684).
type VariantRowNesting = 'flat' | 'sp-child' | 'variant';

// Columns every variant of a cell shares (cellKey), left blank on 'variant' rows.
const CELL_IDENTITY_KEYS: ReadonlySet<state.RecommendationsColumnId> = new Set(
  ['provider', 'account', 'service', 'resource_type', 'region'] as state.RecommendationsColumnId[],
);

// Helper: render one variant row (a single LocalRecommendation) with optional
// indentation styling for multi-variant cells.
//
// `cols` defaults to all COLUMN_DEFS; Commit 2 (column visibility) passes a
// visibility-filtered subset so hidden columns are absent from the DOM.
export function buildVariantRowMarkup(
  rec: LocalRecommendation,
  selectedRecs: ReadonlySet<string>,
  nesting: VariantRowNesting,
  cols: readonly ColumnDef[] = COLUMN_DEFS,
  showCheckboxes = true,
): string {
  // issue #319: cost-bearing cells scale with the active period; resolved
  // once per row and threaded through ctx so renderColumnCell doesn't
  // re-read state on every cell.
  const period = state.getCostPeriod();
  const savingsClass = rec.savings > 1000 ? 'high-savings' : rec.savings > 100 ? 'medium-savings' : '';
  const isSelected = selectedRecs.has(rec.id);
  const accountName = rec.cloud_account_id ? (recsState.accountNamesCache.get(rec.cloud_account_id) || rec.cloud_account_id) : '\u2014';
  const badge = renderSuppressionBadge(rec);
  const recId = escapeHtml(rec.id);
  const pct = displaySavingsPct(rec);
  const pctClass = pct !== null && pct < 0 ? ' class="num effective-pct-negative"' : ' class="num"';
  const pctText = pct === null ? '\u2014' : pct.toFixed(1) + '%';
  const nestedClass = nesting === 'flat' ? '' : ' rec-variant-row';
  const cellCtx = { accountName, badge, pct, pctClass, pctText, period };
  // Issue #869: viewer (readonly) sessions get no checkbox or action buttons
  // in the leading column; the cell is still emitted so column widths align
  // with the cell-summary rows (which always carry the expand chevron there).
  // Issue #120: inline "Plan" button deep-links into the Create Purchase Plan
  // modal pre-seeded with this rec. Only rendered when the session has
  // create:plans permission (mirrors the bulk Create Plan button gate).
  const planBtnHtml = canAccess('create', 'plans')
    ? `<button type="button" class="btn-small icon-btn rec-plan-btn" data-rec-id="${recId}" data-action="plan" aria-label="Create plan for this recommendation" title="Create a purchase plan from this recommendation">${iconSvg('clipboard-check')}</button>`
    : '';
  const selectLabel = escapeHtml(
    [
      'Select',
      rec.service,
      rec.engine ? `${rec.resource_type} (${rec.engine})` : rec.resource_type,
      rec.region,
      accountName,
      formatTerm(rec.term),
      ownValue(PAYMENT_DISPLAY_LABELS, rec.payment ?? '') ?? '',
    ].filter(part => part).join(' '),
  );
  const checkboxCell = showCheckboxes
    ? `<td class="checkbox-col"><span class="rec-select-cell"><input type="checkbox" data-rec-id="${recId}" ${isSelected ? 'checked' : ''} aria-label="${selectLabel}">${planBtnHtml}</span></td>`
    : `<td class="checkbox-col"></td>`;
  return `
  <tr class="recommendation-row${nestedClass} ${savingsClass} ${isSelected ? 'selected' : ''}" data-rec-id="${recId}">
    ${checkboxCell}
    ${cols.map((c) => (nesting === 'variant' && CELL_IDENTITY_KEYS.has(c.key)
      ? '<td class="rec-variant-identity"></td>'
      : renderColumnCell(c.key, rec, cellCtx))).join('')}
  </tr>`;
}


// Escaped "resource type (engine)" for a group summary row; empty when the
// provider sends no resource type (e.g. Azure savings plans).
export function cellIdentityLabel(rec: LocalRecommendation): string {
  if (!rec.resource_type) return '';
  return `${escapeHtml(rec.resource_type)}${rec.engine ? ` (${escapeHtml(rec.engine)})` : ''}`;
}

// renderSuppressionBadge returns HTML for the "recently purchased"
// indicator shown on recs the scheduler has annotated with an active
// purchase_suppression. Deep-links to Purchase History filtered to
// the execution whose suppression contributed the most. Returns ''
// when no suppression applies or the suppression has expired
// (defence-in-depth \u2014 the scheduler should have dropped such recs).
//
//   {suppressed} = rec.suppressed_count
//   {original}   = rec.count + rec.suppressed_count (rec.count is
//                  already post-subtraction)
//   {days}       = ceil((expires_at - now) / 24h), so 23h59m renders
//                  as "1d remaining" rather than "0d".
function renderSuppressionBadge(rec: LocalRecommendation): string {
  const suppressed = rec.suppressed_count ?? 0;
  if (suppressed <= 0) return '';
  const original = rec.count + suppressed;
  const expiresRaw = rec.suppression_expires_at;
  if (!expiresRaw) return '';
  const diffMs = new Date(expiresRaw).getTime() - Date.now();
  if (diffMs <= 0) return '';
  const days = Math.ceil(diffMs / (24 * 60 * 60 * 1000));
  const execID = rec.primary_suppression_execution_id;
  const href = execID ? `#history?execution=${encodeURIComponent(execID)}` : '#history';
  return ` <a class="rec-suppression-badge" href="${href}" title="Capacity from recent purchase; not re-proposed until it expires.">recently purchased ${suppressed}/${original} \u2014 ${days}d remaining</a>`;
}
