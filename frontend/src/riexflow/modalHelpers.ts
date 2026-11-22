import { hourlyPriceDelta } from '../riexchange-confirm';
import type { ExchangeQuoteSummary, OfferingOption, TargetOffering } from '../api';
import { parseTargetCount, formatOfferingCost } from './shared';

// ──────────────────────────────────────────────
// RI Exchange Modal
// ──────────────────────────────────────────────

// TargetRow captures the DOM inputs for one target-offering entry in
// the multi-target modal. Tests assert on the posted request shape by
// finding inputs through their class attributes.
//
// offeringInput is a hidden <input type="hidden"> that holds the resolved
// AWS ReservedInstancesOfferingId UUID. The visible picker
// (modal-exchange-target-select) drives this field on change so the
// submission path always sees a UUID and never a free-text instance type.
export interface TargetRow {
  offeringInput: HTMLInputElement;  // hidden; holds the UUID
  pickerSelect: HTMLSelectElement;  // visible; drives offeringInput
  countInput: HTMLInputElement;
  chipEl: HTMLSpanElement; // cost chip; shows "$X.XX/mo each" or "—".
  rowEl: HTMLDivElement;
}

export type QuoteReqShape = {
  ri_ids: string[];
  targets?: Array<{ offering_id: string; count: number }>;
  target_offering_id?: string;
  target_count?: number;
};

export interface PickerOfferings {
  awsOfferings: TargetOffering[];
  offeringsLoaded: boolean;
  offeringsError: boolean;
}

export function fillPickerOptions(
  select: HTMLSelectElement,
  offerings: PickerOfferings,
  alternativeTargets: OfferingOption[] | undefined,
  initialOfferingId?: string,
): void {
  const { awsOfferings, offeringsLoaded, offeringsError } = offerings;
  // Remove all existing options to avoid duplicate listeners.
  select.innerHTML = '';

  const placeholder = document.createElement('option');
  placeholder.value = '';
  if (offeringsError) {
    // The AWS list failed; CE recommendations (if any) stay selectable,
    // and the Retry button below the targets reloads the AWS list.
    placeholder.textContent = alternativeTargets && alternativeTargets.length > 0
      ? 'AWS offerings unavailable -- pick a recommendation or retry'
      : 'Could not load offerings';
  } else if (!offeringsLoaded) {
    placeholder.textContent = 'Loading offerings...';
  } else if (awsOfferings.length === 0 && !(alternativeTargets && alternativeTargets.length > 0)) {
    placeholder.textContent = 'No compatible offerings found';
  } else {
    placeholder.textContent = 'Select a target offering';
  }
  select.appendChild(placeholder);

  // Group 1: AWS-driven target offerings from DescribeReservedInstancesOfferings
  if (awsOfferings.length > 0) {
    const grp = document.createElement('optgroup');
    grp.label = 'AWS Target Offerings';
    for (const o of awsOfferings) {
      const opt = document.createElement('option');
      opt.value = o.offering_id;
      // Display: "m5.large -- No Upfront" (no HTML injection: all fields from API)
      // textContent escapes on its own; pre-escaping would show "&amp;".
      opt.textContent = o.instance_type + (o.offering_type ? ' -- ' + o.offering_type : '');
      if (initialOfferingId && o.offering_id === initialOfferingId) {
        opt.selected = true;
      }
      grp.appendChild(opt);
    }
    select.appendChild(grp);
  }

  // Group 2: CE recommendations (alternativeTargets from reshape recs)
  if (alternativeTargets && alternativeTargets.length > 0) {
    const grp = document.createElement('optgroup');
    grp.label = 'CE Recommendations';
    for (const alt of alternativeTargets) {
      const opt = document.createElement('option');
      opt.value = alt.offering_id;
      const costStr = formatOfferingCost(alt);
      opt.textContent = alt.instance_type + ' -- ' + costStr + '/mo';
      if (initialOfferingId && alt.offering_id === initialOfferingId) {
        opt.selected = true;
      }
      grp.appendChild(opt);
    }
    select.appendChild(grp);
  }
}

// offeringUUIDPattern mirrors the backend regex for AWS offering UUIDs.
// Backend: internal/api/handler_ri_exchange_offerings.go offeringIDPattern; keep the two in sync.
const offeringUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

