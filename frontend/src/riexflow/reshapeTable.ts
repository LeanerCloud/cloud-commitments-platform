import * as state from '../state';
import { escapeHtml } from '../utils';
import { applyColumnFilters as applyColumnFiltersLib } from '../lib/column-filters';
import { openColumnFilterPopover, rebindColumnFilterPopover, sortedDistinctValues } from '../lib/column-filter-popover';
import type { ReshapeRecommendation, OfferingOption } from '../api';
import { canAccess } from '../permissions';
import { iconSvg } from '../icons';
import { riexState } from './riexState';
import { formatOfferingCost } from './shared';
import { openExchangeModal } from './exchangeModal';
import { hasKnownUtilization } from './riTable';

// ──────────────────────────────────────────────
// Column filters (issue #166 follow-up to merged #570)
//
// Wires inline column filters to the reshape-recommendations table via the
// shared lib/column-filters helpers. Categorical columns (Source RI, Current,
// Suggested, Reason) get a checkbox-list popover; numeric columns (Count,
// Utilization, Normalized Used/Purchased) get a free-text expression popover.
// Display-rounded numeric values match what the cell renders so a typed
// value like "95.0" matches the visible utilization figure.
// ──────────────────────────────────────────────

interface RiExchangeColumnDef {
  key: state.RiExchangeColumnId;
  label: string;
  kind: 'numeric' | 'categorical';
}

const RIEX_COLUMN_DEFS: readonly RiExchangeColumnDef[] = [
  { key: 'source_ri_id',          label: 'Source RI',          kind: 'categorical' },
  { key: 'source_count',          label: 'Source count',       kind: 'numeric'     },
  { key: 'source_instance_type',  label: 'Source instance',    kind: 'categorical' },
  { key: 'target_count',          label: 'Target count',       kind: 'numeric'     },
  { key: 'target_instance_type',  label: 'Target instance',    kind: 'categorical' },
  { key: 'utilization_percent',   label: 'Utilization %',      kind: 'numeric'     },
  { key: 'normalized_used',       label: 'Normalized used',    kind: 'numeric'     },
  { key: 'normalized_purchased',  label: 'Normalized purchased', kind: 'numeric'   },
  { key: 'reason',                label: 'Reason',             kind: 'categorical' },
];

const RIEX_NUMERIC_COLUMNS: ReadonlySet<state.RiExchangeColumnId> = new Set(
  RIEX_COLUMN_DEFS.filter((c) => c.kind === 'numeric').map((c) => c.key),
);

function riexCategoricalCellValue(
  r: import('./api').ReshapeRecommendation,
  col: state.RiExchangeColumnId,
): string {
  switch (col) {
    case 'source_ri_id':         return r.source_ri_id ?? '';
    case 'source_instance_type': return r.source_instance_type ?? '';
    case 'target_instance_type': return r.target_instance_type ?? '';
    case 'reason':               return r.reason ?? '';
    // Numeric columns never reach this branch — return '' so the type matches.
    case 'source_count':
    case 'target_count':
    case 'utilization_percent':
    case 'normalized_used':
    case 'normalized_purchased': return '';
  }
}

function riexNumericCellValue(
  r: import('./api').ReshapeRecommendation,
  col: state.RiExchangeColumnId,
): number {
  switch (col) {
    case 'source_count':         return r.source_count ?? 0;
    case 'target_count':         return r.target_count ?? 0;
    case 'utilization_percent':  return r.utilization_percent ?? Number.NaN;
    case 'normalized_used':      return r.normalized_used ?? Number.NaN;
    case 'normalized_purchased': return r.normalized_purchased ?? Number.NaN;
    // Categorical columns never reach this branch — NaN fails every predicate.
    case 'source_ri_id':
    case 'source_instance_type':
    case 'target_instance_type':
    case 'reason':               return Number.NaN;
  }
}

