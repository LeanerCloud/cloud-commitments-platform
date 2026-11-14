import { confirmDialog } from '../confirmDialog';
import { isValidCombination } from '../commitmentOptions';
import { updateDirtyMarkers } from './settings-dirty';
import { paymentLabel, termLabel } from './settings-fields';
import { SERVICE_FIELDS } from './settings-state';

/**
 * Propagate default term to all service-specific term selects
 */
function propagateTermToServices(term: string): void {
  SERVICE_FIELDS.forEach(({ termId }) => {
    const select = document.getElementById(termId) as HTMLSelectElement | null;
    if (select) {
      select.value = term;
    }
  });
  // Propagation changes term, which may invalidate each service's current
  // payment (e.g., RDS jumping from 1yr to 3yr while "No Upfront" was set).
  // Re-apply per-service constraints to keep the UI honest.
  syncAllServiceCommitmentConstraints();
}

/**
 * Propagate default payment option to all service-specific payment selects
 * Note: Only AWS services have payment options; Azure/GCP use full upfront only
 */
function propagatePaymentToServices(payment: string): void {
  SERVICE_FIELDS
    .filter(f => f.provider === 'aws' && f.paymentId !== null)
    .forEach(({ paymentId }) => {
      const select = document.getElementById(paymentId!) as HTMLSelectElement | null;
      if (select) {
        select.value = payment;
      }
    });
  // If the propagated payment is invalid for any service's current term
  // (e.g., "No Upfront" against RDS 3yr), clamp to the first valid option
  // on that service instead of silently persisting a combo the provider
  // will reject.
  syncAllServiceCommitmentConstraints();
}

/**
 * Apply per-service commitment restrictions to a single service's payment
 * dropdown. AWS services like RDS/ElastiCache/OpenSearch/Redshift don't
 * accept 3-year No-Upfront — commitmentOptions.ts encodes which (term,
 * payment) pairs each service rejects. We hide + disable the invalid
 * option rows so the Settings UI can't save a combination the backend
 * would reject. If the currently-selected payment becomes invalid after
 * a term change, it auto-clamps to the first valid option so the form
 * never holds an unsavable value.
 *
 * Azure and GCP service-default cards are out of scope here — Azure has
 * only two payment options (neither restricted by term) and GCP has no
 * payment selector at all.
 */
export function syncPaymentConstraintsForService(field: typeof SERVICE_FIELDS[number]): void {
  if (field.provider !== 'aws' || !field.paymentId) return;
  const termEl = document.getElementById(field.termId) as HTMLSelectElement | null;
  const paymentEl = document.getElementById(field.paymentId) as HTMLSelectElement | null;
  if (!termEl || !paymentEl) return;

  const term = parseInt(termEl.value || '3', 10);
  let firstValidOption: HTMLOptionElement | null = null;
  for (const opt of Array.from(paymentEl.options)) {
    const valid = isValidCombination(field.provider, field.service, term, opt.value);
    // Belt-and-suspenders: `hidden` removes the option from the rendered
    // dropdown, `disabled` prevents keyboard / programmatic selection in
    // the rare browsers where a hidden <option> can still receive focus.
    opt.hidden = !valid;
    opt.disabled = !valid;
    if (valid && !firstValidOption) firstValidOption = opt;
  }
  const currentOpt = paymentEl.options[paymentEl.selectedIndex];
  if (currentOpt?.disabled && firstValidOption) {
    paymentEl.value = firstValidOption.value;
  }
}

/**
 * Re-apply commitment constraints across every service. Cheap (O(services
 * × payments)), called on settings load and after any bulk write that
 * touches term/payment values (reset, propagation).
 */
export function syncAllServiceCommitmentConstraints(): void {
  SERVICE_FIELDS.forEach(syncPaymentConstraintsForService);
}