// collectTargets reads each row into a validated target. Rows with
// empty or non-UUID offering IDs are treated as an error; the first
// offending row's message is surfaced to the user. A count that is not a
// whole number >= 1 is an error too (never defaulted).
export function collectTargets(targetRows: TargetRow[]): { targets: Array<{ offering_id: string; count: number }>; error?: string } {
  const targets: Array<{ offering_id: string; count: number }> = [];
  for (let i = 0; i < targetRows.length; i++) {
    const row = targetRows[i];
    if (!row) continue;
    const offeringId = row.offeringInput.value.trim();
    if (!offeringId) {
      return { targets: [], error: `Please select a target offering for target ${i + 1}.` };
    }
    if (!offeringUUIDPattern.test(offeringId)) {
      return {
        targets: [],
        error: `Target ${i + 1}: "${offeringId}" is not a valid offering UUID. Please select an offering from the dropdown.`,
      };
    }
    const targetCount = parseTargetCount(row.countInput.value);
    if (targetCount === null) {
      return { targets: [], error: `Target ${i + 1}: count must be a whole number >= 1.` };
    }
    targets.push({ offering_id: offeringId, count: targetCount });
  }
  return { targets };
}

// buildQuoteReq shapes the request body. With one target we post
// the singleton shape (target_offering_id + target_count) for
// backward-compat with older backends and simpler CI harnesses.
// With >1 target we post targets[] so the backend builds a
// multi-element TargetConfigurations slice in the AWS Accept call.
export function buildQuoteReq(riId: string, targets: Array<{ offering_id: string; count: number }>): QuoteReqShape {
  const first = targets[0];
  if (targets.length === 1 && first) {
    return {
      ri_ids: [riId],
      target_offering_id: first.offering_id,
      target_count: first.count,
    };
  }
  return { ri_ids: [riId], targets };
}

// revealExchangeHistory scrolls the Exchange History card into view and
// moves focus to its list, where the completed exchange is listed.
export function revealExchangeHistory(): void {
  const list = document.getElementById('ri-exchange-history-list');
  if (!list) return;
  list.tabIndex = -1;
  (list.closest('section') ?? list).scrollIntoView({ block: 'start' });
  list.focus({ preventScroll: true });
}

export function setResultText(container: HTMLElement, message: string, cls: string): void {
  container.textContent = '';
  const p = document.createElement('p');
  p.className = cls;
  // Errors interrupt; progress and success are announced politely by the
  // container's own role="status".
  if (cls === 'error') p.setAttribute('role', 'alert');
  p.textContent = message;
  container.appendChild(p);
}

export function renderModalQuoteResult(container: HTMLElement, quote: ExchangeQuoteSummary): void {
  container.textContent = '';

  const div = document.createElement('div');
  div.className = 'quote-summary ' + (quote.IsValidExchange ? 'quote-valid' : 'quote-invalid');

  const h4 = document.createElement('h4');
  h4.textContent = quote.IsValidExchange ? 'Valid Exchange' : 'Invalid Exchange';
  div.appendChild(h4);

  if (quote.ValidationFailureReason) {
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = quote.ValidationFailureReason;
    div.appendChild(p);
  }

  const rows: [string, string][] = [
    ['Currency', quote.CurrencyCode],
    ['Payment Due', quote.PaymentDueRaw],
    ['Source Hourly Price', quote.SourceHourlyPriceRaw],
    ['Target Hourly Price', quote.TargetHourlyPriceRaw],
  ];
  const delta = hourlyPriceDelta(quote);
  if (delta) rows.push(['Hourly Price Change', delta]);
  if (quote.OutputReservedInstancesExp) {
    rows.push(['New RI Expiry', quote.OutputReservedInstancesExp]);
  }

  const details = document.createElement('div');
  details.className = 'quote-details';
  for (const [label, value] of rows) {
    const row = document.createElement('div');
    row.className = 'quote-row';
    const span = document.createElement('span');
    span.textContent = label + ':';
    const strong = document.createElement('strong');
    strong.textContent = value;
    row.appendChild(span);
    row.appendChild(strong);
    details.appendChild(row);
  }
  div.appendChild(details);
  container.appendChild(div);
}