// Decimal places the cell renders with — mirrors the toFixed() calls below
// in the row markup so a user typing the displayed value matches the
// rounded cell value. Counts render as integers; utilization and
// normalized units render with one decimal place.
function riexDisplayPrecision(col: state.RiExchangeColumnId): number {
  switch (col) {
    case 'source_count':
    case 'target_count':
      return 0;
    case 'utilization_percent':
    case 'normalized_used':
    case 'normalized_purchased':
      return 1;
    case 'source_ri_id':
    case 'source_instance_type':
    case 'target_instance_type':
    case 'reason':
      return 0;
  }
}

function riexRoundForDisplay(n: number, precision: number): number {
  if (!Number.isFinite(n)) return n;
  return Number(n.toFixed(precision));
}

export function applyRiExchangeColumnFilters(
  recs: readonly import('./api').ReshapeRecommendation[],
  filters: state.RiExchangeColumnFilters,
): import('./api').ReshapeRecommendation[] {
  return applyColumnFiltersLib<
    import('./api').ReshapeRecommendation,
    state.RiExchangeColumnId
  >(recs, filters, {
    categorical: riexCategoricalCellValue,
    numeric: (r, col) => riexRoundForDisplay(riexNumericCellValue(r, col), riexDisplayPrecision(col)),
  });
}

// ── Reshape recommendations popover ──────────────────────────────────────

const RIEX_POPOVER_SCOPE = 'riex';

function riexLabelFor(col: state.RiExchangeColumnId): string {
  return RIEX_COLUMN_DEFS.find((c) => c.key === col)?.label ?? col;
}

function riexTrigger(column: state.RiExchangeColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `#ri-exchange-recommendations-list .column-filter-btn[data-column="${column}"]`,
  );
}

function riexOpenPopoverFor(column: state.RiExchangeColumnId, anchor: HTMLElement): void {
  openColumnFilterPopover({
    scope: RIEX_POPOVER_SCOPE,
    column,
    label: riexLabelFor(column),
    kind: RIEX_NUMERIC_COLUMNS.has(column) ? 'numeric' : 'categorical',
    placeholder: 'e.g. >50, 70..95, 1',
    distinctValues: sortedDistinctValues(riexState.currentRecommendations, (r) => riexCategoricalCellValue(r, column)),
    getFilter: () => state.getRiExchangeColumnFilters()[column],
    onCommit: (filter) => {
      state.setRiExchangeColumnFilter(column, filter);
      rerenderReshape();
    },
    findTrigger: () => riexTrigger(column),
  }, anchor);
}

export function rerenderReshape(): void {
  const container = document.getElementById('ri-exchange-recommendations-list');
  if (container) renderRecommendations(container);
}

// Empty-state copy for a loaded, non-empty RI list. Only RIs with known
// utilization can be said to meet the threshold (issue #1687).
function reshapeFleetCopy(): string {
  const total = riexState.currentRIs.length;
  const plural = total === 1 ? '' : 's';
  if (riexState.utilizationStatus === 'loading') return 'No reshape recommendations right now.';
  const unknown = riexState.currentRIs.filter(ri => !hasKnownUtilization(ri.reserved_instance_id)).length;
  if (unknown === 0) {
    return `All ${total} convertible RI${plural} meet your utilization threshold. No reshape needed.`;
  }
  return `No reshape recommendations right now. Utilization data is unavailable for ${unknown} of ${total} convertible RI${plural}, so ${unknown === 1 ? 'it was' : 'they were'} not checked against your utilization threshold.`;
}

