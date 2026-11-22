import { serviceLabel } from '../lib/service-labels';
import {
  isPaymentSupported,
  type Payment as CompatPayment,
  type Provider as CompatProvider,
} from '../lib/purchase-compatibility';
import { executeButtonLabel } from '../commitmentSummary';
import type { AccountServiceOverride } from '../api/accounts';
import type { LocalRecommendation } from '../types';
import { openModal } from '../modal';
import { canAccess } from '../permissions';
import { fetchOverridesForAccounts } from '../lib/overrides';
import { clearPurchaseModalRecommendations, recsState } from './recsState';
import { normalizeBulkPayment } from './bulkPrefs';
import { pricedCellVariant, cellPaymentOptions } from './groups';
import { clearFanOutBuckets, resetExecuteModeAndLabel } from './fanOutState';
import { dismissOverrideWarning, warnOverrideFetchFailed } from './fanOutModal';
import { renderDirectExecuteWarning, updatePurchaseModalTotals } from './purchaseTotals';
import { renderPurchaseModalRow } from './purchaseRows';

// Resolve a priced override, valid own payment, or priced legacy fallback.
// A payment label alone cannot establish the price of a legacy row.
function resolvePerRecPaymentSeed(
  rec: LocalRecommendation,
  overridesByAccount: Map<string, AccountServiceOverride[]>,
  capacityPercent: number,
): { payment: CompatPayment; source: 'override' | 'rec' | 'fallback'; variant: LocalRecommendation } | null {
  const provider = rec.provider as CompatProvider;
  const term = rec.term as 1 | 3;

  if (rec.cloud_account_id) {
    const overrides = overridesByAccount.get(rec.cloud_account_id);
    if (overrides) {
      const match = overrides.find(
        (o) => o.provider === provider && o.service === rec.service,
      );
      // Issue #1903: an override is only honoured when a priced variant for
      // it was actually loaded — otherwise the override would relabel this
      // row's payment without the matching price.
      const overridePayment = normalizeBulkPayment(match?.payment);
      if (overridePayment && isPaymentSupported(provider, rec.service, term, overridePayment)) {
        const variant = pricedCellVariant(rec, term, overridePayment, capacityPercent);
        if (variant) return { payment: overridePayment, source: 'override', variant };
      }
    }
  }

  const ownPayment = normalizeBulkPayment(rec.payment);
  if (ownPayment && isPaymentSupported(provider, rec.service, term, ownPayment)) {
    return { payment: ownPayment, source: 'rec', variant: rec };
  }

  const options = cellPaymentOptions(rec, term, capacityPercent);
  const preferred = normalizeBulkPayment(recsState.cachedGlobalDefaultPayment);
  if (preferred && options.includes(preferred)) {
    options.splice(options.indexOf(preferred), 1);
    options.unshift(preferred);
  }
  for (const payment of options) {
    const variant = pricedCellVariant(rec, term, payment, capacityPercent);
    if (variant) return { payment, source: 'fallback', variant };
  }
  return null;
}

/**
 * Open the single-bucket purchase modal with editable per-row Term and
 * Payment dropdowns (issue #111 sub-option (iii)).
 *
 * Expanded by issue #320 to also show:
 *   - Per-row "Include" checkboxes (all checked by default) with a
 *     select-all/deselect-all header checkbox.
 *   - Account, Upfront, Monthly Cost, Effective Savings, and Effective %
 *     columns so the user can verify the full breakdown before committing.
 *   - A live totals row that updates as checkboxes toggle.
 *   - Execute Purchase disabled when no rows are checked.
 *
 * Only checked rows are submitted: `getPurchaseModalRecommendations()`
 * filters by `recsState.checkedPurchaseIndices` so the existing execute-purchase
 * code path in app.ts needs no changes.
 *
 * Modal shows monthly totals regardless of the page's cost-period selector
 * — by design (the modal is a commit-decision context where monthly is
 * canonical).
 *
 * Defaults are seeded by resolvePerRecPaymentSeed:
 *   priced override, valid own payment, then a priced legacy fallback.
 *   Rows without a priced payment are explicitly excluded.
 *
 * On change, handlers mutate `recsState.currentPurchaseRecommendations[idx]` in
 * place so `getPurchaseModalRecommendations()` returns the user's
 * edits, and `app.ts::handleExecutePurchase` posts those values per row
 * (replacing the historical hardcoded `'all-upfront'` on that path).
 *
 * Async because it pre-fetches per-account overrides — same pattern as
 * `openFanOutModal`. A failed override fetch shows one warning toast and
 * seeds from the rec's own valid payment or a loaded priced alternative, so
 * the account's configured override is not applied in that case.
 */
