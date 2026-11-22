import * as api from '../api';
import { formatCurrency } from '../utils';
import { confirmDialog } from '../confirmDialog';
import { buildExchangeConfirmBody } from '../riexchange-confirm';
import type { ExchangeQuoteSummary, OfferingOption, TargetOffering } from '../api';
import { openModal, closeModal, createModalHeader } from '../modal';
import { createIcon } from '../icons';
import { showToast } from '../toast';
import { riexState } from './riexState';
import { exchangePaymentPattern, parseTargetCount, currencyPrefix, formatOfferingCost, riAccountLabel } from './shared';
import { TargetRow, fillPickerOptions, collectTargets, buildQuoteReq, revealExchangeHistory, setResultText, renderModalQuoteResult, QuoteReqShape } from './modalHelpers';
import { loadRIExchange } from './load';

export function openExchangeModal(riId: string, count: number, suggestedTargetType?: string, alternativeTargets?: OfferingOption[]): void {
  const modalEl = document.getElementById('ri-exchange-modal');
  if (!modalEl) return;
  const modal = modalEl; // non-null const for use in closures

  const content = modal.querySelector('.modal-content');
  if (!content) return;

  // Scoped state for this modal session. Stored against the modal
  // across quote/execute so execute can re-post the same shape the
  // user saw quoted.
  let modalQuote: ExchangeQuoteSummary | null = null;
  let modalQuoteReq: QuoteReqShape | null = null;
  let quoteRevision = 0;
  let sessionOpen = true;

  // Build header
  const h3 = document.createElement('h3');
  h3.textContent = 'RI Exchange';
  content.textContent = '';
  // "Close dialog", not "Close": a finished exchange relabels Cancel to Close.
  content.appendChild(createModalHeader(h3, { closeLabel: 'Close dialog' }));

  // RI ID display
  const riRow = document.createElement('div');
  riRow.className = 'setting-row';
  const riLabel = document.createElement('label');
  riLabel.textContent = 'RI ID: ';
  const riSpan = document.createElement('span');
  riSpan.className = 'monospace';
  riSpan.textContent = riId;
  riLabel.appendChild(riSpan);
  riRow.appendChild(riLabel);
  content.appendChild(riRow);

  // #1692: name the account before anything is quoted or executed.
  const sourceRI = riexState.currentRIs.find(r => r.reserved_instance_id === riId);
  if (sourceRI) {
    const accountRow = document.createElement('div');
    accountRow.className = 'setting-row';
    accountRow.id = 'modal-exchange-account';
    accountRow.textContent = `Account: ${riAccountLabel(sourceRI.account_id)}`;
    content.appendChild(accountRow);
  }

  // Targets container: one or more rows, each with offering picker +
  // count. Users click "+ Add target" to split a source RI across
  // multiple target shapes in a single atomic AWS exchange.
  const targetsContainer = document.createElement('div');
  targetsContainer.id = 'modal-exchange-targets';
  targetsContainer.className = 'exchange-targets-container';
  content.appendChild(targetsContainer);

  const targetRows: TargetRow[] = [];

  // awsOfferings holds the list loaded from the target-offerings endpoint.
  // Starts empty; populateAwsOfferings() fills it once after the modal opens.
  let awsOfferings: TargetOffering[] = [];
  // offeringsLoaded tracks whether the async load has completed so new
  // rows added after completion are seeded with the already-loaded list.
  let offeringsLoaded = false;
  let offeringsError = false;

  // buildPickerOptions rebuilds the <select> options for all existing
  // rows. Called once after the AWS offerings are loaded, and for any
  // row added after that point.
  const buildPickerOptions = (select: HTMLSelectElement, initialOfferingId?: string): void =>
    fillPickerOptions(select, { awsOfferings, offeringsLoaded, offeringsError }, alternativeTargets, initialOfferingId);

  const refreshPickers = (): void => {
    for (const row of targetRows) {
      buildPickerOptions(row.pickerSelect, row.offeringInput.value || undefined);
    }
  };

  // populateAwsOfferings loads target offerings from the backend and
  // refreshes all existing pickers. On failure it shows an error with a
  // Retry button instead of leaving the user at a dead end (issue #740).
  const populateAwsOfferings = async (): Promise<void> => {
    offeringsLoaded = false;
    offeringsError = false;
    offeringsErrorRow.classList.add('hidden');
    offeringsEmptyRow.classList.add('hidden');
    offeringsEmptyRow.textContent = '';
    refreshPickers();
    try {
      awsOfferings = await api.listTargetOfferings(riId);
    } catch (error) {
      awsOfferings = [];
      offeringsError = true;
      const reason = error instanceof Error && error.message ? `: ${error.message}` : '.';
      offeringsErrorText.textContent = `Could not load target offerings${reason}`;
      offeringsErrorRow.classList.remove('hidden');
    }
    offeringsLoaded = true;
    if (!offeringsError && awsOfferings.length === 0 && !(alternativeTargets && alternativeTargets.length > 0)) {
      offeringsEmptyRow.textContent = 'AWS returned no convertible offerings matching this RI\'s platform, tenancy, scope and term, so there is nothing to exchange into.';
      offeringsEmptyRow.classList.remove('hidden');
    }
    refreshPickers();
  };

  // Empty state for a successful load that returned nothing: without it the
  // picker is a bare placeholder and Get Quote is unusable with no reason (#1696).
  const offeringsEmptyRow = document.createElement('div');
  offeringsEmptyRow.className = 'setting-row exchange-offerings-empty hidden';
  offeringsEmptyRow.setAttribute('role', 'status');
  content.appendChild(offeringsEmptyRow);

  // Error state for a failed offerings load: the message is an alert so
  // screen readers hear it, and Retry reloads the list in place.
  const offeringsErrorRow = document.createElement('div');
  offeringsErrorRow.className = 'setting-row exchange-offerings-error hidden';
  const offeringsErrorText = document.createElement('p');
  offeringsErrorText.className = 'error';
  offeringsErrorText.setAttribute('role', 'alert');
  const retryOfferingsBtn = document.createElement('button');
  retryOfferingsBtn.type = 'button';
  retryOfferingsBtn.className = 'btn';
  retryOfferingsBtn.textContent = 'Retry';
  retryOfferingsBtn.addEventListener('click', () => {
    void populateAwsOfferings();
  });
  offeringsErrorRow.append(offeringsErrorText, retryOfferingsBtn);
  content.appendChild(offeringsErrorRow);

  const addTargetRow = (initialOfferingId?: string, initialCount?: number): void => {
    const rowIndex = targetRows.length;
    const rowEl = document.createElement('div');
    rowEl.className = 'setting-row exchange-target-row';
    rowEl.dataset.rowIndex = String(rowIndex);

    // Hidden input holds the resolved offering-id UUID. The picker
    // select drives this field; collectTargets reads from it. Tests can
    // set this directly to inject a UUID without going through the UI.
    const offeringInput = document.createElement('input');
    offeringInput.type = 'hidden';
    offeringInput.className = 'modal-exchange-target';
    if (initialOfferingId) offeringInput.value = initialOfferingId;
    rowEl.appendChild(offeringInput);

    // Visible picker: a <select> with two optgroups (AWS + CE recs).
    const pickerLabel = document.createElement('label');
    pickerLabel.textContent = 'Target offering: ';
    const pickerSelect = document.createElement('select');
    pickerSelect.className = 'modal-exchange-target-select';
    buildPickerOptions(pickerSelect, initialOfferingId);
    pickerLabel.appendChild(pickerSelect);
    rowEl.appendChild(pickerLabel);

    const countLabel = document.createElement('label');
    countLabel.textContent = 'Count: ';
    const countInput = document.createElement('input');
    countInput.type = 'number';
    countInput.min = '1';
    countInput.value = String(initialCount ?? 1);
    countInput.className = 'modal-exchange-count';
    countLabel.appendChild(countInput);
    rowEl.appendChild(countLabel);

    const removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.className = 'btn exchange-remove-target';
    removeBtn.appendChild(createIcon('x'));
    removeBtn.setAttribute('aria-label', 'Remove target');
    removeBtn.addEventListener('click', () => {
      if (targetRows.length <= 1) return; // keep at least one row
      const idx = targetRows.findIndex((r) => r.rowEl === rowEl);
      if (idx >= 0) {
        targetRows.splice(idx, 1);
        rowEl.remove();
        updateRunningTotal();
        invalidateQuote();
      }
    });
    rowEl.appendChild(removeBtn);

    // Cost chip: shows per-instance monthly cost for the selected offering
    // when it appears in alternativeTargets; otherwise shows "—".
    const chipEl = document.createElement('span');
    chipEl.className = 'cost-chip';
    chipEl.textContent = '—';
    rowEl.appendChild(chipEl);

    // pickerSelect drives the hidden offeringInput and refreshes the chip.
    pickerSelect.addEventListener('change', () => {
      offeringInput.value = pickerSelect.value;
      updateRowChip(pickerSelect.value, chipEl);
      updateRunningTotal();
      invalidateQuote();
    });
    countInput.addEventListener('input', () => {
      updateRunningTotal();
      invalidateQuote();
    });

    targetsContainer.appendChild(rowEl);
    targetRows.push({ offeringInput, pickerSelect, countInput, chipEl, rowEl });
    // Initial chip population for pre-filled rows.
    updateRowChip(offeringInput.value, chipEl);
  };

  // lookupCEAlt returns the alternativeTargets entry (cost and currency)
  // for an offering_id, or undefined when absent.
  function lookupCEAlt(offeringId: string): OfferingOption | undefined {
    if (!alternativeTargets || !offeringId) return undefined;
    return alternativeTargets.find((a) => a.offering_id === offeringId);
  }

  function updateRowChip(offeringId: string, chip: HTMLSpanElement): void {
    const alt = lookupCEAlt(offeringId);
    chip.textContent = alt ? `${formatOfferingCost(alt)}/mo each` : '—';
  }

  // Seed the modal with one row, optionally pre-selecting by offering_id.
  // suggestedTargetType is an instance type (from reshape recs); we
  // match it against CE alternatives to find the offering_id to pre-select.
  const suggestedOfferingId = suggestedTargetType && alternativeTargets
    ? (alternativeTargets.find((a) => a.instance_type === suggestedTargetType)?.offering_id)
    : undefined;
  addTargetRow(suggestedOfferingId, count);

  // Kick off the async AWS offerings load after the first row exists.
  void populateAwsOfferings();

  const addTargetBtnRow = document.createElement('div');
  addTargetBtnRow.className = 'setting-row';
  const addTargetBtn = document.createElement('button');
  addTargetBtn.type = 'button';
  addTargetBtn.className = 'btn';
  addTargetBtn.id = 'modal-exchange-add-target';
  addTargetBtn.textContent = '+ Add target';
  addTargetBtn.addEventListener('click', () => {
    addTargetRow();
    updateRunningTotal();
    invalidateQuote();
  });
  addTargetBtnRow.appendChild(addTargetBtn);
  content.appendChild(addTargetBtnRow);

  // Running total line. Hidden when openExchangeModal is called without
  // alternativeTargets (Convertible RIs table path) since there's no
  // pricing to sum.
  const runningTotalEl = document.createElement('div');
  runningTotalEl.className = 'setting-row exchange-running-total';
  runningTotalEl.id = 'modal-exchange-running-total';
  if (!alternativeTargets || alternativeTargets.length === 0) {
    runningTotalEl.classList.add('hidden');
  }
  content.appendChild(runningTotalEl);

  function updateRunningTotal(): void {
    if (!alternativeTargets || alternativeTargets.length === 0) {
      return;
    }
    let total = 0;
    let anyMissing = false;
    const prefixes = new Set<string>();
    for (const row of targetRows) {
      const alt = lookupCEAlt(row.offeringInput.value);
      const cnt = parseTargetCount(row.countInput.value);
      if (!alt || cnt === null) {
        anyMissing = true;
        continue;
      }
      prefixes.add(currencyPrefix(alt.currency_code));
      total += alt.effective_monthly_cost * cnt;
    }
    if (prefixes.size > 1) {
      // Adding amounts in different currencies would print a meaningless sum.
      runningTotalEl.textContent = 'Estimated monthly cost for the quoted target set: not totaled -- targets are priced in different currencies';
      return;
    }
    const [prefix = '$'] = prefixes;
    const suffix = anyMissing ? ' (incomplete -- some targets have no pricing data)' : '';
    runningTotalEl.textContent = `Estimated monthly cost for the quoted target set: ${formatCurrency(total, prefix, 2)}/mo${suffix}`;
  }
  updateRunningTotal();

  // Result container. A polite live region so quote results, progress and
  // the exchange receipt are announced; setResultText marks errors as alerts.
  const resultContainer = document.createElement('div');
  resultContainer.id = 'modal-exchange-result';
  resultContainer.setAttribute('role', 'status');
  resultContainer.setAttribute('aria-live', 'polite');
  content.appendChild(resultContainer);

  // Execute button (hidden until valid quote)
  const executeBtn = document.createElement('button');
  executeBtn.className = 'btn primary hidden';
  executeBtn.textContent = 'Execute Exchange';

  // Buttons row
  const btnRow = document.createElement('div');
  btnRow.className = 'modal-buttons';

  const quoteBtn = document.createElement('button');
  quoteBtn.className = 'btn primary';
  quoteBtn.textContent = 'Get Quote';

  const cancelBtn = document.createElement('button');
  cancelBtn.className = 'btn';
  cancelBtn.textContent = 'Cancel';

  btnRow.appendChild(quoteBtn);
  btnRow.appendChild(executeBtn);
  btnRow.appendChild(cancelBtn);
  content.appendChild(btnRow);

  // Show modal
  openModal(modal, { onClose: () => {
    sessionOpen = false;
    invalidateQuote();
  } });

  cancelBtn.addEventListener('click', () => {
    closeModal(modal);
  });

  // invalidateQuote discards any previously fetched quote whenever the
  // target set changes (picker, count, remove-row), so Execute can never
  // submit a request that no longer matches what the form displays.
  function invalidateQuote(): void {
    quoteRevision++;
    modalQuote = null;
    modalQuoteReq = null;
    executeBtn.classList.add('hidden');
    resultContainer.textContent = '';
  }

  quoteBtn.addEventListener('click', () => {
    void submitModalQuote();
  });

  executeBtn.addEventListener('click', () => {
    void submitModalExecute();
  });

  // lockCompletedExchange freezes the form after a successful exchange so
  // no edit can clear the receipt (edits call invalidateQuote) and no second
  // quote is offered for RIs that no longer exist. Cancel becomes Close.
  function lockCompletedExchange(): void {
    targetsContainer.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLButtonElement>('input, select, button')
      .forEach((el) => { el.disabled = true; });
    addTargetBtn.disabled = true;
    executeBtn.classList.add('hidden');
    quoteBtn.classList.add('hidden');
    cancelBtn.textContent = 'Close';
    cancelBtn.focus();
  }

  async function submitModalQuote(): Promise<void> {
    if (quoteBtn.disabled) return; // guard against overlapping quote requests
    const { targets, error } = collectTargets(targetRows);
    if (error) {
      setResultText(resultContainer, error, 'error');
      return;
    }

    invalidateQuote();
    const revision = quoteRevision;
    setResultText(resultContainer, 'Getting exchange quote...', 'loading');
    quoteBtn.disabled = true;

    const quoteReq = buildQuoteReq(riId, targets);
    try {
      const quote = await api.getExchangeQuote(quoteReq);
      if (!sessionOpen || revision !== quoteRevision) return;
      modalQuote = quote;
      modalQuoteReq = quoteReq;
      renderModalQuoteResult(resultContainer, modalQuote);
      if (modalQuote.IsValidExchange) executeBtn.classList.remove('hidden');
    } catch (error) {
      if (!sessionOpen || revision !== quoteRevision) return;
      const err = error as Error;
      setResultText(resultContainer, 'Quote failed: ' + err.message, 'error');
    } finally {
      quoteBtn.disabled = false;
    }
  }

  async function submitModalExecute(): Promise<void> {
    if (executeBtn.disabled || !sessionOpen || !modalQuote?.IsValidExchange || !modalQuoteReq) return;
    const quote = modalQuote;
    const request = modalQuoteReq;
    if (!exchangePaymentPattern.test(quote.PaymentDueRaw) || !quote.CurrencyCode?.trim()) {
      setResultText(resultContainer, 'Cannot confirm exchange without a quoted payment and currency. Get a new quote.', 'error');
      return;
    }
    const targets = request.targets ?? [{ offering_id: request.target_offering_id, count: request.target_count }];
    executeBtn.disabled = true;
    quoteBtn.disabled = true;
    let confirmed = false;
    try {
      confirmed = await confirmDialog({
        title: 'Execute RI Exchange',
        body: buildExchangeConfirmBody({
          quote,
          sources: request.ri_ids.map(id => {
            const ri = riexState.currentRIs.find(r => r.reserved_instance_id === id);
            return ri ? `${ri.instance_count} \u00d7 ${ri.instance_type} (${id})` : id;
          }),
          targets: targets.map(target => {
            const o = awsOfferings.find(x => x.offering_id === target.offering_id);
            if (o) return `${target.count} \u00d7 ${o.instance_type} (${o.offering_type}, offering ${target.offering_id})`;
            const alt = alternativeTargets?.find(x => x.offering_id === target.offering_id);
            return alt
              ? `${target.count} \u00d7 ${alt.instance_type} (offering ${target.offering_id})`
              : `${target.count} \u00d7 offering ${target.offering_id}`;
          }),
        }),
        confirmLabel: 'Execute Exchange',
        destructive: true,
        initialFocus: 'cancel',
      });
      if (!confirmed) return;
      setResultText(resultContainer, 'Executing exchange...', 'loading');
      const result = await api.executeExchange({
        ...request,
        max_payment_due_usd: quote.PaymentDueRaw,
        region: quote.Region,
      });
      void loadRIExchange();
      // The toast is the receipt even if the modal was closed mid-request;
      // the exchange ID also stays in the open modal until the user closes it.
      showToast({
        kind: 'success',
        message: `RI exchange completed. Exchange ID: ${result.exchange_id}`,
        actions: [{ label: 'View history', onClick: () => {
          if (sessionOpen) closeModal(modal);
          revealExchangeHistory();
        } }],
      });
      if (!sessionOpen) return;
      setResultText(resultContainer, 'Exchange completed. ID: ' + result.exchange_id, 'success-message');
      modalQuote = null;
      modalQuoteReq = null;
      lockCompletedExchange();
    } catch (error) {
      if (!sessionOpen) return;
      const err = error as Error;
      setResultText(resultContainer, 'Exchange failed: ' + err.message, 'error');
    } finally {
      executeBtn.disabled = false;
      quoteBtn.disabled = false;
      if (!confirmed) executeBtn.focus();
    }
  }
}
