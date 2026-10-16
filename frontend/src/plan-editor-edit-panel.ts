import type { PlanEditorResponse, PlanEditorDisplay } from './api/plan-editor';
import { adaptPlanEditor, editorServiceTargets } from './plan-editor-adapter';
import { canDraftStep, canReviewEditorDraft, EditorDraft, draftBlock, draftDateChanges, redistributeEditorBlock, resetEditorDraft, selectDraftStep, setApprovedDraftEdits, setDraftDate } from './lib/plan-editor-draft';
import { renderSchedulePlot, schedulePlotFrame, updateSchedulePlot } from './plan-schedule-plot';
import { escapeHtml } from './utils';
import { planEditorReason } from './lib/plan-editor-reasons';

export function mountPlanEditorEditPanel(host: HTMLElement, response: PlanEditorResponse): () => void {
  let state = adaptPlanEditor(response).graphDraft, dirty: 'date' | 'range' | null = null;
  const services = Object.entries(response.plan.services ?? {});
  host.hidden = false;
  host.closest('.plan-editor-layout')?.classList.add('has-editor-draft');
  host.innerHTML = `<h3 tabindex="-1" data-edit="heading">Purchase schedule</h3>
    <p class="editor-draft-notice" id="plan-editor-save-unavailable">Local date proposals are not saved. Saving changes is not available in this local editor yet.</p>
    <p>${escapeHtml(response.plan.name)}</p>
    <label>Stored service for coverage display<select data-edit="service"></select></label>
    <p class="help-text" data-edit="target"></p><div class="schedule-preview-scroll" tabindex="0" aria-label="Stored calendar and local date proposals"></div>
    <fieldset data-edit="controls"><legend>Local date proposals</legend>
      <label><input type="checkbox" data-edit="approved"> Allow edits to eligible approved future steps</label>
      <label>Step<select data-edit="step"></select></label><label>Date (UTC)<input type="date" data-edit="date"></label>
      <p class="help-text" data-edit="selected"></p>
      <div class="form-row"><label>Block start (UTC)<input type="date" data-edit="start"></label>
        <label data-edit="end-label">Block end (UTC)<input type="date" data-edit="end"></label></div>
      <button type="button" data-edit="reset">Reset local date proposals</button>
      <p role="alert" data-edit="error"></p><p role="status" data-edit="status"></p>
    </fieldset>
    <details open><summary>All logical steps</summary><ul data-edit="slots"></ul></details>
    <details><summary>Local before and after changes</summary><ul data-edit="changes"></ul></details>
    <details><summary>Retained execution history</summary><ul data-edit="history"></ul></details>
    <details><summary>Stored settings and accounts</summary><div data-edit="settings"></div></details>`;
  const field = <T extends HTMLElement>(name: string): T => host.querySelector<T>(`[data-edit="${name}"]`)!;
  const service = field<HTMLSelectElement>('service'), step = field<HTMLSelectElement>('step');
  const date = field<HTMLInputElement>('date'), start = field<HTMLInputElement>('start'), end = field<HTMLInputElement>('end');
  const approved = field<HTMLInputElement>('approved'), plot = host.querySelector<HTMLElement>('.schedule-preview-scroll')!;
  service.replaceChildren(...services.map(([key, item]) => new Option(`${item.provider} / ${item.service}${item.enabled ? '' : ' (disabled)'}`, key)));
  service.disabled = services.length === 0;
  const list = (name: string, values: readonly string[]): void => field(name).replaceChildren(...values.map(value => {
    const item = document.createElement('li'); item.textContent = value; return item;
  }));
  const filters = (value: readonly string[] | null): string => value === null ? 'unspecified' : value.length ? value.join(', ') : 'none';
  field('settings').textContent = `Plan ${response.plan.enabled ? 'enabled' : 'disabled'}; automatic purchase ${response.plan.auto_purchase ? 'on' : 'off'}; notification lead ${response.plan.notification_days_before} days. Target accounts: ${response.account_ids.map(id => response.accounts.find(account => account.id === id)?.name ?? id).join(', ') || 'none'}.`;
  const ramp = response.plan.ramp_schedule, rampInfo = document.createElement('p');
  rampInfo.textContent = `Stored ramp: ${ramp.type}; ${ramp.percent_per_step}% per step; ${ramp.step_interval_days} day interval; step ${ramp.current_step} of ${ramp.total_steps}; start ${ramp.start_date ?? 'unavailable'}; commitment cap ${response.plan.max_step_commitment ?? 'unavailable'}.`;
  field('settings').appendChild(rampInfo);
  services.forEach(([, item]) => {
    const details = document.createElement('p');
    details.textContent = `${item.provider}/${item.service}: ${item.term > 0 ? item.term + ' year(s)' : 'term unavailable'}, ${item.payment || 'payment unavailable'}, coverage ${item.coverage}%, ramp ${item.ramp_schedule}, minimum count ${item.min_count}. Engine include/exclude: ${filters(item.include_engines)} / ${filters(item.exclude_engines)}. Region include/exclude: ${filters(item.include_regions)} / ${filters(item.exclude_regions)}. Type include/exclude: ${filters(item.include_types)} / ${filters(item.exclude_types)}.`;
    field('settings').appendChild(details);
  });
  list('history', response.history.map(row => `Step ${row.step_number}; ${row.status}; date ${row.scheduled_date ?? 'unavailable'}; attempt ${row.retry_attempt_n}; approval ${row.approval_state}.`));
  if (!response.history.length) list('history', ['No retained executions.']);
  const message = (value: string): void => { field('error').textContent = value; };
  const paintPlot = (draft: EditorDraft): void => {
    const targets = services.length ? editorServiceTargets(response, service.value) : null;
    const supported = targets !== null && targets.every(target => Number.isFinite(target) && target >= 0 && target <= 100);
    const points = draft.dates.flatMap((value, index) => value === null || !supported ? [] : [{
      step: index + 1, date: value, target: targets![index]!,
    }]);
    field('target').textContent = supported
      ? `Cumulative coverage (%) for ${service.selectedOptions[0]!.textContent}. Stored ramp share × service coverage. All dates use UTC.`
      : 'Coverage plot unavailable for this stored service. Dates and all logical steps remain available.';
    if (!points.length) {
      plot.textContent = supported ? 'No dated points to place on the calendar. Enter eligible missing dates explicitly below.' : 'The selected service has no supported coverage plot.';
      return;
    }
    const frame = schedulePlotFrame(points.map(point => point.date)), svg = plot.querySelector<SVGSVGElement>('svg');
    if (svg && svg.querySelectorAll('[data-preview-step]').length === points.length) updateSchedulePlot(svg, points, frame, draft.selected, false, null);
    else plot.innerHTML = renderSchedulePlot(points, frame, draft.selected, false, null);
    plot.querySelector('svg')!.setAttribute('aria-label', 'Stored schedule with local date proposals');
  };
  const paintRangeValues = (draft: EditorDraft, block: readonly [number, number] | null): void => {
    if (dirty === 'range') return;
    start.value = block ? draft.dates[block[0]] ?? '' : '';
    end.value = block ? draft.dates[block[1]] ?? '' : '';
  };
  const paintRange = (draft: EditorDraft): void => {
    const block = draftBlock(draft), complete = block !== null && draft.dates.slice(block[0], block[1] + 1).every(value => value !== null);
    const single = block !== null && block[0] === block[1];
    start.disabled = !complete || dirty === 'date'; end.disabled = !complete || dirty === 'date' || single;
    field('end-label').hidden = single;
    paintRangeValues(draft, block);
  };
  const paintSelection = (draft: EditorDraft): void => {
    if (!step.options.length) step.replaceChildren(...draft.dates.map((_, index) => new Option(`Step ${index + 1}`, String(index))));
    step.value = String(draft.selected); step.disabled = dirty !== null;
    approved.checked = draft.approvedEdits; approved.disabled = dirty !== null;
    approved.parentElement!.hidden = !response.graph!.steps.some(slot => slot.capability === 'approved_toggle_required' && slot.requires_approved_edits);
    date.disabled = !canDraftStep(draft, draft.selected) || dirty === 'range';
    if (dirty !== 'date') date.value = draft.dates[draft.selected] ?? '';
  };
  const quantityText = (display: PlanEditorDisplay | null): string => {
    if (display?.quantity_source !== 'stored_snapshot' || display.quantity === null || !display.quantity_unit) return 'Quantity unavailable.';
    return `Stored snapshot quantity: ${display.quantity} ${display.quantity_unit}; not a fulfilled total.`;
  };
  const paintDescription = (draft: EditorDraft): void => {
    const selected = response.graph!.steps[draft.selected]!;
    field('selected').textContent = `${canDraftStep(draft, draft.selected) ? 'Local date proposal allowed; server verification still required.' : planEditorReason(selected.lock_reason, 'This step is fixed.')} ${quantityText(selected.display)} Amount unavailable.`;
    list('slots', response.graph!.steps.map((slot, index) => `Step ${slot.step_number}: ${draft.dates[index] ?? 'date unavailable'}; ramp share ${slot.cumulative_target_percent}%; ${slot.status ?? (slot.root_state === 'missing' ? 'no current root' : 'status unavailable')}. ${canDraftStep(draft, index) ? 'Local proposal enabled.' : planEditorReason(slot.lock_reason, `${slot.capability.replace(/_/g, ' ')}.`)}`));
    const changes = draftDateChanges(draft);
    list('changes', changes.map(change => `Step ${change.step}: ${change.before ?? 'no date'} → ${change.after ?? 'no date'}${change.requiresReapproval ? '; reapproval required' : ''}`));
    field('status').textContent = !draft.approvedEdits && changes.some(change => change.requiresReapproval)
      ? 'Approved date proposals remain visible. Re-enable approved edits or reset them before Review.'
      : canReviewEditorDraft(draft) ? 'Local date checks pass. Nothing has been saved or approved.' : 'Complete eligible dates and resolve date constraints before Review.';
  };
  const paint = (): void => {
    field('controls').hidden = state === null;
    if (!state || !response.graph) {
      plot.textContent = `Calendar unavailable: ${response.graph_unavailable_reason ?? 'this plan has no supported graph'}.`;
      list('slots', ['No logical graph is available. Stored settings and history remain shown.']);
      return;
    }
    paintPlot(state); paintSelection(state); paintRange(state); paintDescription(state);
  };
  const commit = (): void => {
    if (!state) return;
    try {
      state = dirty === 'range' ? redistributeEditorBlock(state, start.value, end.value) : setDraftDate(state, state.selected, date.value);
      dirty = null; message(''); paint();
    } catch (error) { message((error as Error).message); paint(); }
  };
  const input = (event: Event): void => {
    if (event.target === date) dirty = 'date';
    else if (event.target === start || event.target === end) dirty = 'range';
    else return;
    paint();
  };
  const change = (event: Event): void => {
    if (event.target === service) { paint(); return; }
    if (!state) return;
    if (event.target === step && !dirty) state = selectDraftStep(state, Number(step.value));
    else if (event.target === approved && !dirty) state = setApprovedDraftEdits(state, approved.checked);
    else if ([date, start, end].includes(event.target as HTMLInputElement)) { commit(); return; }
    paint();
  };
  const keydown = (event: KeyboardEvent): void => {
    if (event.key === 'Enter' && [date, start, end].includes(event.target as HTMLInputElement)) { event.preventDefault(); commit(); }
  };
  field<HTMLButtonElement>('reset').onclick = () => { if (state) { state = resetEditorDraft(state); dirty = null; message(''); paint(); } };
  host.addEventListener('input', input); host.addEventListener('change', change); host.addEventListener('keydown', keydown);
  paint();
  return () => {
    host.removeEventListener('input', input); host.removeEventListener('change', change); host.removeEventListener('keydown', keydown);
    host.replaceChildren(); host.hidden = true; host.closest('.plan-editor-layout')?.classList.remove('has-editor-draft');
    state = null;
  };
}
