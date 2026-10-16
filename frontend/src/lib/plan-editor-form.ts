import type { PlanEditorFieldRule, PlanEditorResponse } from '../api/plan-editor';
import { planEditorReason } from './plan-editor-reasons';

export interface EditFormHooks {
  readonly selectService: (provider: string, service: string, term: number, payment: string) => void;
  readonly setAccounts: (accounts: readonly { readonly id: string; readonly name: string }[]) => void;
}

const lockedByDefault: PlanEditorFieldRule = { editable: false, reason: 'unsupported_field', requires_approved_edits: false, requires_proposed_value_revalidation: false };

export function fillEditForm(form: HTMLFormElement, response: PlanEditorResponse, hooks: EditFormHooks): () => void {
  const control = (id: string): HTMLInputElement => {
    const found = form.querySelector<HTMLInputElement>(`#${id}`);
    if (!found) throw new Error(`Plan form control #${id} is missing.`);
    return found;
  };
  const all = Array.from(form.querySelectorAll<HTMLInputElement>('input,select,textarea'));
  const wasDisabled = new Map(all.map(item => [item, item.disabled]));
  const reasons: HTMLElement[] = [];
  const undo = (): void => {
    reasons.forEach(reason => { document.getElementById(reason.id.replace(/-reason$/, ''))?.removeAttribute('aria-describedby'); reason.remove(); });
    wasDisabled.forEach((value, item) => { item.disabled = value; });
  };
  try {
    const { plan, field_policy: policy } = response;
    control('plan-name').value = plan.name;
    control('plan-enabled').checked = plan.enabled;
    control('plan-auto-purchase').checked = plan.auto_purchase;
    control('plan-notify-days').value = String(plan.notification_days_before);
    const stored = Object.values(plan.services ?? {})[0];
    if (stored) {
      hooks.selectService(stored.provider, stored.service, stored.term, stored.payment);
      control('plan-coverage').value = String(stored.coverage);
    }
    hooks.setAccounts(response.account_ids.map(id => ({ id, name: response.accounts.find(account => account.id === id)?.name ?? id })));
    const cap = control('plan-max-step-commitment');
    cap.value = plan.max_step_commitment === null ? 'No cap' : String(plan.max_step_commitment);
    form.querySelectorAll<HTMLInputElement>('input,select,textarea,.selected-accounts button').forEach(item => { item.disabled = true; });
    for (const [key, id] of [['name', 'plan-name'], ['enabled', 'plan-enabled'], ['auto_purchase', 'plan-auto-purchase'], ['notification_days_before', 'plan-notify-days']] as const) {
      const rule = policy.controls[key] ?? lockedByDefault, item = control(id);
      item.disabled = !rule.editable;
      if (rule.editable) continue;
      const reason = document.createElement('p');
      reason.id = `${id}-reason`; reason.className = 'help-text';
      reason.textContent = planEditorReason(rule.reason, rule.requires_approved_edits ? planEditorReason('approved_edits_disabled', '') : planEditorReason('unsupported_field', ''));
      item.setAttribute('aria-describedby', reason.id);
      (item.closest('.setting-row') ?? item.closest('label') ?? item).after(reason);
      reasons.push(reason);
    }
  } catch (error) { undo(); throw error; }
  return undo;
}
