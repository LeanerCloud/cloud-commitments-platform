import { buildCreateRequest } from './lib/plan-editor-request';
import { canReviewEditorDraft } from './lib/plan-editor-draft';
import { newUuid } from './lib/uuid';
import type { PlanEditorSaveResponse } from './api/plan-editor';
import type { PlanEditorPanelHandle } from './plan-editor-panel';
import { mountReview, type PlanReviewHandle } from './plan-editor-save';

export interface CreateReviewDeps {
  readonly form: HTMLFormElement;
  readonly host: HTMLElement;
  readonly button: HTMLButtonElement;
  readonly panel: PlanEditorPanelHandle;
  readonly accountIds: () => string[];
  // Reason the dialog's current selection cannot be created (for example a Savings Plans service), or ''.
  readonly guard: () => string;
  readonly generation: () => number;
  readonly onSaved: (saved: PlanEditorSaveResponse, open: boolean) => void;
}

// Create goes through the same preview, review and atomic Save as Edit, with expected_revision null and a client-chosen plan id.
// The plan id is fixed for the dialog session, so a second Create after an uncertain failure collides with the first (409) instead of duplicating it;
// only the command id changes when the request does.
export function mountCreateReview(deps: CreateReviewDeps): PlanReviewHandle {
  const { form, panel } = deps;
  const input = (id: string): HTMLInputElement => form.querySelector<HTMLInputElement>(`#${id}`)!;
  const whole = (id: string, label: string, min: number, max: number): number | string => {
    const text = input(id).value.trim(), value = text === '' ? NaN : Number(text);
    return Number.isInteger(value) && value >= min && value <= max ? value : `${label} must be a whole number between ${min} and ${max}.`;
  };
  const planId = newUuid();
  return mountReview({ ...deps, planId: () => planId,
    create: true,
    subscribe: sync => {
      form.addEventListener('input', sync); form.addEventListener('change', sync);
      return [panel.subscribe(sync), () => { form.removeEventListener('input', sync); form.removeEventListener('change', sync); }];
    },
    reload: () => Promise.resolve(true), // the dialog closes on success, so there is nothing to reload
    build: commandId => {
      const guard = deps.guard();
      if (guard) return { blocked: guard };
      const draft = panel.draft(), ramp = panel.ramp();
      if (!draft || !ramp || !canReviewEditorDraft(draft)) {
        return { blocked: deps.host.querySelector('[data-editor="error"]')?.textContent || 'Fix the proposed dates before reviewing.' };
      }
      if (input('plan-service').value === '') return { blocked: 'Choose a service.' };
      if (input('plan-name').value.trim() === '') return { blocked: 'Enter a plan name.' };
      const term = whole('plan-term', 'Term', 1, 10), coverage = whole('plan-coverage', 'Target coverage', 0, 100), notify = whole('plan-notify-days', 'Notification days', 1, 30);
      for (const bad of [term, coverage, notify]) if (typeof bad === 'string') return { blocked: bad };
      const built = buildCreateRequest({
        name: input('plan-name').value, enabled: input('plan-enabled').checked, autoPurchase: input('plan-auto-purchase').checked,
        notifyDays: notify as number, accountIds: deps.accountIds(),
        service: { provider: input('plan-provider').value, service: input('plan-service').value, term: term as number, payment: input('plan-payment').value, coverage: coverage as number },
        ramp: { preset: form.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked')?.value ?? '', intervalDays: ramp.intervalDays, percentPerStep: ramp.percent },
        dates: draft.dates.map(date => date!), commandId,
      });
      return 'unavailable' in built ? { blocked: built.unavailable } : { request: built };
    } });
}
