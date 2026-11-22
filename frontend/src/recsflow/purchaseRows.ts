import { formatCurrency, formatTerm } from '../utils';
import { serviceLabel } from '../lib/service-labels';
import { showToast } from '../toast';
import { recsState } from './recsState';
import { effectiveMonthlySavings, displaySavingsPct } from './money';
import type { BulkPurchasePayment } from './bulkPrefs';
import { pricedCellVariant, cellTermOptions, cellPaymentOptions } from './groups';
import { updatePurchaseModalTotals } from './purchaseTotals';

// renderPurchaseModalRow builds one editable <tr> for the per-row
// purchase modal. Idx is the index into recsState.currentPurchaseRecommendations
// — change handlers locate the live rec via that index so subsequent
// edits to other rows don't stale-close over an outdated array
// reference. The modal does NOT re-render mid-edit; only the row's own
// Payment <select> options are rebuilt on a Term change.
//
// Issue #320: adds Include checkbox (col 0), Account (col 1), and
// Upfront/Monthly Cost/Eff. Savings/Eff. % columns before Term/Payment.
// The checkbox change handler updates recsState.checkedPurchaseIndices and calls
// updatePurchaseModalTotals so the live totals row and Execute button
// state stay in sync.
export function renderPurchaseModalRow(idx: number, paymentSource: 'override' | 'rec' | 'fallback'): HTMLTableRowElement {
  const rec = recsState.currentPurchaseRecommendations[idx]!;
  const tr = document.createElement('tr');
  tr.dataset['recIdx'] = String(idx);

  // Issue #320: Include checkbox (col 0).
  const includeTd = document.createElement('td');
  const includeCb = document.createElement('input');
  includeCb.type = 'checkbox';
  includeCb.className = 'purchase-modal-row-include';
  includeCb.checked = recsState.checkedPurchaseIndices.has(idx);
  includeCb.setAttribute('aria-label', `Include row ${idx + 1}`);
  includeTd.appendChild(includeCb);
  tr.appendChild(includeTd);

  // Account (col 1): display name from recsState.accountNamesCache, fallback to ID.
  const accountTd = document.createElement('td');
  const accountName = rec.cloud_account_id
    ? (recsState.accountNamesCache.get(rec.cloud_account_id) || rec.cloud_account_id)
    : '—';
  accountTd.appendChild(document.createTextNode(accountName));
  tr.appendChild(accountTd);

  // Service / Type (col 2): service + resource_type combined.
  const serviceTypeTd = document.createElement('td');
  serviceTypeTd.appendChild(document.createTextNode(serviceLabel(rec.service)));
  if (rec.resource_type) {
    const typeSpan = document.createElement('span');
    typeSpan.className = 'purchase-modal-resource-type';
    typeSpan.appendChild(document.createTextNode(' / ' + rec.resource_type));
    serviceTypeTd.appendChild(typeSpan);
  }
  tr.appendChild(serviceTypeTd);

  // Region (col 3).
  const regionCell = document.createElement('td');
  regionCell.appendChild(document.createTextNode(rec.region));
  tr.appendChild(regionCell);

  // Count (col 4).
  const countCell = document.createElement('td');
  countCell.appendChild(document.createTextNode(String(rec.count)));
  tr.appendChild(countCell);

  // Upfront (col 5): upfront_cost is already the total for all count units.
  const upfrontTd = document.createElement('td');
  upfrontTd.appendChild(document.createTextNode(formatCurrency(rec.upfront_cost)));
  tr.appendChild(upfrontTd);

  // Monthly cost (col 6): null when provider API didn't return it.
  const monthlyCostTd = document.createElement('td');
  monthlyCostTd.appendChild(
    document.createTextNode(rec.monthly_cost != null ? formatCurrency(rec.monthly_cost) : '—'),
  );
  tr.appendChild(monthlyCostTd);

  // Effective monthly savings (col 7): reuses effectiveMonthlySavings helper.
  // savings and upfront_cost are already total-for-rec values, so the helper
  // returns the aggregate effective savings for all count units.
  const effSavTd = document.createElement('td');
  effSavTd.className = 'savings';
  effSavTd.appendChild(document.createTextNode(formatCurrency(effectiveMonthlySavings(rec))));
  tr.appendChild(effSavTd);

  // Effective % (col 8): reuses displaySavingsPct (provider % preferred,
  // falls back to effectiveSavingsPct).
  const effPctTd = document.createElement('td');
  const pct = displaySavingsPct(rec);
  if (pct !== null && pct < 0) effPctTd.className = 'effective-pct-negative';
  effPctTd.appendChild(document.createTextNode(pct !== null ? pct.toFixed(1) + '%' : '—'));
  tr.appendChild(effPctTd);

  // Term select (col 9). Options are the terms actually loaded for this
  // cell (issue #1903) — an unpriced term is never offered.
  const termCell = document.createElement('td');
  const termSelect = document.createElement('select');
  termSelect.className = 'purchase-row-term';
  for (const t of cellTermOptions(rec)) {
    const opt = document.createElement('option');
    opt.value = String(t);
    opt.textContent = formatTerm(t);
    if (t === rec.term) opt.selected = true;
    termSelect.appendChild(opt);
  }
  termCell.appendChild(termSelect);
  tr.appendChild(termCell);

  // Payment select (col 10). Options are restricted to the (term, payment)
  // combinations actually loaded for this cell (issue #1903), so the user
  // can never pick a combo the API never priced.
  const paymentCell = document.createElement('td');
  const paymentSelect = document.createElement('select');
  paymentSelect.className = 'purchase-row-payment';
  rebuildPaymentOptions(paymentSelect, cellPaymentOptions(rec, rec.term as 1 | 3, recsState.currentPurchaseCapacityPercent), (rec.payment ?? '') as BulkPurchasePayment | '');
  paymentCell.appendChild(paymentSelect);
  if (paymentSource === 'override') {
    const sourceNote = document.createElement('span');
    sourceNote.className = 'purchase-row-payment-source';
    sourceNote.textContent = ' (from account override)';
    paymentCell.appendChild(sourceNote);
  }
  tr.appendChild(paymentCell);

  // Issue #320: Include checkbox change handler.
  // Must query the select-all checkbox from the DOM each time because
  // the handler outlives the initial render call.
  includeCb.addEventListener('change', () => {
    if (includeCb.checked) {
      recsState.checkedPurchaseIndices.add(idx);
    } else {
      recsState.checkedPurchaseIndices.delete(idx);
    }
    const selectAllCb = document.getElementById('purchase-modal-select-all') as HTMLInputElement | null;
    if (selectAllCb) updatePurchaseModalTotals(selectAllCb);
  });

  // Issue #1903: swap in the loaded sibling variant for the selected
  // (term, payment) and re-render the whole row so the cost cells, the
  // Include checkbox, and both selects stay derived from one source. Term
  // and Payment changes both funnel through this so neither can leave the
  // row's price stale.
  const applyVariantChange = (focusSelector: string): void => {
    const term = parseInt(termSelect.value, 10) === 3 ? 3 : 1;
    const payment = paymentSelect.value as BulkPurchasePayment;
    const live = recsState.currentPurchaseRecommendations[idx];
    if (!live) return;
    const variant = pricedCellVariant(live, term, payment, recsState.currentPurchaseCapacityPercent);
    if (!variant) {
      // Reachable: a sibling variant with a smaller count can floor to 0 at
      // the modal's capacity. Keep the priced rec and put the selects back.
      showToast({
        message: `No priced ${formatTerm(term)} / ${payment} option for this row at ${recsState.currentPurchaseCapacityPercent}% capacity.`,
        kind: 'warning',
      });
      termSelect.value = String(live.term);
      rebuildPaymentOptions(paymentSelect, cellPaymentOptions(live, live.term as 1 | 3, recsState.currentPurchaseCapacityPercent), (live.payment ?? '') as BulkPurchasePayment | '');
      return;
    }
    recsState.currentPurchaseRecommendations[idx] = { ...variant, payment };
    const fresh = renderPurchaseModalRow(idx, paymentSource);
    tr.replaceWith(fresh);
    fresh.querySelector<HTMLSelectElement>(focusSelector)?.focus();
    const selectAllCb = document.getElementById('purchase-modal-select-all') as HTMLInputElement | null;
    if (selectAllCb) updatePurchaseModalTotals(selectAllCb);
  };

  termSelect.addEventListener('change', () => {
    const live = recsState.currentPurchaseRecommendations[idx];
    if (!live) return;
    const newTerm = parseInt(termSelect.value, 10) === 3 ? 3 : 1;
    // Rebuild this row's payment options for the new term before applying —
    // applyVariantChange reads paymentSelect.value, so it must already
    // reflect the new term's loaded options.
    rebuildPaymentOptions(paymentSelect, cellPaymentOptions(live, newTerm, recsState.currentPurchaseCapacityPercent), (live.payment ?? '') as BulkPurchasePayment | '');
    applyVariantChange('.purchase-row-term');
  });

  paymentSelect.addEventListener('change', () => applyVariantChange('.purchase-row-payment'));

  return tr;
}

// rebuildPaymentOptions clears and re-populates a <select> with the given
// Payment options. If `desired` is in the new option set, it stays
// selected; otherwise the first option wins and the select's `.value`
// reflects that. Issue #1903: options are passed in by the caller
// (cellPaymentOptions — loaded variants only) rather than derived here from
// the full compat table, so an unpriced combination can never be offered.
function rebuildPaymentOptions(
  select: HTMLSelectElement,
  options: readonly BulkPurchasePayment[],
  desired: BulkPurchasePayment | '',
): void {
  while (select.firstChild) select.removeChild(select.firstChild);
  let matched = false;
  for (const opt of options) {
    const o = document.createElement('option');
    o.value = opt;
    o.textContent = opt;
    if (opt === desired) {
      o.selected = true;
      matched = true;
    }
    select.appendChild(o);
  }
  if (!matched && options.length > 0) {
    select.value = options[0]!;
  }
}
