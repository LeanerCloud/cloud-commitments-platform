import * as state from '../state';
import { formatCurrency, escapeHtml, NO_VALUE } from '../utils';
import { serviceLabel } from '../lib/service-labels';
import { iconSvg } from '../icons';
import { savingsPlansBucketLabel } from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { formatTermShort } from '../lib/table-format';
import { expandedCells, expandedSpGroups, recsState } from './recsState';
import { providerDisplayName, providerBadgeClass } from './shared';
import { scaleCost, formatCostForPeriod, periodUnit, formatScaledRange } from './money';
import { cellSummary, formatSavingsRange, formatTermRange } from './ranges';
import { type ColumnDef, COLUMN_DEFS, getColumnLabel } from './columnDefs';
import { TOGGLEABLE_COLUMN_KEYS } from './columnPrefs';
import { groupRecsByCell, groupSpCellKeys, sortVariantsInCell, pickBestVariantPerCell } from './groups';
import { groupsInSortOrder, sortIndicator } from './sort';
import { buildVariantRowMarkup, cellIdentityLabel } from './cells';

export function buildListMarkup(
  recommendations: LocalRecommendation[],
  selectedRecs: ReadonlySet<string>,
  visibleCols: readonly ColumnDef[] = COLUMN_DEFS,
  showCheckboxes = true,
): string {
  const sort = state.getRecommendationsSort();
  const filters = state.getRecommendationsColumnFilters();
  const period = state.getCostPeriod();
  const filterBtn = (column: state.RecommendationsColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = getColumnLabel(column, period);
    const label = filters[column] ? `Filter ${lbl} \u2014 currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${label}" title="${label}">${iconSvg('filter')}</button>`;
  };
  const colHeader = (col: ColumnDef): string => {
    const lbl = getColumnLabel(col.key, period);
    // Visual-only columns (e.g. usage_history sparkline) are not sortable and
    // have no column-filter button; render a plain non-interactive <th>.
    if (col.sortable === false) {
      return `<th>${lbl}</th>`;
    }
    // Issue #755: the <th> stays a plain column header (exposing aria-sort)
    // and holds two sibling buttons, sort and filter. A focusable
    // role=button <th> wrapping the filter button made Enter on the filter
    // bubble into the sort handler, and nested the two controls.
    // Money and count columns right-align so digits line up (issue #739).
    const numClass = col.kind === 'numeric' ? ' num' : '';
    const ariaSort = sort.column === col.key
      ? ` aria-sort="${sort.direction === 'asc' ? 'ascending' : 'descending'}"`
      : '';
    // .th-inner is a nowrap flex row: when the table is squeezed the sort
    // label wraps inside its button and the funnel stays in the cell.
    return `<th class="sortable${numClass}" data-sort="${col.key}"${ariaSort}><span class="th-inner"><button type="button" class="sort-btn"><span>${lbl}</span>${sortIndicator(col.key, sort.column, sort.direction)}</button>${filterBtn(col.key)}</span></th>`;
  };

  // issues #225 + #226: group by cell, sort groups, then render.
  const groups = groupRecsByCell(recommendations);
  // Issue #768: sort order is selection-independent; selectedRecs is used only
  // for per-row rendering (checked state, CSS class) and the summary cards.
  const sortedKeys = groupsInSortOrder(groups, sort);

  // Update module-level group key cache for the Expand-All button.
  // Only multi-variant cells count — single-variant cells render flat with no chevron.
  recsState.lastVisibleGroupKeys = sortedKeys.filter((k) => (groups.get(k)?.length ?? 0) > 1);

  // The "Recommended range" banner that used to live here was redundant
  // with the Potential Monthly Savings card after #272 / #279 brought
  // the same range to the summary header. Removed (closes #278).

  // Summary row colspan: the big cell covers resource_type (always shown) + all
  // visible toggleable columns. The 3 fixed identity cells (provider, account,
  // service) always render separately, so this colspan = 1 + visible toggleable count.
  const visibleToggleableCols = visibleCols.filter((c) => TOGGLEABLE_COLUMN_KEYS.has(c.key));
  const summaryColspan = 1 + visibleToggleableCols.length;
  const visibleKeys = new Set(visibleCols.map((c) => c.key));

  // issue #135: build SP plan-type group map for visual grouping.
  // spCellGroups maps spGroupKey -> cellKey[] for scopes with 2+ SP cell keys.
  const spCellGroups = groupSpCellKeys(sortedKeys, groups);
  // Reverse index: cellKey -> spGroupKey, for O(1) lookup during row iteration.
  const cellKeyToSpGroup = new Map<string, string>();
  for (const [sgk, cellKeys] of spCellGroups) {
    for (const ck of cellKeys) cellKeyToSpGroup.set(ck, sgk);
  }
  // Track which SP groups have already had their parent row emitted.
  const renderedSpGroups = new Set<string>();

  // Build tbody rows: grouped for multi-variant cells, flat for single-variant.
  // SP groups get an additional parent row that wraps the per-plan-type cells.
  const rows: string[] = [];
  for (const key of sortedKeys) {
    const variants = groups.get(key)!;
    const sgk = cellKeyToSpGroup.get(key);

    if (sgk !== undefined) {
      // This cell key belongs to an SP group with 2+ plan types.
      if (renderedSpGroups.has(sgk)) {
        // Already rendered the parent row on a previous iteration; skip.
        continue;
      }
      renderedSpGroups.add(sgk);

      // Render the SP group parent row, then (if expanded) all child cell rows.
      const childCellKeys = spCellGroups.get(sgk)!;
      const spRep = groups.get(childCellKeys[0]!)![0]!;
      const sgAccountName = spRep.cloud_account_id
        ? (recsState.accountNamesCache.get(spRep.cloud_account_id) || spRep.cloud_account_id)
        : '\u2014';
      const isSpExpanded = expandedSpGroups.has(sgk);
      const spChevron = iconSvg(isSpExpanded ? 'chevron-down' : 'chevron-right');
      const spSlugs = childCellKeys.map((ck) => groups.get(ck)![0]!.service);
      const spLabel = savingsPlansBucketLabel(spSlugs);
      const planTypeCount = childCellKeys.length;
      // Aggregate savings: sum the best-variant savings across all child cells.
      const sfxLabel = periodUnit(period);
      let spSavingsTotal = 0;
      for (const ck of childCellKeys) {
        const cv = groups.get(ck)!;
        spSavingsTotal += Math.max(...cv.map((r) => r.savings));
      }
      // formatCostForPeriod scales internally; passing the already-scaled
      // scaledSpSavings here double-applied the period factor (issue #252).
      const spSavingsText = `${formatCostForPeriod(spSavingsTotal, period)}${sfxLabel}`;
      const sgkAttr = escapeHtml(sgk);
      rows.push(`
  <tr class="rec-sp-group-row" data-sp-group-key="${sgkAttr}">
    <td class="checkbox-col">
      <button type="button" class="rec-sp-group-chevron" data-sp-group-key="${sgkAttr}" aria-expanded="${isSpExpanded}" aria-label="${isSpExpanded ? 'Collapse' : 'Expand'} Savings Plans plan types">
        ${spChevron}
      </button>
    </td>
    <td><span class="provider-badge ${providerBadgeClass(spRep.provider)}">${escapeHtml(providerDisplayName(spRep.provider))}</span></td>
    <td>${escapeHtml(sgAccountName)}</td>
    <td><span class="service-badge rec-sp-group-badge">${escapeHtml(spLabel)} <span class="rec-sp-plan-count">+${planTypeCount} plan types</span></span></td>
    <td colspan="${summaryColspan}" class="rec-sp-group-summary">
      <span class="rec-sp-group-savings">${spSavingsText}</span>
    </td>
  </tr>`);

      if (!isSpExpanded) continue;

      // Expanded: render each child cell (possibly itself multi-variant).
      for (const ck of childCellKeys) {
        const childVariants = groups.get(ck)!;
        if (childVariants.length === 1) {
          rows.push(buildVariantRowMarkup(childVariants[0]!, selectedRecs, 'sp-child', visibleCols, showCheckboxes));
          continue;
        }

        // Multi-variant child cell inside an expanded SP group.
        const isCellExpanded = expandedCells.has(ck);
        const childSummary = cellSummary(childVariants);
        const childRep = childVariants[0]!;
        const childAccountName = childRep.cloud_account_id
          ? (recsState.accountNamesCache.get(childRep.cloud_account_id) || childRep.cloud_account_id)
          : '\u2014';
        const selectedChildVariant = childVariants.find((r) => selectedRecs.has(r.id));
        const scaledChildSavingsMin = scaleCost(childSummary.savingsMin, period) ?? childSummary.savingsMin;
        const scaledChildSavingsMax = scaleCost(childSummary.savingsMax, period) ?? childSummary.savingsMax;
        const childSavingsDisplay = selectedChildVariant
          ? `${formatCostForPeriod(selectedChildVariant.savings, period)}${sfxLabel} <span class="rec-variants-count">(+${childVariants.length - 1} variants)</span>`
          : `${formatScaledRange(scaledChildSavingsMin, scaledChildSavingsMax, period)}${sfxLabel}`;
        const childUpfrontDisplay = selectedChildVariant
          ? formatCurrency(selectedChildVariant.upfront_cost)
          : formatSavingsRange(childSummary.upfrontMin, childSummary.upfrontMax);
        const childTermDisplay = selectedChildVariant
          ? formatTermShort(selectedChildVariant.term) || NO_VALUE
          : formatTermRange(childSummary.termMin, childSummary.termMax);
        const cellChevron = iconSvg(isCellExpanded ? 'chevron-down' : 'chevron-right');
        const childIdentityParts = [cellIdentityLabel(childRep)];
        if (visibleKeys.has('region')) childIdentityParts.push(escapeHtml(childRep.region));
        childIdentityParts.push(`${childVariants.length} variants`);
        const childRangeParts: string[] = [];
        if (visibleKeys.has('savings')) childRangeParts.push(childSavingsDisplay);
        if (visibleKeys.has('upfront_cost')) childRangeParts.push(`upfront: ${childUpfrontDisplay}`);
        if (visibleKeys.has('term')) childRangeParts.push(`term: ${childTermDisplay}`);
        const ckAttr = escapeHtml(ck);
        rows.push(`
  <tr class="rec-cell-summary-row rec-sp-group-child" data-cell-key="${ckAttr}">
    <td class="checkbox-col">
      <button type="button" class="rec-cell-chevron" data-cell-key="${ckAttr}" aria-expanded="${isCellExpanded}" aria-label="${isCellExpanded ? 'Collapse' : 'Expand'} cell variants">
        ${cellChevron}
      </button>
    </td>
    <td><span class="provider-badge ${providerBadgeClass(childRep.provider)}">${escapeHtml(providerDisplayName(childRep.provider))}</span></td>
    <td>${escapeHtml(childAccountName)}</td>
    <td><span class="service-badge">${escapeHtml(serviceLabel(childRep.service))}</span></td>
    <td colspan="${summaryColspan}" class="rec-cell-summary-content">
      <div class="rec-cell-summary-inner">
        <span class="rec-cell-identity">${childIdentityParts.filter(Boolean).join(' &mdash; ')}</span>
        ${childRangeParts.length > 0 ? `<span class="rec-cell-range">${childRangeParts.join(' &middot; ')}</span>` : ''}
      </div>
    </td>
  </tr>`);
        if (isCellExpanded) {
          const sortedChildVariants = sortVariantsInCell(childVariants);
          for (const v of sortedChildVariants) {
            rows.push(buildVariantRowMarkup(v, selectedRecs, 'variant', visibleCols, showCheckboxes));
          }
        }
      }
      continue;
    }

    // Non-SP cell (or SP singleton scope -- only one plan type in scope).
    if (variants.length === 1) {
      // Single-variant: render flat, no group header, no indent.
      rows.push(buildVariantRowMarkup(variants[0]!, selectedRecs, 'flat', visibleCols, showCheckboxes));
      continue;
    }

    // Multi-variant cell (non-SP).
    const isExpanded = expandedCells.has(key);
    const summary = cellSummary(variants);
    const rep = variants[0]!;
    const accountName = rep.cloud_account_id ? (recsState.accountNamesCache.get(rep.cloud_account_id) || rep.cloud_account_id) : '\u2014';

    // Selected variant in this cell (if any) -- used to show selected values on summary row.
    const selectedVariant = variants.find((r) => selectedRecs.has(r.id));

    // Summary row savings display: selected variant value if one is selected;
    // otherwise the range across all variants.
    // issue #319: scale savings by the active period.
    const sfxLabel = periodUnit(period);
    const scaledSavingsMin = scaleCost(summary.savingsMin, period) ?? summary.savingsMin;
    const scaledSavingsMax = scaleCost(summary.savingsMax, period) ?? summary.savingsMax;
    const savingsDisplay = selectedVariant
      ? `${formatCostForPeriod(selectedVariant.savings, period)}${sfxLabel} <span class="rec-variants-count">(+${variants.length - 1} variants)</span>`
      : `${formatScaledRange(scaledSavingsMin, scaledSavingsMax, period)}${sfxLabel}`;

    const upfrontDisplay = selectedVariant
      ? formatCurrency(selectedVariant.upfront_cost)
      : formatSavingsRange(summary.upfrontMin, summary.upfrontMax);

    const termDisplay = selectedVariant
      ? formatTermShort(selectedVariant.term) || NO_VALUE
      : formatTermRange(summary.termMin, summary.termMax);

    const chevron = iconSvg(isExpanded ? 'chevron-down' : 'chevron-right');
    const identityParts = [cellIdentityLabel(rep)];
    if (visibleKeys.has('region')) {
      identityParts.push(escapeHtml(rep.region));
    }
    identityParts.push(`${variants.length} variants`);

    const rangeParts: string[] = [];
    if (visibleKeys.has('savings')) {
      rangeParts.push(savingsDisplay);
    }
    if (visibleKeys.has('upfront_cost')) {
      rangeParts.push(`upfront: ${upfrontDisplay}`);
    }
    if (visibleKeys.has('term')) {
      rangeParts.push(`term: ${termDisplay}`);
    }

    const chevronButton = `<button type="button" class="rec-cell-chevron" data-cell-key="${escapeHtml(key)}" aria-expanded="${isExpanded}" aria-label="${isExpanded ? 'Collapse' : 'Expand'} cell variants">
        ${chevron}
      </button>`;
    // Issue #1006: the expand chevron always lives in the leading td.checkbox-col,
    // before the Provider column, for all roles including readonly viewers.
    // This matches the SP-group rows (which always emitted a leading cell) and
    // the owner decision to keep the control at the table's far-left edge.
    const chevronCell = `<td class="checkbox-col">${chevronButton}</td>`;

    rows.push(`
  <tr class="rec-cell-summary-row" data-cell-key="${escapeHtml(key)}">
    ${chevronCell}
    <td><span class="provider-badge ${providerBadgeClass(rep.provider)}">${escapeHtml(providerDisplayName(rep.provider))}</span></td>
    <td>${escapeHtml(accountName)}</td>
    <td><span class="service-badge">${escapeHtml(serviceLabel(rep.service))}</span></td>
    <td colspan="${summaryColspan}" class="rec-cell-summary-content">
      <div class="rec-cell-summary-inner">
        <span class="rec-cell-identity">${identityParts.filter(Boolean).join(' &mdash; ')}</span>
        ${rangeParts.length > 0 ? `<span class="rec-cell-range">${rangeParts.join(' &middot; ')}</span>` : ''}
      </div>
    </td>
  </tr>`);

    if (isExpanded) {
      const sortedVariants = sortVariantsInCell(variants);
      for (const v of sortedVariants) {
        rows.push(buildVariantRowMarkup(v, selectedRecs, 'variant', visibleCols, showCheckboxes));
      }
    }
  }

  // Issue #479: select-all header checkbox renders as a proper tri-state
  // reflecting current selection vs. the set of best-variant-per-cell
  // recommendations (the set that selectAll's onChange actually populates,
  // see openColumnPopover wiring + #224). Indeterminate is set via JS in
  // renderRecommendationsList's post-render hook because HTML attributes
  // can't express the indeterminate state.
  // Issue #869: skip the tri-state computation entirely for viewer sessions
  // to avoid dead-code paths when showCheckboxes is false.
  // Issue #1006: the leading th.checkbox-col is always present (empty for viewers)
  // so the column aligns with the chevron cells in every row type.
  let checkboxColHeader: string;
  if (showCheckboxes) {
    const bestVariants = pickBestVariantPerCell(recommendations);
    const bestVariantIds = new Set(bestVariants.map((r) => r.id));
    let selectedBestCount = 0;
    selectedRecs.forEach((id) => { if (bestVariantIds.has(id)) selectedBestCount++; });
    const allSelected = bestVariants.length > 0 && selectedBestCount === bestVariants.length;
    const selectAllCheckedAttr = allSelected ? ' checked' : '';
    const selectAllIndeterminate = selectedBestCount > 0 && selectedBestCount < bestVariants.length;
    const selectAllDataIndeterminate = ` data-indeterminate="${selectAllIndeterminate ? 'true' : 'false'}"`;
    checkboxColHeader = `<th class="checkbox-col"><input type="checkbox" id="select-all-recs" aria-label="Select all recommendations"${selectAllCheckedAttr}${selectAllDataIndeterminate}></th>`;
  } else {
    checkboxColHeader = `<th class="checkbox-col"></th>`;
  }

  return `
    <table class="table-compact table-sticky-start">
      <thead>
        <tr>
          ${checkboxColHeader}
          ${visibleCols.map((c) => colHeader(c)).join('')}
        </tr>
      </thead>
      <tbody>
        ${rows.join('')}
      </tbody>
    </table>
  `;
}
