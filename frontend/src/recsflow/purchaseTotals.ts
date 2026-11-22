import { formatCurrency } from '../utils';
import { savingsPlanHourlySummary } from '../sp-capacity';
import { isSavingsPlanService } from '../lib/purchase-compatibility';
import { billsMonthly, buildCommitmentBand, commitmentLine, commitmentTotals, formatCommitmentTotals, type CommitmentLine } from '../commitmentSummary';
import type { LocalRecommendation } from '../types';
import { recsState } from './recsState';
import { effectiveMonthlySavings, weightedDisplaySavingsPct } from './money';

/**
 * Updates the totals row and Execute button disabled state based on the
 * current `recsState.checkedPurchaseIndices` set. Also syncs the select-all
 * checkbox's indeterminate/checked/unchecked state.
 *
 * Called on modal open and on every per-row or select-all checkbox change.
 *
 * The `selectAllCb` parameter is the select-all header checkbox element,
 * passed in so the live-update handler doesn't need to re-query it.
 */
export function updatePurchaseModalTotals(selectAllCb: HTMLInputElement): void {
  const totalsRow = document.getElementById('purchase-modal-totals-row');

  // Compute totals over checked indices only.
  let totalCount = 0;
  let totalUpfront = 0;
  let totalMonthlyCost = 0;
  let totalEffSavings = 0;
  const checkedRecs: LocalRecommendation[] = [];
  let hasMonthlyCostData = false;

  for (const idx of recsState.checkedPurchaseIndices) {
    const rec = recsState.currentPurchaseRecommendations[idx];
    if (!rec) continue;
    totalCount += rec.count;
    totalUpfront += rec.upfront_cost;
    const monthlyCost = rec.monthly_cost;
    if (monthlyCost != null) {
      totalMonthlyCost += monthlyCost;
      hasMonthlyCostData = true;
    }
    checkedRecs.push(rec);
    totalEffSavings += effectiveMonthlySavings(rec);
  }
  const avgEffPct = weightedDisplaySavingsPct(checkedRecs);

  // Rebuild the totals row DOM. Clear first.
  if (totalsRow) {
    while (totalsRow.firstChild) totalsRow.removeChild(totalsRow.firstChild);

    // Placeholder for the Include checkbox column.
    const tdBlank = document.createElement('td');
    tdBlank.textContent = '';
    totalsRow.appendChild(tdBlank);

    // Account column placeholder.
    const tdTotalLabel = document.createElement('td');
    const strong = document.createElement('strong');
    strong.textContent = 'Totals';
    tdTotalLabel.appendChild(strong);
    totalsRow.appendChild(tdTotalLabel);

    // Service / Type placeholder.
    const tdBlank2 = document.createElement('td');
    totalsRow.appendChild(tdBlank2);

    // Region placeholder.
    const tdBlank3 = document.createElement('td');
    totalsRow.appendChild(tdBlank3);

    // Count.
    const tdCount = document.createElement('td');
    tdCount.id = 'purchase-modal-total-count';
    const countStrong = document.createElement('strong');
    countStrong.textContent = String(totalCount);
    tdCount.appendChild(countStrong);
    totalsRow.appendChild(tdCount);

    // Upfront.
    const tdUpfront = document.createElement('td');
    tdUpfront.id = 'purchase-modal-total-upfront';
    const upfrontStrong = document.createElement('strong');
    upfrontStrong.textContent = formatCurrency(totalUpfront);
    tdUpfront.appendChild(upfrontStrong);
    totalsRow.appendChild(tdUpfront);

    // Monthly cost.
    const tdMonthly = document.createElement('td');
    tdMonthly.id = 'purchase-modal-total-monthly';
    const monthlyStrong = document.createElement('strong');
    monthlyStrong.textContent = hasMonthlyCostData ? formatCurrency(totalMonthlyCost) : '—';
    tdMonthly.appendChild(monthlyStrong);
    totalsRow.appendChild(tdMonthly);

    // Effective savings.
    const tdEffSav = document.createElement('td');
    tdEffSav.id = 'purchase-modal-total-eff-savings';
    tdEffSav.className = 'savings';
    const effSavStrong = document.createElement('strong');
    effSavStrong.textContent = formatCurrency(totalEffSavings);
    tdEffSav.appendChild(effSavStrong);
    totalsRow.appendChild(tdEffSav);

    // Effective % (weighted average over on-demand monthly).
    const tdEffPct = document.createElement('td');
    tdEffPct.id = 'purchase-modal-total-eff-pct';
    const effPctStrong = document.createElement('strong');
    if (avgEffPct !== null) {
      effPctStrong.textContent = avgEffPct.toFixed(1) + '%';
      if (avgEffPct < 0) tdEffPct.className = 'effective-pct-negative';
    } else {
      effPctStrong.textContent = '—';
    }
    tdEffPct.appendChild(effPctStrong);
    totalsRow.appendChild(tdEffPct);

    // Term and Payment column placeholders (editable per row, no aggregate).
    const tdBlankTerm = document.createElement('td');
    totalsRow.appendChild(tdBlankTerm);
    const tdBlankPayment = document.createElement('td');
    totalsRow.appendChild(tdBlankPayment);
  }

  // Update Execute button disabled state.
  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  if (executeBtn) {
    const noneSelected = recsState.checkedPurchaseIndices.size === 0;
    const submitting = executeBtn.dataset['submitting'] === 'true';
    executeBtn.disabled = submitting || noneSelected;
    executeBtn.title = submitting
      ? 'Purchase submission in progress'
      : noneSelected ? 'Select at least one purchase' : '';
  }

  // Sync select-all checkbox indeterminate/checked/unchecked state.
  const total = recsState.currentPurchaseRecommendations.length;
  const checked = recsState.checkedPurchaseIndices.size;
  if (checked === 0) {
    selectAllCb.checked = false;
    selectAllCb.indeterminate = false;
  } else if (checked === total) {
    selectAllCb.checked = true;
    selectAllCb.indeterminate = false;
  } else {
    selectAllCb.checked = false;
    selectAllCb.indeterminate = true;
  }

  const band = document.getElementById('purchase-modal-commitment-band');
  if (band) {
    band.replaceChildren(buildCommitmentBand(checkedPurchaseLines(), { upfrontLabel: 'Upfront' }));
    const checkedRecs = Array.from(recsState.checkedPurchaseIndices, (i) => recsState.currentPurchaseRecommendations[i]).filter((r): r is LocalRecommendation => !!r);
    const spHourly = savingsPlanHourlySummary(checkedRecs, recsState.currentPurchaseCapacityPercent, isSavingsPlanService);
    if (spHourly) {
      const note = document.createElement('p');
      note.className = 'purchase-modal-sp-hourly';
      note.textContent = spHourly;
      band.appendChild(note);
    }
  }

  // Issue #1903: the direct-execute warning's dollar total is derived from
  // recsState.checkedPurchaseIndices too, so it must refresh on every checkbox toggle,
  // not only when the radio itself changes.
  renderDirectExecuteWarning();
}

