import * as state from '../state';
import { formatCurrency } from '../utils';
import { showToast } from '../toast';
import { savingsPlanHourlySummary } from '../sp-capacity';
import {
  isSavingsPlanService,
  SAVINGS_PLANS_BUCKET_KEY,
  savingsPlansBucketLabel,
  UMBRELLA_SLUGS,
} from '../lib/purchase-compatibility';
import { commitmentTotals, paymentLabel } from '../commitmentSummary';
import type { LocalRecommendation } from '../types';
import { formatCostForPeriod, periodSuffix } from './money';
import { pricedCellVariant, cellPaymentOptions } from './groups';
import { fanOutBucketAccountLabel, type FanOutBucket, isSubmittableBucket } from './fanOutState';
import { refreshFanOutSummary, bucketCommitmentLines } from './fanOutSummary';

export function renderFanOutBucketSection(b: FanOutBucket): HTMLElement {
  const section = document.createElement('section');
  section.className = 'fanout-bucket form-section';
  if (b.id) section.dataset['bucketId'] = b.id;

  // Service label: SP bucket → "Savings Plans (Compute + SageMaker)"
  // listing only the plan types actually present in this bucket;
  // non-SP bucket → the raw service slug (e.g. "ec2", "rds"). Order
  // follows the recs' insertion order so it tracks the table.
  const isSPBucket = b.service === SAVINGS_PLANS_BUCKET_KEY;
  const serviceLabel = isSPBucket
    ? savingsPlansBucketLabel(b.recs.map((r) => r.service))
    : b.service;

  // The header counts recommendations (table rows); the totals line below
  // counts the commitments (units) they buy, so the two numbers no longer
  // share a word.
  const title = document.createElement('h4');
  title.className = 'fanout-bucket-title';
  const titleText = document.createElement('span');
  titleText.textContent = `${b.provider.toUpperCase()} / ${fanOutBucketAccountLabel(b)} / ${serviceLabel}`;
  const recCount = document.createElement('span');
  recCount.className = 'fanout-bucket-rec-count';
  recCount.textContent = `${b.recs.length} recommendation${b.recs.length === 1 ? '' : 's'}`;
  title.append(titleText, recCount);
  section.appendChild(title);

  const status = document.createElement('p');
  const renderStatus = (): void => {
    // Same predicate the submit filter and the header totals use, so the
    // "will be skipped" label cannot disagree with what actually submits.
    const compat = isSubmittableBucket(b);
    status.className = compat ? 'fanout-bucket-ok' : 'fanout-bucket-error';
    status.textContent = compat
      ? `${b.capacityPercent}% capacity · ${b.term}-year term · ${paymentLabel(b.payment).toLowerCase()}`
      : `A selected payment option is unavailable at ${b.capacityPercent}% capacity. This bucket will be skipped.`;
  };
  renderStatus();
  section.appendChild(status);

  // Issue #111: Per-bucket Payment dropdown. Default-selected =
  // bucket.payment (seeded by resolveBucketPaymentSeed: override →
  // toolbar). Options come from paymentOptionsFor, which already
  // filters to the supported (provider, service, term) Payment values
  // — so the user can never pick an unsupported combo here. The
  // change handler updates the bucket's payment in module state
  // (`recsState.currentFanOutBuckets`) so getFanOutBuckets() returns the
  // user's choice and handleFanOutExecute (in app.ts) reads the
  // right value per POST.
  const paymentRow = document.createElement('div');
  paymentRow.className = 'fanout-bucket-payment-row';
  const paymentLabelEl = document.createElement('label');
  paymentLabelEl.className = 'fanout-bucket-payment-label';
  paymentLabelEl.appendChild(document.createTextNode('Payment'));
  const paymentSelect = document.createElement('select');
  paymentSelect.className = 'fanout-bucket-payment';
  const purchasePending = document.getElementById('execute-purchase-btn')?.dataset['submitting'] === 'true';
  const bucketOptions = (b.recs[0] ? cellPaymentOptions(b.recs[0], b.term, b.capacityPercent) : [])
    .filter((payment) => b.recs.every((rec) =>
      cellPaymentOptions(rec, b.term, b.capacityPercent).includes(payment),
    ));
  const hasCurrentOption = bucketOptions.includes(b.payment);
  let unavailableOption: HTMLOptionElement | undefined;
  if (!hasCurrentOption) {
    const option = document.createElement('option');
    option.value = '';
    option.textContent = 'Unavailable: no priced payment';
    option.selected = true;
    option.disabled = true;
    paymentSelect.appendChild(option);
    unavailableOption = option;
  }
  for (const opt of bucketOptions) {
    const option = document.createElement('option');
    option.value = opt;
    option.textContent = paymentLabel(opt);
    option.selected = opt === b.payment;
    paymentSelect.appendChild(option);
  }
  if (unavailableOption) {
    unavailableOption.selected = true;
    paymentSelect.value = '';
  }
  paymentSelect.disabled = purchasePending || bucketOptions.length === 0;
  const renderedValue = paymentSelect.value;
  paymentSelect.addEventListener('change', () => {
    if (paymentSelect.disabled || document.getElementById('execute-purchase-btn')?.dataset['submitting'] === 'true') {
      paymentSelect.value = renderedValue;
      return;
    }
    const next = paymentSelect.value as FanOutBucket['payment'];
    const replacements: Array<LocalRecommendation | null> = b.recs.map((rec) => {
      const replacement = pricedCellVariant(rec, b.term, next, b.capacityPercent);
      return replacement ? { ...replacement, payment: next } : null;
    });
    if (replacements.some((replacement) => replacement === null)) {
      showToast({
        message: `No priced ${next} option is available for every row at ${b.capacityPercent}% capacity.`,
        kind: 'warning',
      });
      paymentSelect.value = renderedValue;
      return;
    }
    b.recs = replacements.map((replacement) => replacement!);
    b.payment = next;
    refreshFanOutSummary();
    const expanded = Array.from(section.querySelectorAll('details')).map((details) => details.open);
    const refreshed = renderFanOutBucketSection(b);
    refreshed.querySelectorAll('details').forEach((details, index) => {
      details.open = expanded[index] ?? false;
    });
    section.replaceWith(refreshed);
    refreshed.querySelector<HTMLSelectElement>('.fanout-bucket-payment')?.focus();
  });
  paymentLabelEl.appendChild(paymentSelect);
  paymentRow.appendChild(paymentLabelEl);
  if (b.paymentSource === 'override') {
    const sourceNote = document.createElement('span');
    sourceNote.className = 'fanout-bucket-payment-source';
    sourceNote.textContent = 'Set by account override';
    paymentRow.appendChild(sourceNote);
  }
  section.appendChild(paymentRow);

  const bucketTotal = b.recs.reduce(
    (acc, r) => ({
      count: acc.count + r.count,
      upfront: acc.upfront + r.upfront_cost,
      savings: acc.savings + r.savings,
    }),
    { count: 0, upfront: 0, savings: 0 },
  );
  // issue #319: scale bucket savings by the active cost period.
  const bPeriod = state.getCostPeriod();
  const totals = document.createElement('p');
  totals.className = 'fanout-bucket-totals';
  const bucketMonthly = commitmentTotals(bucketCommitmentLines(b)).monthly;
  const monthlyPart = bucketMonthly === null ? 'monthly cost not reported' : `${formatCurrency(bucketMonthly)}/mo recurring`;
  totals.textContent = `${bucketTotal.count} commitment${bucketTotal.count === 1 ? '' : 's'} · ${formatCurrency(bucketTotal.upfront)} upfront · ${monthlyPart} · ${formatCostForPeriod(bucketTotal.savings, bPeriod)} savings ${periodSuffix(bPeriod)}`;
  section.appendChild(totals);
  const spHourly = isSPBucket ? savingsPlanHourlySummary(b.recs, b.capacityPercent, isSavingsPlanService) : null;
  if (spHourly) {
    const spHourlyEl = document.createElement('p');
    spHourlyEl.className = 'fanout-bucket-sp-hourly';
    spHourlyEl.textContent = spHourly;
    section.appendChild(spHourlyEl);
  }

  // Issue #249: for mixed-SP buckets with 2+ distinct plan types, render
  // a collapsible per-plan-type breakdown so operators can see how their
  // bulk-buy is split across Compute / SageMaker / EC2 Instance / Database
  // plan types before submitting. Collapsed by default to keep the modal
  // compact; the chevron in the <summary> affords expand.
  if (isSPBucket) {
    // Group recs by their per-rec service slug, excluding umbrella slugs
    // (e.g. "savings-plans", "savingsplans") that represent the SP family
    // as a whole rather than a specific plan type. Including them would
    // inflate the "+N plan types" count and render a spurious non-concrete
    // plan-type row. Issue #249.
    const byPlanType = new Map<string, LocalRecommendation[]>();
    for (const r of b.recs) {
      if (UMBRELLA_SLUGS.has(r.service)) continue;
      const existing = byPlanType.get(r.service);
      if (existing) {
        existing.push(r);
      } else {
        byPlanType.set(r.service, [r]);
      }
    }
    if (byPlanType.size >= 2) {
      const details = document.createElement('details');
      details.className = 'fanout-sp-plan-types';
      const summaryEl = document.createElement('summary');
      summaryEl.className = 'fanout-sp-plan-types-summary';
      summaryEl.textContent = `+${byPlanType.size} plan types`;
      details.appendChild(summaryEl);

      for (const [slug, planRecs] of byPlanType) {
        const planLabel = savingsPlansBucketLabel([slug]);
        const planTotal = planRecs.reduce(
          (acc, r) => ({
            count: acc.count + r.count,
            upfront: acc.upfront + r.upfront_cost,
            savings: acc.savings + r.savings,
          }),
          { count: 0, upfront: 0, savings: 0 },
        );
        const row = document.createElement('p');
        row.className = 'fanout-sp-plan-type-row';
        row.textContent = `${planLabel}: ${planTotal.count} commitment${planTotal.count === 1 ? '' : 's'} · ${formatCurrency(planTotal.upfront)} upfront · ${formatCostForPeriod(planTotal.savings, bPeriod)} savings ${periodSuffix(bPeriod)}`;
        details.appendChild(row);
      }

      section.appendChild(details);
    }
  }

  return section;
}
