import type { PlanEditorFieldRule, PlanEditorResponse } from '../api/plan-editor';
import { planEditorReason } from './plan-editor-reasons';
import { isSavingsPlanService, resolvePlanService } from './plan-service-ids';

export interface EditFormHooks {
  readonly selectService: (provider: string, service: string, term: number, payment: string, serviceLabel?: string) => void;
  readonly setAccounts: (accounts: readonly { readonly id: string; readonly name: string }[]) => void;
  readonly onAccountsChange: (listener: () => void) => () => void;
}

export interface EditFormPatch {
  readonly plan: Readonly<Record<string, string | number | boolean>>;
  readonly services: Readonly<Record<string, Readonly<Record<string, string | number>>>>;
  readonly target_accounts?: readonly string[];
}
export interface EditFormModel {
  patch(): EditFormPatch;
  onChange(listener: () => void): () => void;
}
export interface EditFormHandle { readonly model: EditFormModel; dispose(): void }

const lockedByDefault: PlanEditorFieldRule = { editable: false, reason: 'unsupported_field', requires_approved_edits: false, requires_proposed_value_revalidation: false };
const PLAN_FIELDS = [['name', 'plan-name'], ['enabled', 'plan-enabled'], ['auto_purchase', 'plan-auto-purchase'], ['notification_days_before', 'plan-notify-days']] as const;
const SERVICE_FIELDS = [['provider', 'plan-provider'], ['service', 'plan-service'], ['term', 'plan-term'], ['payment', 'plan-payment'], ['coverage', 'plan-coverage']] as const;
const NUMERIC = new Set(['term', 'coverage']);
const SAVINGS_PLANS_NOTE = ' (Savings Plans are not supported yet)';

function restoreSavingsPlanOptions(select: HTMLSelectElement | null): void {
  select?.querySelectorAll<HTMLOptionElement>('option[data-sp-label]').forEach(option => {
    option.textContent = option.dataset['spLabel']!; option.disabled = false; delete option.dataset['spLabel'];
  });
}

const RAMP_PRESETS: ReadonlyArray<readonly [string, number, number]> = [['immediate', 100, 0], ['weekly-25pct', 25, 7], ['monthly-10pct', 10, 30]];
const RAMP_UNAVAILABLE = 'Changing the ramp of an existing plan is not available yet';

function fillEditRamp(form: HTMLFormElement, ramp: PlanEditorResponse['plan']['ramp_schedule']): HTMLInputElement {
  const equal = ramp.allocation_mode === 'equal_count';
  const preset = ramp.type === 'immediate' ? 'immediate' : equal ? 'custom' : RAMP_PRESETS.find(([, percent, days]) => percent === ramp.percent_per_step && days === ramp.step_interval_days)?.[0] ?? 'custom';
  const radios = Array.from(form.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"]'));
  radios.forEach(radio => { radio.checked = radio.value === preset; });
  const field = (id: string): HTMLInputElement => form.querySelector<HTMLInputElement>(`#${id}`)!;
  field('ramp-step-percent').value = String(equal ? Math.round(ramp.percent_per_step * 100) / 100 : ramp.percent_per_step); field('ramp-interval-days').value = String(ramp.step_interval_days);
  form.querySelector('#custom-ramp-config')?.classList.toggle('hidden', preset !== 'custom');
  return radios.find(radio => radio.checked)!;
}

function resetEditRamp(form: HTMLFormElement): void {
  form.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"],#ramp-step-percent,#ramp-interval-days').forEach(item => {
    item.checked = item.defaultChecked; item.value = item.defaultValue;
  });
  form.querySelector('#custom-ramp-config')?.classList.add('hidden');
  const group = form.querySelector('.ramp-options');
  ['role', 'aria-labelledby', 'aria-describedby'].forEach(name => group?.removeAttribute(name));
}