// renderDirectExecuteWarning rebuilds the "this will charge $X upfront
// immediately" callout from the currently checked rows. It is a money total
// in the same modal as the per-row cost cells (issue #1903), so it must
// follow both a checkbox toggle (updatePurchaseModalTotals, pre-existing
// gap) and a Term/Payment re-price, not only the radio's own change event.
// No-ops when the callout isn't in the DOM (no direct-execute permission).
function checkedPurchaseLines(): CommitmentLine[] {
  const lines: CommitmentLine[] = [];
  for (const idx of recsState.checkedPurchaseIndices) {
    const r = recsState.currentPurchaseRecommendations[idx];
    if (r) lines.push(commitmentLine(r));
  }
  return lines;
}

export function renderDirectExecuteWarning(): void {
  const directWarning = document.querySelector<HTMLElement>('#purchase-details .direct-execute-warning');
  if (!directWarning) return;
  directWarning.hidden = recsState.currentExecuteMode !== 'direct';
  if (recsState.currentExecuteMode !== 'direct') return;
  const lines = checkedPurchaseLines();
  const totals = commitmentTotals(lines);
  const shown = formatCommitmentTotals(lines);
  // Issue #711: a no-upfront purchase would otherwise read "charge $0.00".
  let recurring = '';
  if (totals.monthly === null) {
    recurring = billsMonthly(lines) ? ', plus monthly fees the provider did not report' : '';
  } else if (totals.monthly > 0) {
    recurring = `, then ${shown.monthly} a month for the term (${shown.termTotal} in total)`;
  }
  while (directWarning.firstChild) directWarning.removeChild(directWarning.firstChild);
  const icon = document.createElement('strong');
  icon.textContent = 'Warning: ';
  directWarning.appendChild(icon);
  const text = document.createTextNode(
    `This will charge ${shown.upfront} upfront immediately${recurring}. ` +
    'This bypasses the approval step.',
  );
  directWarning.appendChild(text);
}