export async function openPurchaseModal(recommendations: LocalRecommendation[], capacityPercent = 100): Promise<void> {
  const openToken = ++recsState.purchaseModalOpenToken;
  clearFanOutBuckets();
  recsState.currentPurchaseCapacityPercent = capacityPercent;
  recsState.currentPurchaseRecommendations = [];
  const pendingRows = recsState.currentPurchaseRecommendations;
  recsState.checkedPurchaseIndices = new Set();
  recsState.checkedPurchaseModalInitialised = true;

  const container = document.getElementById('purchase-details');
  if (!container) return;

  // Pre-fetch overrides for every distinct non-empty cloud_account_id
  // in the input set. One fetch per account, parallel via Promise.all,
  // cached in a per-call Map.
  const accountIDs = new Set<string>();
  for (const r of recommendations) {
    if (r.cloud_account_id) accountIDs.add(r.cloud_account_id);
  }
  const { byAccount: overridesByAccount, failedIds } = await fetchOverridesForAccounts(accountIDs);

  if (openToken !== recsState.purchaseModalOpenToken || recsState.currentPurchaseRecommendations !== pendingRows) return;
  warnOverrideFetchFailed(failedIds);

  const seeds = recommendations.map((r) => resolvePerRecPaymentSeed(r, overridesByAccount, capacityPercent));
  const resolvedSeeds = seeds.filter((seed) => seed !== null);
  recsState.currentPurchaseRecommendations = resolvedSeeds.map((seed) => ({ ...seed.variant, payment: seed.payment }));
  recsState.checkedPurchaseIndices = new Set(recsState.currentPurchaseRecommendations.map((_, i) => i));

  while (container.firstChild) container.removeChild(container.firstChild);

  const unavailable = recommendations.filter((_, i) => seeds[i] === null);
  if (unavailable.length > 0) {
    const notice = document.createElement('div');
    notice.className = 'purchase-modal-unavailable';
    notice.setAttribute('role', 'alert');
    notice.textContent = 'These recommendations are unavailable and excluded: no priced payment is available for their term and selected capacity.';
    const list = document.createElement('ul');
    for (const rec of unavailable) {
      const item = document.createElement('li');
      item.textContent = [rec.cloud_account_id, rec.provider, serviceLabel(rec.service), rec.resource_type, rec.region, `${rec.term}-year`].filter(Boolean).join(' / ');
      list.appendChild(item);
    }
    notice.appendChild(list);
    container.appendChild(notice);
  }

  // Reset the execute mode for this modal session so a prior direct-execute
  // choice does not carry over to a freshly opened modal (issue #289).
  resetExecuteModeAndLabel();

  // Execute-mode toggle (issue #289): shown only to sessions with
  // execute-any:purchases or execute-own:purchases. Everyone else sees only
  // the approval-required note with no toggle.
  const canDirectExecute =
    canAccess('execute-any', 'purchases') || canAccess('execute-own', 'purchases');

  if (canDirectExecute) {
    // Toggle section: "How would you like to handle this purchase?"
    const toggleSection = document.createElement('div');
    toggleSection.className = 'form-section execute-mode-toggle';

    const toggleLabel = document.createElement('p');
    toggleLabel.className = 'execute-mode-label';
    toggleLabel.textContent = 'How would you like to handle this purchase?';
    toggleSection.appendChild(toggleLabel);

    const radioGroup = document.createElement('div');
    radioGroup.className = 'execute-mode-radio-group';
    radioGroup.setAttribute('role', 'radiogroup');
    radioGroup.setAttribute('aria-label', 'Purchase execution mode');

    // Option 1: Send for approval (default)
    const approvalRadioLabel = document.createElement('label');
    approvalRadioLabel.className = 'execute-mode-radio-label';
    const approvalRadio = document.createElement('input');
    approvalRadio.type = 'radio';
    approvalRadio.name = 'execute-mode';
    approvalRadio.value = '';
    approvalRadio.checked = true;
    approvalRadio.id = 'execute-mode-approval';
    approvalRadioLabel.appendChild(approvalRadio);
    approvalRadioLabel.appendChild(document.createTextNode(' Send for approval (default)'));
    radioGroup.appendChild(approvalRadioLabel);

    // Option 2: Execute Now
    const directRadioLabel = document.createElement('label');
    directRadioLabel.className = 'execute-mode-radio-label';
    const directRadio = document.createElement('input');
    directRadio.type = 'radio';
    directRadio.name = 'execute-mode';
    directRadio.value = 'direct';
    directRadio.id = 'execute-mode-direct';
    directRadioLabel.appendChild(directRadio);
    directRadioLabel.appendChild(document.createTextNode(' Execute Now'));
    radioGroup.appendChild(directRadioLabel);

    toggleSection.appendChild(radioGroup);
    container.appendChild(toggleSection);

    // Warning callout shown only when "Execute Now" is selected.
    const directWarning = document.createElement('div');
    directWarning.className = 'direct-execute-warning';
    directWarning.hidden = true;
    directWarning.setAttribute('role', 'alert');
    directWarning.setAttribute('aria-live', 'polite');
    container.appendChild(directWarning);

    // Wire radio changes to update state + show/hide warning.
    const updateExecuteMode = (): void => {
      recsState.currentExecuteMode = directRadio.checked ? 'direct' : '';
      renderDirectExecuteWarning();
      // Update the submit button label to reflect the selected mode.
      const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
      if (executeBtn) {
        executeBtn.textContent = executeButtonLabel(recsState.currentExecuteMode === 'direct');
      }
    };

    approvalRadio.addEventListener('change', updateExecuteMode);
    directRadio.addEventListener('change', updateExecuteMode);
  } else {
    // No direct-execute permission: show the standard approval-required note.
    const approvalNote = document.createElement('p');
    approvalNote.className = 'approval-required-note';
    approvalNote.textContent =
      'Submitting will email an approval request to the configured approver - commitments are charged only after the approver clicks the link in that email.';
    container.appendChild(approvalNote);
  }

  // Commitments table with per-row Include checkboxes, Term, and Payment selects.
  const commitsSection = document.createElement('div');
  commitsSection.className = 'form-section purchase-modal-commits';

  const table = document.createElement('table');
  table.className = 'purchase-modal-table';

  // Table header: select-all checkbox + per-column labels.
  const thead = document.createElement('thead');
  const headRow = document.createElement('tr');

  // Select-all checkbox in the header (issue #320).
  const selectAllTh = document.createElement('th');
  const selectAllCb = document.createElement('input');
  selectAllCb.type = 'checkbox';
  selectAllCb.id = 'purchase-modal-select-all';
  selectAllCb.checked = true; // all selected by default
  selectAllCb.setAttribute('aria-label', 'Select all purchases');
  selectAllCb.title = 'Select / deselect all';
  selectAllTh.appendChild(selectAllCb);

  const includeLabel = document.createElement('span');
  includeLabel.className = 'purchase-modal-include-label';
  includeLabel.textContent = 'Include';
  selectAllTh.appendChild(includeLabel);
  headRow.appendChild(selectAllTh);

  for (const label of ['Account', 'Service / Type', 'Region', 'Count', 'Upfront', 'Monthly Cost', 'Eff. Savings', 'Eff. %', 'Term', 'Payment']) {
    const th = document.createElement('th');
    th.textContent = label;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (let i = 0; i < recsState.currentPurchaseRecommendations.length; i++) {
    tbody.appendChild(renderPurchaseModalRow(i, resolvedSeeds[i]!.source));
  }
  table.appendChild(tbody);

  // Totals row in tfoot (issue #320: live totals updated on checkbox toggle).
  const tfoot = document.createElement('tfoot');
  const totalsRow = document.createElement('tr');
  totalsRow.id = 'purchase-modal-totals-row';
  totalsRow.className = 'purchase-modal-totals';
  table.appendChild(tfoot);
  tfoot.appendChild(totalsRow);

  commitsSection.appendChild(table);
  // Upfront / Monthly / Total over term for the checked rows (issue #711);
  // refilled by updatePurchaseModalTotals.
  const band = document.createElement('div');
  band.id = 'purchase-modal-commitment-band';
  commitsSection.appendChild(band);
  container.appendChild(commitsSection);

  // Wire the select-all checkbox (must happen after tbody rows are in DOM
  // so per-row checkboxes can be queried).
  selectAllCb.addEventListener('change', () => {
    const allIndices = recsState.currentPurchaseRecommendations.map((_, i) => i);
    if (selectAllCb.checked) {
      recsState.checkedPurchaseIndices = new Set(allIndices);
    } else {
      recsState.checkedPurchaseIndices = new Set();
    }
    // Sync each row's checkbox to the new state.
    const rowCheckboxes = container.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
    rowCheckboxes.forEach((cb) => {
      cb.checked = selectAllCb.checked;
    });
    updatePurchaseModalTotals(selectAllCb);
  });

  // Initial totals render and Execute button state.
  updatePurchaseModalTotals(selectAllCb);

  const purchaseModal = document.getElementById('purchase-modal');
  // onClose covers every close path (Escape included, issue #331): closing
  // this modal without submitting must always discard both single-row and
  // fan-out purchase state, or a later fan-out open can submit stale
  // buckets left over from a previous session instead of the current
  // selection.
  if (purchaseModal) {
    openModal(purchaseModal, {
      onClose: () => {
        clearPurchaseModalRecommendations();
        clearFanOutBuckets();
        resetExecuteModeAndLabel();
        dismissOverrideWarning();
      },
    });
  }
}
