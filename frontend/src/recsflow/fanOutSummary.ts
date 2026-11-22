import * as state from '../state';
import { formatCurrency, CURRENCY_DEFAULT_DIGITS } from '../utils';
import { commitmentLine, formatCommitmentTotals, type CommitmentLine } from '../commitmentSummary';
import { recsState } from './recsState';
import { formatCostForPeriod, periodSuffix } from './money';
import { type FanOutBucket, isSubmittableBucket } from './fanOutState';

/**
 * Narrows the fan-out modal to the buckets with the given ids (issue #757):
 * after a partial failure only the failed buckets stay submittable, so a
 * retry cannot resubmit the ones that already went through. Re-renders the
 * modal header so the email count and totals describe the retry.
 */
export function retainFanOutBuckets(ids: ReadonlySet<string>): void {
  if (!recsState.currentFanOutBuckets) return;
  recsState.currentFanOutBuckets = recsState.currentFanOutBuckets.filter((b) => b.id !== undefined && ids.has(b.id));
  refreshFanOutSummary();
}

// renderFanOutSummary rebuilds the fan-out modal's header — title, email
// count, skipped-bucket note, and totals — from the submittable subset
// (issue #1904), so what the user sees here matches exactly what
// getFanOutBuckets() returns to app.ts on submit. Also disables the
// Execute button when nothing is submittable.
export function renderFanOutSummary(summary: HTMLElement, buckets: FanOutBucket[]): void {
  while (summary.firstChild) summary.removeChild(summary.firstChild);

  const submittable = buckets.filter(isSubmittableBucket);
  const skipped = buckets.length - submittable.length;

  const summaryTitle = document.createElement('h3');
  summaryTitle.textContent = `Bulk purchase — ${buckets.length} bucket${buckets.length === 1 ? '' : 's'}`;
  summary.appendChild(summaryTitle);

  const emailNote = document.createElement('p');
  emailNote.className = 'fanout-email-note';
  let emailText = `Will send ${submittable.length} approval email${submittable.length === 1 ? '' : 's'} — one per bucket.`;
  if (skipped > 0) {
    emailText += ` ${skipped} incompatible bucket${skipped === 1 ? '' : 's'} will be skipped.`;
  }
  emailNote.textContent = emailText;
  summary.appendChild(emailNote);

  const totals = computeFanOutTotals(submittable);
  // A key/value grid: each total is a small tile with its label above the
  // value. The text still reads "Label: value" for assistive tech and copy;
  // only the colon is hidden visually.
  const grid = document.createElement('div');
  grid.className = 'fanout-totals';
  const totalLine = (label: string, value: string, cls = ''): HTMLParagraphElement => {
    const p = document.createElement('p');
    p.className = cls ? `fanout-total fanout-total-${cls}` : 'fanout-total';
    const labelEl = document.createElement('span');
    labelEl.className = 'fanout-total-label';
    labelEl.textContent = label;
    const sep = document.createElement('span');
    sep.className = 'fanout-total-sep';
    sep.textContent = ': ';
    const strong = document.createElement('strong');
    if (cls) strong.className = cls;
    strong.textContent = value;
    p.append(labelEl, sep, strong);
    return p;
  };
  // issue #319: scale total savings by the active cost period.
  const fanOutPeriod = state.getCostPeriod();
  grid.appendChild(totalLine('Total commitments', String(totals.totalCount)));
  grid.appendChild(totalLine('Total upfront', formatCurrency(totals.totalUpfront)));
  // Issue #711: upfront alone reads $0 for no-upfront buckets that bill
  // monthly for one or three years, so state the recurring charge and the
  // total over the term next to it.
  const termText = formatCommitmentTotals(submittable.flatMap(bucketCommitmentLines), CURRENCY_DEFAULT_DIGITS);
  grid.appendChild(totalLine('Monthly recurring', termText.monthly));
  grid.appendChild(totalLine('Total over term', termText.termTotal));
  grid.appendChild(totalLine(`Total savings ${periodSuffix(fanOutPeriod)}`, formatCostForPeriod(totals.totalSavings, fanOutPeriod), 'savings'));
  summary.appendChild(grid);

  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  if (executeBtn) {
    const submitting = executeBtn.dataset['submitting'] === 'true';
    executeBtn.disabled = submitting || submittable.length === 0;
    executeBtn.title = submitting
      ? 'Purchase submission in progress'
      : submittable.length === 0 ? 'No compatible buckets to submit' : '';
  }
}

// refreshFanOutSummary re-renders #fanout-summary from recsState.currentFanOutBuckets.
// Called after a bucket's Payment change so a user who repairs a skipped
// bucket sees the email count, skipped note, and totals follow immediately
// (issue #1904) rather than only on the next modal open.
export function refreshFanOutSummary(): void {
  if (!recsState.currentFanOutBuckets) return;
  const summary = document.getElementById('fanout-summary');
  if (!summary) return;
  renderFanOutSummary(summary, recsState.currentFanOutBuckets);
}

export function bucketCommitmentLines(b: FanOutBucket): CommitmentLine[] {
  return b.recs.map((r) => commitmentLine(r, { payment: b.payment }));
}

function computeFanOutTotals(buckets: FanOutBucket[]): { totalCount: number; totalUpfront: number; totalSavings: number } {
  let totalCount = 0;
  let totalUpfront = 0;
  let totalSavings = 0;
  for (const b of buckets) {
    for (const r of b.recs) {
      totalCount += r.count;
      totalUpfront += r.upfront_cost;
      totalSavings += r.savings;
    }
  }
  return { totalCount, totalUpfront, totalSavings };
}
