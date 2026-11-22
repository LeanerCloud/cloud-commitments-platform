import { formatCurrency } from '../utils';
import type { OfferingOption } from '../api';
import { createIcon } from '../icons';
import { riexState } from './riexState';

export const exchangePaymentPattern = /^\d+(?:\.\d+)?$/;

// A target count is a whole number >= 1; anything else (empty, 0, "2.5",
// "3abc") is null so the form can refuse it instead of quoting one unit.
export function parseTargetCount(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const n = Number(trimmed);
  return Number.isSafeInteger(n) && n >= 1 ? n : null;
}

// Human-readable provider labels for empty-state copy and the not-available
// message. Mirrors the map in inventory.ts.
const PROVIDER_LABELS: Record<string, string> = { aws: 'AWS', azure: 'Azure', gcp: 'GCP' };

export function providerLabel(provider: string): string {
  return PROVIDER_LABELS[provider] ?? provider.toUpperCase();
}

// Mode label mapping — single source of truth
export const MODE_LABELS: Record<string, string> = { manual: "Manual Approval", auto: "Fully Automated" };

/**
 * Render a plain `.empty` paragraph via textContent (no innerHTML) so backend
 * or provider-derived strings can never inject markup. Wipes any prior
 * children (skeleton, stale table) for a clean handoff.
 */
export function renderEmptyState(container: HTMLElement, message: string): void {
  container.textContent = '';
  const p = document.createElement('p');
  p.className = 'empty';
  p.textContent = message;
  container.appendChild(p);
}

/** "Expires in 35 days; extend the term." -> "expires in 35 days; extend the term". */
export function inlineReason(reason: string): string {
  const trimmed = reason.trim().replace(/[.\s]+$/, '');
  // Lower-case a capitalised first word, but leave acronyms ("AWS") alone.
  return /^[A-Z][a-z]/.test(trimmed) ? trimmed[0]!.toLowerCase() + trimmed.slice(1) : trimmed;
}

/**
 * Fill `el` as a table callout: an alert icon and a muted body, one line
 * per entry. Used for the reshape staleness and excluded-RI notices, which
 * used to be bold run-on paragraphs with a "!" prefix.
 */
export function renderTableCallout(el: HTMLElement, tone: 'warning' | 'error', lines: string[]): void {
  el.className = `freshness-banner ${tone} table-callout`;
  el.setAttribute('role', 'note');
  el.textContent = '';
  el.appendChild(createIcon('alert-triangle'));
  const body = document.createElement('div');
  body.className = 'table-callout-body';
  for (const line of lines) {
    const p = document.createElement('p');
    p.textContent = line;
    body.appendChild(p);
  }
  el.appendChild(body);
}

// currencyPrefix is the prefix formatCurrency puts before an amount in
// `code`. USD keeps the app-wide "$"; any other ISO-4217 code is shown
// as "EUR " so a non-USD price is never read as dollars (issue #284). An
// absent code keeps "$": exchanges are USD-only end to end (the backend
// refuses non-USD quotes), so a missing code is a USD offering.
export function currencyPrefix(code: string | undefined): string {
  const c = code?.trim().toUpperCase();
  return !c || c === 'USD' ? '$' : `${c} `;
}

// formatOfferingCost renders an offering's monthly cost in its own currency.
export function formatOfferingCost(alt: OfferingOption, cost: number = alt.effective_monthly_cost): string {
  return formatCurrency(cost, currencyPrefix(alt.currency_code), 2);
}

// "CUDly host (123456789012)": the ID is added only when the name lacks it.
export function riAccountLabel(accountID: string | undefined): string {
  if (!accountID) return 'could not be determined';
  const name = riexState.riAccountNames.get(accountID);
  if (!name) return `AWS account ${accountID}`;
  return name.includes(accountID) ? name : `${name} (${accountID})`;
}