export function renderRecommendations(container: HTMLElement): void {
  if (!riexState.currentRecommendations || riexState.currentRecommendations.length === 0) {
    // The "well-utilized" copy is only truthful when the RI fleet actually
    // contains convertibles to be utilized. Prior to this commit the same
    // message fired regardless of fleet presence, so a brand-new tenant saw
    // a claim about utilization of a non-existent fleet.
    // Until this scope's RI list is in (or when it failed to load), say
    // nothing about the fleet rather than quote a stale count (#1135).
    const copy = !riexState.currentRIsLoaded
      ? 'No reshape recommendations right now.'
      : riexState.currentRIs.length === 0
        ? 'Reshape recommendations appear once your accounts have active convertible Reserved Instances — none are registered yet.'
        : reshapeFleetCopy();
    container.innerHTML = `<p class="empty">${escapeHtml(copy)}</p>`;
    return;
  }

  // Issue #365: same admin-only gate as the convertible-RI table.
  const canExchange = canAccess('admin', '*');

  // Apply per-column filters before rendering. Each filter is ANDed with
  // the others; broken numeric expressions are skipped so the popover can
  // surface the error without forcing the user to clear the field first.
  const filters = state.getRiExchangeColumnFilters();
  const visible = applyRiExchangeColumnFilters(riexState.currentRecommendations, filters);

  const filterBtn = (column: state.RiExchangeColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = riexLabelFor(column);
    const label = filters[column] ? `Filter ${lbl} — currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${escapeHtml(label)}" title="${escapeHtml(label)}">${iconSvg('filter')}</button>`;
  };
  // Columns that pack two values ("4x m5.large", "26.6 / 64.0") have one
  // filter per value. Two bare funnels side by side gave no hint which was
  // which, so each one is named under the header label. The visible word is
  // part of the button's accessible name ("Filter Source count").
  const namedFilterBtn = (column: state.RiExchangeColumnId, name: string): string =>
    filterBtn(column).replace('</button>', `<span class="column-filter-btn-name">${name}</span></button>`);
  const filterGroup = (...btns: string[]): string => `<span class="th-filter-group">${btns.join('')}</span>`;

  // Render an indexable list so per-row Exchange buttons can find the
  // matching original recommendation (idx into riexState.currentRecommendations).
  // Index the full list once instead of an indexOf scan per visible row,
  // which made the row build quadratic (#319). The first index wins, as
  // with indexOf.
  const indexByRec = new Map<ReshapeRecommendation, number>();
  riexState.currentRecommendations.forEach((rec, i) => {
    if (!indexByRec.has(rec)) indexByRec.set(rec, i);
  });
  const visibleWithIdx = visible.map((rec) => ({
    rec,
    idx: indexByRec.get(rec) ?? -1,
  }));

  container.innerHTML = `
    <table class="table-compact${canExchange ? ' table-sticky-end' : ''}">
      <thead>
        <tr>
          <th>Source RI${filterBtn('source_ri_id')}</th>
          <th>Current${filterGroup(namedFilterBtn('source_count', 'count'), namedFilterBtn('source_instance_type', 'instance'))}</th>
          <th>Suggested${filterGroup(namedFilterBtn('target_count', 'count'), namedFilterBtn('target_instance_type', 'instance'))}</th>
          <th>Alternatives</th>
          <th>Utilization${filterBtn('utilization_percent')}</th>
          <th>Normalized Units${filterGroup(namedFilterBtn('normalized_used', 'used'), namedFilterBtn('normalized_purchased', 'purchased'))}</th>
          <th>Reason${filterBtn('reason')}</th>
          ${canExchange ? '<th>Actions</th>' : ''}
        </tr>
      </thead>
      <tbody>
        ${visibleWithIdx.length === 0 ? `<tr><td colspan="${canExchange ? 8 : 7}" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${visibleWithIdx.map(({ rec, idx }) => {
          // The API can omit utilization_percent (the filter extractor
          // already maps it to NaN). Unknown renders as a neutral em-dash:
          // calling toFixed on it threw and left the panel on its skeleton,
          // and undefined >= 70 is false, which painted unknown as red.
          const utilPct = typeof rec.utilization_percent === 'number' && Number.isFinite(rec.utilization_percent)
            ? rec.utilization_percent
            : null;
          const utilClass = utilPct === null ? '' : utilPct >= 95 ? 'util-green' : utilPct >= 70 ? 'util-yellow' : 'util-red';
          const altCell = renderAlternativesCell(rec.alternative_targets);
          return `
          <tr>
            <td class="monospace"><span class="cell-truncate cell-truncate--xs" title="${escapeHtml(rec.source_ri_id)}">${escapeHtml(rec.source_ri_id)}</span></td>
            <td>${rec.source_count}x ${escapeHtml(rec.source_instance_type)}</td>
            <td>${renderSuggestedCell(rec)}</td>
            <td class="cell-wrap">${altCell}</td>
            <td class="${utilClass}">${utilPct === null ? '—' : `${utilPct.toFixed(1)}%`}</td>
            <td>${rec.normalized_used.toFixed(1)} / ${rec.normalized_purchased.toFixed(1)}</td>
            <td class="cell-wrap">${escapeHtml(rec.reason)}</td>
            ${canExchange ? `<td>${renderReshapeExchangeButton(rec, idx)}</td>` : ''}
          </tr>`;
        }).join('')}
      </tbody>
    </table>
  `;

  // Wire per-column filter buttons.  e.stopPropagation prevents the
  // surrounding <th> from also handling the click (no sort handler today,
  // but matches the pattern in recommendations.ts so future <th>-level
  // handlers won't conflict).
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.RiExchangeColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      riexOpenPopoverFor(column, btn);
    });
  });
  // Keep an open filter popover attached to the re-rendered trigger.
  rebindColumnFilterPopover(RIEX_POPOVER_SCOPE);

  // Attach "Exchange" handlers. Same as renderRIsTable: for non-admin
  // sessions the selector matches zero elements and this loop is a no-op.
  container.querySelectorAll<HTMLButtonElement>('[data-action="fill-quote"]').forEach(btn => {
    btn.addEventListener('click', () => {
      const idx = parseInt(btn.dataset['index'] || '0', 10);
      const rec = riexState.currentRecommendations[idx];
      if (rec) fillQuoteFromRecommendation(rec);
    });
  });
}

// renderAlternativesCell formats the cross-family alternative targets
// as a comma-separated list of "<instance_type> $X.XX/mo" chips inside
// a single table cell. Each instance_type is escapeHtml'd; cost values
// go through the shared formatCurrency helper (digits: 2) and the
// currency prefix is escaped too, so there's no HTML injection vector.
function renderAlternativesCell(alternatives: OfferingOption[] | undefined): string {
  if (!alternatives || alternatives.length === 0) {
    return '—';
  }
  return alternatives
    .map((alt) => `<span class="cost-chip">${escapeHtml(alt.instance_type)} ${escapeHtml(formatOfferingCost(alt))}/mo</span>`)
    .join(' ');
}

// hasReshapeTarget reports whether a reshape recommendation names a primary
// exchange target. The analyzer emits target_instance_type "" and
// target_count 0 when it has none, for example for a completely unused RI
// or one whose target would exceed the units the account owns (#1410).
function hasReshapeTarget(rec: ReshapeRecommendation): boolean {
  return Boolean(rec.target_instance_type) && rec.target_count > 0;
}

// canExchangeReshape reports whether the row's Exchange button has anything
// to offer: a primary target, or cross-family alternatives the modal's picker
// can preselect from.
function canExchangeReshape(rec: ReshapeRecommendation): boolean {
  return hasReshapeTarget(rec) || (rec.alternative_targets?.length ?? 0) > 0;
}

function renderSuggestedCell(rec: ReshapeRecommendation): string {
  if (!hasReshapeTarget(rec)) {
    return '<span class="text-muted">No exchange target</span>';
  }
  return `${rec.target_count}x ${escapeHtml(rec.target_instance_type)}`;
}

// renderReshapeExchangeButton renders the row's Exchange action. A row with
// nothing to exchange into gets a disabled button with no data-action, so no
// click handler is attached and the reason is still discoverable on hover.
function renderReshapeExchangeButton(rec: ReshapeRecommendation, idx: number): string {
  if (!canExchangeReshape(rec)) {
    const why = 'No exchange target available for this RI';
    return `<button type="button" class="btn-small" disabled title="${why}" aria-label="Exchange: ${why}">Exchange</button>`;
  }
  return `<button type="button" class="btn-small" data-action="fill-quote" data-index="${idx}">Exchange</button>`;
}

function fillQuoteFromRecommendation(rec: ReshapeRecommendation): void {
  // With no primary target the modal opens on an empty target row for the
  // alternatives; a count of 0 would only fail validation.
  const count = hasReshapeTarget(rec) ? rec.target_count : 1;
  openExchangeModal(rec.source_ri_id, count, rec.target_instance_type, rec.alternative_targets);
}

export function fillQuoteFromRI(riId: string, count: number): void {
  openExchangeModal(riId, count);
}
