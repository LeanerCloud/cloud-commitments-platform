/**
 * Per-recommendation Archera quote (issue #799): one small button on each
 * Opportunities row, shown only when the backend reports the insurance
 * integration configured (the same status gate as the plan-wide comparison).
 * Nothing is requested on load: each click is one read-only request.
 *
 * Rules enforced here:
 *   - Every server string is set with textContent; the only innerHTML is the
 *     static icon markup.
 *   - Money is the exact decimal string the server sent, shown verbatim
 *     (null is "Unknown", never 0), with no currency symbol and no parsing.
 *   - "unavailable" (no exact Archera line item) shows "No Archera quote" and
 *     never a figure or an estimate.
 *   - Failures show fixed text chosen by HTTP status; no server or vendor
 *     text is ever displayed.
 *   - One panel at a time; a response for a row the user has left is dropped.
 */

import { getInsuranceRecommendation, getInsuranceStatus } from './api/insurance';
import type { ArcheraRecommendationComparison, ArcheraRecommendationOffer } from './api/types';
import { el, table, totalsRows, formatFetchedAt, text } from './archera-comparison';
import { iconSvg } from './icons';

export const NO_QUOTE = 'No Archera quote';
export const MSG_AMBIGUOUS = 'The Archera match for this recommendation is ambiguous. No quote is shown.';
export const MSG_INELIGIBLE = 'This recommendation is not eligible for an Archera quote.';
export const MSG_NOT_FOUND = 'An Archera quote is not available for this recommendation.';
export const MSG_GENERIC = 'The Archera quote could not be loaded. Try again later.';
const MSG_LOADING = 'Loading Archera quote';
const MSG_LOADED = 'Archera quote loaded';
const BUTTON_LABEL = 'Show Archera quote for this recommendation';

/** Fixed, status-keyed text. Server and vendor text is never shown. */
export function failureMessage(err: unknown): string {
  switch ((err as { status?: number } | null)?.status) {
    case 409: return MSG_AMBIGUOUS;
    case 422: return MSG_INELIGIBLE;
    case 404: return MSG_NOT_FOUND;
    default: return MSG_GENERIC;
  }
}

let gate: Promise<boolean> | null = null;

/** Reads the insurance status once per page load; any failure means off. */
export function insuranceEnabled(): Promise<boolean> {
  gate ??= getInsuranceStatus().then(s => s.configured === true, () => false);
  return gate;
}

/** Test hook: forget the cached status and any open panel. */
export function resetArcheraRecommendation(): void {
  gate = null;
  active?.ctrl.abort();
  active = null;
}

function offerTable(caption: string, o: ArcheraRecommendationOffer): HTMLElement {
  const t = table(caption, ['Measure', 'Amount']);
  const identity: Array<[string, string]> = [
    ['Offer', text(o.offer_id)],
    ['Provider / commitment type', `${text(o.provider)} / ${text(o.commitment_type)}`],
    ['Term / payment option', `${text(o.effective_contract_term)} / ${text(o.payment_option)}`],
  ];
  for (const [label, value] of identity) {
    const tr = el('tr');
    tr.append(el('th', label), el('td', value));
    t.body.appendChild(tr);
  }
  totalsRows(t.body, { ...o.monthly, upfront_one_time_cost: o.upfront_one_time_cost });
  t.align();
  return t.wrap;
}

/** Figures view for a resource_associated response. Exported for tests. */
export function buildQuoteView(q: ArcheraRecommendationComparison, current: ArcheraRecommendationOffer): HTMLElement {
  const root = el('div');
  root.appendChild(el('h4', 'Archera quote'));
  root.appendChild(el('p', formatFetchedAt(q.fetched_at), 'archera-fetched-at'));
  root.appendChild(el('p', q.currency_note, 'archera-note'));
  root.appendChild(el('p', q.non_gating_disclosure, 'archera-disclosure'));
  root.appendChild(el('p', q.sponsorship_disclosure, 'archera-disclosure'));
  root.appendChild(offerTable('Archera quote for this recommendation', current));
  for (const c of q.candidates) {
    root.appendChild(el('h5', `Alternative ${text(c.offer_id)} (${text(c.status)})`));
    if (c.figures) root.appendChild(offerTable(`Figures for alternative ${text(c.offer_id)}`, c.figures));
    else root.appendChild(el('p', 'No figures'));
  }
  return root;
}

interface Active { token: number; ctrl: AbortController; button: HTMLButtonElement; panel: HTMLElement }
let active: Active | null = null;
let seq = 0;

function clearActive(): void {
  if (!active) return;
  active.ctrl.abort();
  active.panel.remove();
  active.button.disabled = false;
  active.button.removeAttribute('aria-busy');
  active = null;
}

function openPanel(button: HTMLButtonElement, row: HTMLTableRowElement, id: string): void {
  clearActive();
  const token = ++seq;
  const ctrl = new AbortController();
  const panelRow = el('tr', undefined, 'rec-archera-row');
  const cellEl = el('td');
  cellEl.colSpan = row.children.length;
  const panel = el('section', undefined, 'rec-archera-panel');
  panel.setAttribute('aria-label', 'Archera quote');
  const live = el('div', MSG_LOADING);
  live.setAttribute('role', 'status');
  live.setAttribute('aria-live', 'polite');
  const result = el('div');
  panel.append(live, result);
  cellEl.appendChild(panel);
  panelRow.appendChild(cellEl);
  row.after(panelRow);
  active = { token, ctrl, button, panel: panelRow };
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  const stale = (): boolean => token !== seq;

  void getInsuranceRecommendation(id, ctrl.signal)
    .then(q => {
      if (stale()) return;
      if (q?.status === 'unavailable') {
        result.replaceChildren(el('p', NO_QUOTE));
        live.textContent = NO_QUOTE;
      } else if (q?.status === 'resource_associated' && q.current) {
        result.replaceChildren(buildQuoteView(q, q.current));
        live.textContent = MSG_LOADED;
      } else {
        live.textContent = MSG_GENERIC;
      }
    })
    .catch((err: unknown) => {
      if (stale()) return;
      live.textContent = failureMessage(err);
    })
    .finally(() => {
      if (stale()) return;
      button.disabled = false;
      button.removeAttribute('aria-busy');
      if (button.isConnected) button.focus();
    });
}

/**
 * Adds the per-row button to every recommendation row in `container` once the
 * insurance status says configured. With the gate off no button is added and
 * no quote request is ever made.
 */
export async function attachArcheraQuoteControls(container: HTMLElement): Promise<void> {
  if (active && !active.panel.isConnected) clearActive();
  if (!(await insuranceEnabled()) || !container.isConnected) return;
  container.querySelectorAll<HTMLTableRowElement>('tr.recommendation-row[data-rec-id]').forEach(row => {
    const id = row.dataset['recId'] ?? '';
    const host = row.querySelector('.rec-select-cell') ?? row.querySelector('td.checkbox-col');
    if (!id || !host || host.querySelector('.rec-archera-btn')) return;
    const btn = el('button', undefined, 'btn-small icon-btn rec-archera-btn');
    btn.type = 'button';
    btn.dataset['action'] = 'archera-quote';
    btn.setAttribute('aria-label', BUTTON_LABEL);
    btn.title = 'Archera insured-commitment quote for this recommendation';
    btn.innerHTML = iconSvg('info'); // static icon markup, no server data
    btn.addEventListener('click', e => {
      e.stopPropagation();
      openPanel(btn, row, id);
    });
    host.appendChild(btn);
  });
}