export function fillEditForm(form: HTMLFormElement, response: PlanEditorResponse, hooks: EditFormHooks): EditFormHandle {
  const control = (id: string): HTMLInputElement => {
    const found = form.querySelector<HTMLInputElement>(`#${id}`);
    if (!found) throw new Error(`Plan form control #${id} is missing.`);
    return found;
  };
  const wasDisabled = new Map(Array.from(form.querySelectorAll<HTMLInputElement>('input,select,textarea')).map(item => [item, item.disabled]));
  const planReasons: HTMLElement[] = [], serviceReasons: HTMLElement[] = [];
  let rampRadio: HTMLInputElement | undefined;
  const listeners = new Set<() => void>(), cleanups: Array<() => void> = [];
  const clear = (bucket: HTMLElement[]): void => bucket.splice(0).forEach(reason => {
    document.getElementById(reason.id.replace(/-reason$/, ''))?.removeAttribute('aria-describedby'); reason.remove();
  });
  const labelOf = (item: HTMLElement): string => Array.from(item.closest('label')?.childNodes ?? []).filter(node => node.nodeType === Node.TEXT_NODE)
    .map(node => node.textContent ?? '').join('').trim().replace(/:$/, '');
  const apply = (item: HTMLInputElement, rule: PlanEditorFieldRule, bucket: HTMLElement[], anchor?: Element | null, label?: string, sentence?: string): void => {
    item.disabled = !rule.editable;
    if (rule.editable) return;
    const reason = document.createElement('p');
    reason.id = `${item.id}-reason`; reason.className = 'help-text';
    const text = sentence ?? planEditorReason(rule.reason, planEditorReason(rule.requires_approved_edits ? 'approved_edits_disabled' : 'unsupported_field', ''));
    reason.textContent = label ? `${label}: ${text}` : text;
    item.setAttribute('aria-describedby', reason.id);
    (anchor ?? item.closest('.setting-row') ?? item.closest('.form-row') ?? item.closest('label') ?? item).after(reason);
    bucket.push(reason);
  };
  const dispose = (): void => {
    cleanups.splice(0).forEach(cleanup => cleanup());
    clear(planReasons); clear(serviceReasons); restoreSavingsPlanOptions(form.querySelector<HTMLSelectElement>('#plan-service'));
    if (rampRadio) { rampRadio.removeAttribute('id'); resetEditRamp(form); }
    wasDisabled.forEach((value, item) => { item.disabled = value; });
  };
  try {
    const { plan, field_policy: policy } = response;
    const services = plan.services ?? {}, edits: Record<string, Record<string, string>> = {}, baseline: Record<string, string> = {};
    let current: string | undefined = Object.keys(services)[0];
    const accountIds = (): string[] => control('plan-account-ids').value.split(',').filter(Boolean);
    const patch = (): EditFormPatch => {
      const stored: Record<string, string | number | boolean> = { name: plan.name, enabled: plan.enabled, auto_purchase: plan.auto_purchase, notification_days_before: plan.notification_days_before };
      const planPatch: Record<string, string | number | boolean> = {};
      for (const [key, id] of PLAN_FIELDS) {
        const item = control(id), value = typeof stored[key] === 'boolean' ? item.checked : key === 'notification_days_before' ? Number(item.value) : item.value;
        if (value !== stored[key]) planPatch[key] = value;
      }
      const servicePatch: Record<string, Record<string, string | number>> = {};
      for (const [key, fields] of Object.entries(edits)) {
        const entries = Object.entries(fields).map(([field, value]) => [field, NUMERIC.has(field) ? Number(value) : value] as const);
        if (entries.length) servicePatch[key] = Object.fromEntries(entries);
      }
      const ids = accountIds(), same = ids.length === response.account_ids.length && ids.every(id => response.account_ids.includes(id));
      return same ? { plan: planPatch, services: servicePatch } : { plan: planPatch, services: servicePatch, target_accounts: ids };
    };
    let last = '';
    const notify = (): void => {
      const signature = JSON.stringify(patch());
      if (signature === last) return;
      last = signature; listeners.forEach(listener => listener());
    };
    const paint = (key: string): void => {
      clear(serviceReasons);
      const item = services[key]!, resolved = resolvePlanService(item.provider, item.service);
      const unsupported = resolved.state === 'unsupported';
      const show = (provider: string, service: string, term: number, payment: string): void => hooks.selectService(provider, service, term, payment,
        unsupported ? `Unsupported: ${item.provider}/${item.service} (read-only)` : undefined);
      show(item.provider, resolved.canonical ?? item.service, item.term, item.payment);
      control('plan-coverage').value = String(item.coverage);
      for (const [field, id] of SERVICE_FIELDS) baseline[field] = control(id).value;
      const edit = edits[key];
      if (edit && Object.keys(edit).length) {
        show(edit['provider'] ?? baseline['provider']!, edit['service'] ?? baseline['service']!, Number(edit['term'] ?? baseline['term']), edit['payment'] ?? baseline['payment']!);
        control('plan-coverage').value = edit['coverage'] ?? baseline['coverage']!;
      }
      const blocked = unsupported ? 'This service is not supported by the plan editor.' : isSavingsPlanService(item.service) ? 'Savings Plans are not supported yet.' : undefined;
      for (const [field, id] of SERVICE_FIELDS) apply(control(id), blocked ? lockedByDefault : policy.services[key]?.[field] ?? lockedByDefault, serviceReasons, undefined, labelOf(control(id)), blocked);
      const picker = control('plan-service') as unknown as HTMLSelectElement;
      restoreSavingsPlanOptions(picker);
      if (!picker.disabled) picker.querySelectorAll<HTMLOptionElement>('option').forEach(option => {
        if (!isSavingsPlanService(option.value)) return;
        option.dataset['spLabel'] = option.textContent ?? ''; option.textContent += SAVINGS_PLANS_NOTE; option.disabled = true;
      });
    };
    control('plan-name').value = plan.name;
    control('plan-enabled').checked = plan.enabled;
    control('plan-auto-purchase').checked = plan.auto_purchase;
    control('plan-notify-days').value = String(plan.notification_days_before);
    hooks.setAccounts(response.account_ids.map(id => ({ id, name: response.accounts.find(account => account.id === id)?.name ?? id })));
    control('plan-account-ids').value = response.account_ids.join(',');
    control('plan-max-step-commitment').value = plan.max_step_commitment === null ? 'No cap' : String(plan.max_step_commitment);
    form.querySelectorAll<HTMLInputElement>('input,select,textarea,.selected-accounts button').forEach(item => { item.disabled = true; });
    for (const [key, id] of PLAN_FIELDS) apply(control(id), policy.controls[key] ?? lockedByDefault, planReasons);
    const accounts = policy.controls['target_accounts'] ?? lockedByDefault;
    apply(control('plan-account-search'), accounts, planReasons, form.querySelector('.plan-accounts-search'), 'Target accounts');
    form.querySelectorAll<HTMLButtonElement>('.selected-accounts button').forEach(button => { button.disabled = !accounts.editable; });
    rampRadio = fillEditRamp(form, plan.ramp_schedule); rampRadio.id = 'plan-ramp-schedule';
    const rampLock = policy.controls['step_count'];
    const group = form.querySelector('.ramp-options')!, ramp = plan.ramp_schedule;
    apply(rampRadio, rampLock && !rampLock.editable ? rampLock : lockedByDefault, planReasons, group, 'Ramp-up schedule', rampLock && !rampLock.editable ? undefined : RAMP_UNAVAILABLE);
    const rampReason = planReasons[planReasons.length - 1]!;
    if (ramp.allocation_mode === 'equal_count') rampReason.textContent += `${rampReason.textContent!.endsWith('.') ? '' : '.'} Equal-count ramp: ${ramp.total_steps} ${ramp.total_steps === 1 ? 'step' : 'steps'}.`;
    rampRadio.removeAttribute('aria-describedby');
    group.setAttribute('role', 'radiogroup'); group.setAttribute('aria-labelledby', 'plan-ramp-heading'); group.setAttribute('aria-describedby', rampReason.id);
    if (current) paint(current);
    last = JSON.stringify(patch());
    const record = (): void => {
      if (current) for (const [field, id] of SERVICE_FIELDS) {
        const item = control(id), fields = edits[current] ??= {};
        if (item.disabled || item.value === baseline[field]) delete fields[field]; else fields[field] = item.value;
      }
      notify();
    };
    const pick = (event: Event): void => {
      const target = event.target as HTMLElement;
      if (!target.matches('[data-edit="service"]')) return;
      const key = (target as HTMLSelectElement).value;
      if (key in services) { current = key; paint(key); notify(); }
    };
    const layout = form.closest('.plan-editor-layout');
    form.addEventListener('input', record); form.addEventListener('change', record); layout?.addEventListener('change', pick);
    cleanups.push(() => { form.removeEventListener('input', record); form.removeEventListener('change', record); layout?.removeEventListener('change', pick); }, hooks.onAccountsChange(notify));
    return { dispose, model: { patch, onChange: listener => { listeners.add(listener); return () => { listeners.delete(listener); }; } } };
  } catch (error) { dispose(); throw error; }
}
