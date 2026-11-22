import * as state from '../state';
import { formatDate, escapeHtml } from '../utils';
import { applyColumnFilters as applyColumnFiltersLib } from '../lib/column-filters';
import { openColumnFilterPopover, rebindColumnFilterPopover, sortedDistinctValues } from '../lib/column-filter-popover';
import type { ConvertibleRI, ExchangeableAzureRI, RIUtilization } from '../api';
import { canAccess } from '../permissions';
import { iconSvg } from '../icons';
import { attachAzureExchangeToolbar, buildAzureSelectCell, buildAzureSelectHeader, canPreviewAzureExchange } from '../riexchange-azure';
import { formatIsoTermShort, formatPaymentOption } from '../lib/table-format';
import { riexState } from './riexState';
import { renderEmptyState, riAccountLabel } from './shared';
import { openExchangeModal } from './exchangeModal';
import { loadRIExchange } from './load';

export function hasKnownUtilization(riID: string): boolean {
  const pct = riexState.currentUtilization.get(riID)?.utilization_percent;
  return typeof pct === 'number' && Number.isFinite(pct);
}

export function renderRIsTable(container: HTMLElement, accountID?: string): void {
  if (!riexState.currentRIs || riexState.currentRIs.length === 0) {
    // issue #871: name the active scope so the user knows the result is
    // filtered (by the account chip) rather than globally empty.
    const msg = accountID
      ? `No active convertible Reserved Instances for AWS account ${accountID}.`
      : 'No active convertible Reserved Instances found for AWS.';
    renderEmptyState(container, msg);
    return;
  }

  // Issue #365: RI exchanges mutate cloud-provider RI state and are
  // admin-only by default on the backend. Hide the per-row Exchange
  // button for non-admin sessions so readonly users don't get a 403
  // on click. Defense in depth, backend still enforces.
  const canExchange = canAccess('admin', '*');

  // Apply per-column filters (issue #1414). Mirrors the pattern in
  // renderRecommendations; broken numeric expressions are skipped so
  // the popover can surface the inline error without forcing a clear.
  const filters = state.getActiveRiColumnFilters();
  const visibleRIs = applyActiveRiColumnFilters(riexState.currentRIs, riexState.currentUtilization, filters);

  const filterBtn = (column: state.ActiveRiColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = activeRiLabelFor(column);
    const label = filters[column] ? `Filter ${lbl} — currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${escapeHtml(label)}" title="${escapeHtml(label)}">${iconSvg('filter')}</button>`;
  };

  const accountLabels = [...new Set(riexState.currentRIs.map(r => riAccountLabel(r.account_id)))];
  container.innerHTML = `
    <p class="setting-hint" id="ri-exchange-account-label">Account: ${escapeHtml(accountLabels.join(', '))}</p>
    <table class="table-compact${canExchange ? ' table-sticky-end' : ''}">
      <thead>
        <tr>
          <th>RI ID</th>
          <th>Instance Type${filterBtn('instance_type')}</th>
          <th>AZ${filterBtn('availability_zone')}</th>
          <th class="num">Count${filterBtn('instance_count')}</th>
          <th>Offering${filterBtn('offering_type')}</th>
          <th>Expiry</th>
          <th>Utilization${filterBtn('utilization_pct')}</th>
          ${canExchange ? '<th>Actions</th>' : ''}
        </tr>
      </thead>
      <tbody>
        ${visibleRIs.length === 0 ? `<tr><td colspan="${canExchange ? 8 : 7}" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${visibleRIs.map(ri => {
          const utilPct = hasKnownUtilization(ri.reserved_instance_id)
            ? riexState.currentUtilization.get(ri.reserved_instance_id)!.utilization_percent
            : null;
          const utilClass = utilPct === null ? '' : utilPct >= 95 ? 'util-green' : utilPct >= 70 ? 'util-yellow' : 'util-red';
          const utilText = utilPct !== null
            ? `${utilPct.toFixed(1)}%`
            : riexState.utilizationStatus === 'loading'
              ? '<span class="loading-inline">...</span>'
              : `<span class="text-muted" title="${riexState.utilizationStatus === 'failed' ? 'Utilization could not be loaded' : 'No utilization data was returned for this RI'}">No data</span>`;
          return `
          <tr>
            <td class="monospace">${escapeHtml(ri.reserved_instance_id)}</td>
            <td>${escapeHtml(ri.instance_type)}</td>
            <td>${escapeHtml(ri.availability_zone)}</td>
            <td class="num">${ri.instance_count}</td>
            <td>${escapeHtml(formatPaymentOption(ri.offering_type))}</td>
            <td>${formatDate(ri.end)}</td>
            <td class="${utilClass}">${utilText}</td>
            ${canExchange ? `<td><button class="btn-small" data-action="quote-ri" data-ri-id="${escapeHtml(ri.reserved_instance_id)}" data-count="${ri.instance_count}" aria-label="Exchange ${escapeHtml(ri.reserved_instance_id)}">Exchange</button></td>` : ''}
          </tr>`;
        }).join('')}
      </tbody>
    </table>
  `;

  // Wire per-column filter buttons (same pattern as renderRecommendations).
  // e.stopPropagation prevents the surrounding <th> from also handling
  // the click so future sort handlers won't conflict.
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.ActiveRiColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      activeRiOpenPopoverFor(column, btn);
    });
  });
  // Keep an open filter popover attached to the re-rendered trigger.
  rebindColumnFilterPopover(ACTIVE_RI_POPOVER_SCOPE);

  // Attach "Exchange" handlers for individual RIs
  container.querySelectorAll<HTMLButtonElement>('[data-action="quote-ri"]').forEach(btn => {
    btn.addEventListener('click', () => {
      const count = parseInt(btn.dataset['count'] || '1', 10);
      openExchangeModal(btn.dataset['riId'] || '', isNaN(count) ? 1 : count);
    });
  });
}

/**
 * Render the Azure exchangeable-reservations table (issue #871). Built
 * entirely via DOM construction / textContent — never innerHTML — so any
 * Azure-derived field (SKU, display name, region) cannot inject markup.
 *
 * Users who can preview an exchange get a selection column and an
 * "Exchange selected" action that opens the Azure exchange modal
 * (riexchange-azure.ts, issue #81).
 */
export function renderAzureRIsTable(
  container: HTMLElement,
  reservations: ExchangeableAzureRI[],
  accountID?: string,
): void {
  if (!reservations || reservations.length === 0) {
    const msg = accountID
      ? `No exchangeable reservations for Azure subscription ${accountID}.`
      : 'No exchangeable reservations found for Azure.';
    renderEmptyState(container, msg);
    return;
  }

  container.textContent = '';
  const table = document.createElement('table');
  const canExchange = canPreviewAzureExchange();
  table.className = canExchange ? 'table-compact table-sticky-start' : 'table-compact';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  const labels = ['Reservation', 'SKU', 'Quantity', 'Region', 'Term', 'Expiry'];
  if (canExchange) headerRow.appendChild(buildAzureSelectHeader());
  for (const label of labels) {
    const th = document.createElement('th');
    th.textContent = label;
    if (label === 'Quantity') th.className = 'num';
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  reservations.forEach((r, i) => {
    const tr = buildAzureRIRow(r);
    if (canExchange) tr.prepend(buildAzureSelectCell(r, i));
    tbody.appendChild(tr);
  });
  table.appendChild(tbody);
  container.appendChild(table);
  if (canExchange) attachAzureExchangeToolbar(container, table, reservations, () => { void loadRIExchange(); });
}

function buildAzureRIRow(r: ExchangeableAzureRI): HTMLTableRowElement {
  const tr = document.createElement('tr');

  appendAzureCell(tr, r.display_name || r.reservation_id);
  appendAzureCell(tr, r.sku);
  appendAzureCell(tr, String(r.quantity), true);
  appendAzureCell(tr, r.region || '—');
  appendAzureCell(tr, formatIsoTermShort(r.term) || '—');
  appendAzureCell(tr, r.expiry_date ? formatDate(r.expiry_date) : '—');

  return tr;
}

// numeric right-aligns the cell with tabular digits (td.num, issue #739).
function appendAzureCell(tr: HTMLTableRowElement, text: string, numeric = false): void {
  const td = document.createElement('td');
  td.textContent = text;
  if (numeric) td.className = 'num';
  tr.appendChild(td);
}

// ── Active Convertible RIs column filters (issue #1414) ──────────────────

interface ActiveRiColumnDef {
  key: state.ActiveRiColumnId;
  label: string;
  kind: 'numeric' | 'categorical';
}

const ACTIVE_RI_COLUMN_DEFS: readonly ActiveRiColumnDef[] = [
  { key: 'instance_type',     label: 'Instance Type', kind: 'categorical' },
  { key: 'availability_zone', label: 'AZ',            kind: 'categorical' },
  { key: 'offering_type',     label: 'Offering',      kind: 'categorical' },
  { key: 'instance_count',    label: 'Count',         kind: 'numeric'     },
  { key: 'utilization_pct',   label: 'Utilization %', kind: 'numeric'     },
];

const ACTIVE_RI_NUMERIC_COLUMNS: ReadonlySet<state.ActiveRiColumnId> = new Set(
  ACTIVE_RI_COLUMN_DEFS.filter((c) => c.kind === 'numeric').map((c) => c.key),
);

function activeRiLabelFor(col: state.ActiveRiColumnId): string {
  return ACTIVE_RI_COLUMN_DEFS.find((c) => c.key === col)?.label ?? col;
}

function activeRiCategoricalCellValue(ri: ConvertibleRI, col: state.ActiveRiColumnId): string {
  switch (col) {
    case 'instance_type':     return ri.instance_type ?? '';
    case 'availability_zone': return ri.availability_zone ?? '';
    case 'offering_type':     return ri.offering_type ?? '';
    case 'instance_count':
    case 'utilization_pct':   return '';
  }
}

function activeRiNumericCellValue(
  ri: ConvertibleRI,
  util: Map<string, RIUtilization>,
  col: state.ActiveRiColumnId,
): number {
  switch (col) {
    case 'instance_count':  return ri.instance_count ?? 0;
    case 'utilization_pct': {
      const u = util.get(ri.reserved_instance_id);
      return u !== undefined ? u.utilization_percent : Number.NaN;
    }
    case 'instance_type':
    case 'availability_zone':
    case 'offering_type':   return Number.NaN;
  }
}

function activeRiDisplayPrecision(col: state.ActiveRiColumnId): number {
  switch (col) {
    case 'instance_count':  return 0;
    case 'utilization_pct': return 1;
    case 'instance_type':
    case 'availability_zone':
    case 'offering_type':   return 0;
  }
}

/**
 * Pure filter function for the Active Convertible RIs table. Exported so
 * unit tests can exercise the filter pipeline without DOM setup. Mirrors
 * applyRiExchangeColumnFilters.
 */
export function applyActiveRiColumnFilters(
  ris: readonly ConvertibleRI[],
  util: Map<string, RIUtilization>,
  filters: state.ActiveRiColumnFilters,
): ConvertibleRI[] {
  return applyColumnFiltersLib<ConvertibleRI, state.ActiveRiColumnId>(ris, filters, {
    categorical: activeRiCategoricalCellValue,
    numeric: (ri, col) => {
      const raw = activeRiNumericCellValue(ri, util, col);
      if (!Number.isFinite(raw)) return raw;
      return Number(raw.toFixed(activeRiDisplayPrecision(col)));
    },
  });
}

// ── Active RI popover ─────────────────────────────────────────────────────
// Lifecycle lives in lib/column-filter-popover.ts (issue #18).

const ACTIVE_RI_POPOVER_SCOPE = 'active-ri';

function rerenderRIs(): void {
  const container = document.getElementById('ri-exchange-instances-list');
  if (container) renderRIsTable(container, riexState.currentRIAccountID);
}

function activeRiTrigger(column: state.ActiveRiColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `#ri-exchange-instances-list .column-filter-btn[data-column="${column}"]`,
  );
}

function activeRiOpenPopoverFor(column: state.ActiveRiColumnId, anchor: HTMLElement): void {
  openColumnFilterPopover({
    scope: ACTIVE_RI_POPOVER_SCOPE,
    column,
    label: activeRiLabelFor(column),
    kind: ACTIVE_RI_NUMERIC_COLUMNS.has(column) ? 'numeric' : 'categorical',
    placeholder: 'e.g. >50, 10..20, 4',
    distinctValues: sortedDistinctValues(riexState.currentRIs, (ri) => activeRiCategoricalCellValue(ri, column)),
    getFilter: () => state.getActiveRiColumnFilters()[column],
    onCommit: (filter) => {
      state.setActiveRiColumnFilter(column, filter);
      rerenderRIs();
    },
    findTrigger: () => activeRiTrigger(column),
  }, anchor);
}