// Human-readable display names for each (provider, service) tuple,
// keyed to match the SERVICE_FIELDS constant. Source of truth: the
// <h5> titles on each Service Default card in index.html. Used by
// buildAffectedList so the "Propagate to N services" confirmation
// pop-up reads "AWS EC2 Reserved Instances" instead of the backend
// identifier "aws ec2" (#467).
//
// Missing entries fall back to a UPPER + raw service slug, which is
// strictly an improvement-of-niceness — anything missing here renders
// the way the old code rendered everything. Keep this table in sync
// when SERVICE_FIELDS grows.
const SERVICE_DISPLAY_NAMES: Record<string, Record<string, string>> = {
  aws: {
    'ec2': 'EC2 Reserved Instances',
    'rds': 'RDS Reserved Instances',
    'elasticache': 'ElastiCache Reserved Nodes',
    'opensearch': 'OpenSearch Reserved Instances',
    'redshift': 'Redshift Reserved Nodes',
    'savings-plans-compute': 'Compute Savings Plans',
    'savings-plans-ec2instance': 'EC2 Instance Savings Plans',
    'savings-plans-sagemaker': 'SageMaker Savings Plans',
    'savings-plans-database': 'Database Savings Plans',
  },
  azure: {
    'compute': 'Virtual Machine Reservations',
    'relational-db': 'SQL Database Reservations',
    'nosql': 'CosmosDB Reservations',
    'cache': 'Redis Cache Reservations',
    'search': 'Cognitive Search Reservations',
  },
  gcp: {
    'compute': 'Compute Engine CUDs',
    'relational-db': 'Cloud SQL CUDs',
    'cache': 'Memorystore CUDs',
  },
};

function serviceDisplayName(provider: string, service: string): string {
  const providerLabel = provider.toUpperCase();
  const display = SERVICE_DISPLAY_NAMES[provider]?.[service];
  return display ? `${providerLabel} ${display}` : `${providerLabel} ${service}`;
}

function buildAffectedList(affected: { provider: string; service: string }[]): HTMLDivElement {
  // Build the diff list via DOM APIs (not innerHTML) so we don't need an
  // escapeHtml import; provider + service values come from the SERVICE_FIELDS
  // constant so they're inherently safe, but constructor-free DOM is the
  // simpler contract.
  const wrap = document.createElement('div');
  const count = affected.length;
  const intro = document.createElement('p');
  intro.textContent = `${count} service${count === 1 ? '' : 's'} will change:`;
  wrap.appendChild(intro);
  const ul = document.createElement('ul');
  affected.forEach((f) => {
    const li = document.createElement('li');
    li.textContent = serviceDisplayName(f.provider, f.service);
    ul.appendChild(li);
  });
  wrap.appendChild(ul);
  return wrap;
}

export async function confirmAndPropagateTerm(select: HTMLSelectElement): Promise<void> {
  const previousValue = select.dataset['previous'] ?? select.value;
  const newValue = select.value;
  if (newValue === previousValue) return;
  const affected = SERVICE_FIELDS.filter(({ termId }) => {
    const svc = document.getElementById(termId) as HTMLSelectElement | null;
    return svc && svc.value !== newValue;
  });
  if (affected.length === 0) {
    // Everything already matches — nothing to propagate, no need to prompt.
    select.dataset['previous'] = newValue;
    return;
  }
  const ok = await confirmDialog({
    title: `Apply "${termLabel(newValue)}" to ${affected.length} service${affected.length === 1 ? '' : 's'}?`,
    body: buildAffectedList(affected),
    confirmLabel: 'Apply to all',
  });
  if (ok) {
    propagateTermToServices(newValue);
    select.dataset['previous'] = newValue;
    updateDirtyMarkers();
  } else {
    // User cancelled — restore the default select to its prior value so
    // the visible state matches what's persisted/saved. Re-run the
    // dirty-marker diff (setting .value via JS doesn't fire 'input', so
    // setupDirtyTracking's listener wouldn't catch the revert) so the
    // field highlight and global "Unsaved changes" badge clear when the
    // restored value matches the saved snapshot. Issue #468.
    select.value = previousValue;
    updateDirtyMarkers();
  }
}

export async function confirmAndPropagatePayment(select: HTMLSelectElement): Promise<void> {
  const previousValue = select.dataset['previous'] ?? select.value;
  const newValue = select.value;
  if (newValue === previousValue) return;
  const affected = SERVICE_FIELDS
    .filter(f => f.provider === 'aws' && f.paymentId !== null)
    .filter(({ paymentId }) => {
      const svc = document.getElementById(paymentId!) as HTMLSelectElement | null;
      return svc && svc.value !== newValue;
    });
  if (affected.length === 0) {
    select.dataset['previous'] = newValue;
    return;
  }
  const ok = await confirmDialog({
    title: `Apply "${paymentLabel(newValue)}" to ${affected.length} AWS service${affected.length === 1 ? '' : 's'}?`,
    body: buildAffectedList(affected),
    confirmLabel: 'Apply to all',
  });
  if (ok) {
    propagatePaymentToServices(newValue);
    select.dataset['previous'] = newValue;
    updateDirtyMarkers();
  } else {
    // Mirror the Term branch: re-run the dirty-marker diff after the
    // JS-driven value restore so the visible state matches the saved
    // snapshot. Issue #468.
    select.value = previousValue;
    updateDirtyMarkers();
  }
}
