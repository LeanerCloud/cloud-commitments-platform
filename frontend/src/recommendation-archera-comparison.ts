import { getInsuranceStatus, getRecommendationInsuranceComparison } from './api/insurance';
import type { RecommendationArcheraComparison, RecommendationArcheraOffer } from './api/types';
import { canAccess } from './permissions';
import { openModal, closeModal } from './modal';

const pending = new Set<string>();

const FAILURES: Record<number, string> = {
  400: 'The recommendation request was invalid.',
  403: 'You are not authorised to view Archera alternatives.',
  404: 'This recommendation no longer exists. Refresh the list.',
  409: 'The recommendation changed or is ambiguous. Refresh the list and try again.',
  422: 'This recommendation type cannot be compared with Archera.',
  502: 'Archera is unavailable or returned an unusable response.',
  503: 'The comparison service is not available right now.',
  504: 'Archera did not respond in time.',
};

function failureText(error: unknown): string {
  const status = (error as { status?: number } | null)?.status;
  return (status !== undefined && FAILURES[status]) || 'Archera alternatives unavailable.';
}

function node<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  return element;
}

function offerView(offer: RecommendationArcheraOffer, title: string): HTMLElement {
  const section = node('section');
  section.append(node('h3', title));
  const values: Array<[string, string | null]> = [
    ['Offer', offer.offer_id],
    ['Provider / product', `${offer.provider} / ${offer.commitment_type}`],
    ['Selected hourly commitment', offer.hourly_selected_commitment],
    ['Native duration (seconds)', offer.native_duration_seconds],
    ['Native plan type', offer.native_plan_type],
    ['Native instance family', offer.native_instance_family],
    ['Effective contract / lease term', offer.effective_contract_term],
    ['Payment', offer.payment_option],
    ['Lease attached', offer.lease_attached ? 'Yes' : 'No'],
    ['Monthly commitment cost (730 hours)', offer.monthly.monthly_730h_commitment_cost_total],
    ['Monthly provider cost (730 hours)', offer.monthly.monthly_730h_cloud_provider_cost],
    ['Monthly Archera premium (730 hours)', offer.monthly.monthly_730h_archera_premium],
    ['Monthly gross savings (730 hours)', offer.monthly.monthly_730h_gross_savings],
    ['Monthly net savings (730 hours)', offer.monthly.monthly_730h_net_savings],
    ['Monthly covered on-demand cost (730 hours)', offer.monthly.monthly_730h_covered_on_demand_cost],
    ['Upfront cost (one-time)', offer.upfront_one_time_cost],
  ];
  const list = node('dl');
  for (const [label, value] of values) list.append(node('dt', label), node('dd', value ?? 'Unknown'));
  section.append(list);
  return section;
}

function resultView(result: RecommendationArcheraComparison): HTMLElement {
  const root = node('div');
  root.append(node('p', result.status === 'resource_associated'
    ? 'Resource association found. Comparable economics remain unavailable.'
    : `Association unavailable: ${result.reason.replace(/_/g, ' ')}`));
  root.append(node('p', `Fetched at ${result.fetched_at}`));
  root.append(node('p', result.currency_note));
  root.append(node('p', 'Currency: unknown. Comparable cost difference: unavailable.'));
  root.append(node('p', 'The stored hourly amount has already passed through floating-point conversion. Original provider precision is unproven; these are native vendor figures, not an equivalent-cost quote.'));
  root.append(node('p', `Premium included: ${result.premium_included ? 'Yes' : 'No'}`));
  root.append(node('p', result.non_gating_disclosure), node('p', result.sponsorship_disclosure));
  if (result.current) root.append(offerView(result.current, 'Current vendor offer'));
  for (const [index, candidate] of result.candidates.entries()) {
    root.append(node('p', `Alternative ${index + 1}: ${candidate.status.replace(/_/g, ' ')}. ${candidate.reason.replace(/_/g, ' ')}`));
    root.append(node('p', 'Alternative native product identity is unavailable; current-offer dimensions are not inherited.'));
    if (candidate.figures) root.append(offerView(candidate.figures, `Vendor alternative ${index + 1}`));
  }
  return root;
}

async function compare(id: string, button: HTMLButtonElement): Promise<void> {
  if (pending.has(id) || !canAccess('view', 'recommendations')) return;
  pending.add(id);
  const modal = node('div');
  modal.className = 'modal hidden';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-label', 'Archera recommendation alternatives');
  const panel = node('div');
  panel.className = 'modal-content modal-wide';
  const close = node('button', 'Close');
  close.type = 'button';
  close.className = 'btn';
  close.addEventListener('click', () => closeModal(modal));
  const content = node('div', 'Loading Archera alternatives');
  content.setAttribute('aria-live', 'polite');
  panel.append(node('h2', 'Archera recommendation alternatives'), close, content);
  modal.append(panel);
  document.body.append(modal);
  openModal(modal, { onClose: () => { button.disabled = false; modal.remove(); } });
  button.disabled = true;
  try {
    const result = await getRecommendationInsuranceComparison(id);
    if (result.recommendation_id !== id) throw Object.assign(new Error('mismatched recommendation'), { status: 409 });
    content.replaceChildren(resultView(result));
  } catch (error) {
    content.textContent = failureText(error);
  } finally {
    pending.delete(id);
    button.disabled = false;
  }
}

export async function mountRecommendationArchera(root: HTMLElement): Promise<void> {
  if (!canAccess('view', 'recommendations')) return;
  try {
    if (!(await getInsuranceStatus()).configured) return;
  } catch {
    return;
  }
  for (const row of root.querySelectorAll<HTMLTableRowElement>('tr.recommendation-row[data-rec-id]')) {
    const id = row.dataset['recId'];
    if (!id || row.querySelector('[data-action="archera"]')) continue;
    const button = node('button', 'Archera alternatives');
    button.type = 'button';
    button.className = 'btn btn-small';
    button.dataset['action'] = 'archera';
    button.addEventListener('click', event => {
      event.stopPropagation();
      void compare(id, button);
    });
    row.cells[0]?.append(button);
  }
}
