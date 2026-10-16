import { mountPlanEditorEditPanel, PlanEditorEditHandle } from '../plan-editor-edit-panel';
import { response } from './fixtures/plan-editor';

let host: HTMLElement, handle: PlanEditorEditHandle, svg: SVGSVGElement, captured: boolean, ctmLeft: number;
const field = <T extends HTMLElement>(name: string): T => host.querySelector<T>(`[data-edit="${name}"]`)!;
const point = (index: number): SVGGElement => host.querySelector<SVGGElement>(`[data-preview-step="${index}"]`)!;
const handleOf = (boundary: string): SVGGElement => host.querySelector<SVGGElement>(`[data-preview-boundary="${boundary}"]`)!;
const cx = (index: number): number => Number(point(index).querySelector('circle')!.getAttribute('cx'));
const dayPx = (): number => (cx(2) - cx(1)) / 3;
const drag = (target: Element, from: number, days: number, release = true): void => {
  const at = from + days * dayPx(); send('pointerdown', target, from); send('pointermove', svg, at); if (release) send('pointerup', svg, at);
};
const send = (type: string, target: Element, x: number): void => { target.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }),
  { pointerId: 1, isPrimary: true, button: 0, clientX: x, clientY: 0 })); };
const key = (target: Element, name: string, init: KeyboardEventInit = {}): KeyboardEvent => {
  const event = new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...init }); target.dispatchEvent(event); return event;
};
const stubSvg = (): void => {
  svg = host.querySelector<SVGSVGElement>('svg')!; captured = false; ctmLeft = 0;
  Object.assign(svg, { getScreenCTM: () => ({ e: ctmLeft, inverse: () => ({}) }),
    createSVGPoint: () => ({ x: 0, y: 0, matrixTransform() { return { x: this.x }; } }),
    setPointerCapture: () => { captured = true; }, hasPointerCapture: () => captured, releasePointerCapture: () => { captured = false; } });
};
const dates = ['2026-10-01', '2026-11-06', '2026-11-09', '2026-11-12', '2026-11-15'];
const editableBlock = () => {
  const base = response();
  return { ...base, graph: { ...base.graph!, steps: base.graph!.steps.map((slot, i) => i < 1 ? slot : { ...slot, scheduled_date: dates[i]!, status: 'pending' as const, root_state: 'unique' as const,
    execution_id: `root-${i + 1}`, capability: 'replace_pending' as const, editable: true, requires_approved_edits: false, lock_reason: null, display: base.graph!.steps[1]!.display }) } };
};
const mount = (source = editableBlock()): void => { handle = mountPlanEditorEditPanel(host, source); stubSvg(); };
const changes = (): string[] => Array.from(field('changes').children).map(item => item.textContent!);

beforeEach(() => {
  jest.useFakeTimers({ now: new Date('2026-10-15T12:00:00Z') });
  document.body.innerHTML = '<div class="modal"><div class="plan-editor-layout"><aside id="editor"></aside><form><input value="x"></form><div class="modal-buttons"><button id="close-plan-modal-btn">Cancel</button><button id="save">Save</button></div></div></div>';
  host = document.getElementById('editor')!;
});
afterEach(() => { handle?.dispose(); jest.useRealTimers(); });

test('dragging an editable point changes that step in the controlled draft and the changes list', () => {
  mount();
  drag(point(1), cx(1), 1);
  expect(field<HTMLInputElement>('date').value).toBe('2026-11-07');
  expect(changes()).toEqual(['Step 2: 2026-11-06 → 2026-11-07']);
  expect(handle.draft()!.dates[1]).toBe('2026-11-07');
  expect(document.querySelector<HTMLElement>('form')!.inert).toBeFalsy();
});

test('a completed point cannot be dragged or moved by keyboard, and shows its reason when selected', () => {
  mount();
  send('pointerdown', point(0), cx(0)); send('pointermove', svg, cx(0) + 20);
  expect(captured).toBe(false); expect(document.querySelector<HTMLElement>('form')!.inert).toBeFalsy();
  expect(field<HTMLInputElement>('date').value).toBe('2026-10-01'); expect(changes()).toHaveLength(0);
  expect(field('selected').textContent).toContain('This step already ran and cannot be changed.');
  key(point(0), 'ArrowRight', { altKey: true }); expect(changes()).toHaveLength(0);
});

test('the Start handle redistributes only the selected block and leaves the fixed neighbor', () => {
  mount();
  send('click', point(1), 0);
  expect(handleOf('start').hasAttribute('hidden')).toBe(false);
  drag(handleOf('start'), cx(1), 1);
  const next = handle.draft()!.dates;
  expect(next[0]).toBe('2026-10-01'); expect(next[1]).toBe('2026-11-07'); expect(next[4]).toBe('2026-11-15');
});

test('the End handle keeps the block start and the fixed neighbor', () => {
  mount();
  send('click', point(1), 0);
  drag(handleOf('end'), cx(4), 1);
  const next = handle.draft()!.dates;
  expect(next[0]).toBe('2026-10-01'); expect(next[1]).toBe('2026-11-06'); expect(next[4]).toBe('2026-11-16');
});

test('Escape restores the pre-gesture draft and the next Escape is left for the dialog', () => {
  mount();
  drag(point(1), cx(1), 1, false);
  expect(field<HTMLInputElement>('date').value).toBe('2026-11-07'); expect(document.querySelector<HTMLElement>('form')!.inert).toBe(true);
  const first = key(document.querySelector('.modal')!, 'Escape');
  expect(first.defaultPrevented).toBe(true); expect(field<HTMLInputElement>('date').value).toBe('2026-11-06');
  expect(document.querySelector<HTMLElement>('form')!.inert).toBeFalsy(); expect(captured).toBe(false);
  expect(key(document.querySelector('.modal')!, 'Escape').defaultPrevented).toBe(false);
});

test('a submit during a gesture is cancelled with the message', () => {
  mount();
  drag(point(1), cx(1), 1, false);
  const submit = new Event('submit', { bubbles: true, cancelable: true }); document.querySelector('form')!.dispatchEvent(submit);
  expect(submit.defaultPrevented).toBe(true); expect(field('error').textContent).toBe('Finish or cancel the date drag before saving.');
});

test('pointercancel and lost capture roll back; closing during a gesture restores inert and releases capture', () => {
  mount();
  drag(point(1), cx(1), 1, false); send('pointercancel', svg, 0);
  expect(field<HTMLInputElement>('date').value).toBe('2026-11-06');
  drag(point(1), cx(1), 1, false);
  handle.dispose();
  expect(document.querySelector<HTMLElement>('form')!.inert).toBeFalsy(); expect(document.querySelector<HTMLElement>('#save')!.inert).toBeFalsy(); expect(captured).toBe(false);
});

test('approved edits off keeps the dirty date but blocks Review; the point is fixed again', () => {
  const base = response();
  mount({ ...base, graph: { ...base.graph!, steps: base.graph!.steps.map((slot, i) => i === 2 ? { ...slot, scheduled_date: '2026-11-09' } : slot) } });
  field<HTMLInputElement>('approved').checked = true; field('approved').dispatchEvent(new Event('change', { bubbles: true }));
  expect(point(2).hasAttribute('data-fixed')).toBe(false);
  key(point(2), 'ArrowRight', { altKey: true });
  expect(changes()[0]).toContain('2026-11-09 → 2026-11-10');
  field<HTMLInputElement>('approved').checked = false; field('approved').dispatchEvent(new Event('change', { bubbles: true }));
  expect(point(2).hasAttribute('data-fixed')).toBe(true);
  expect(changes()[0]).toContain('2026-11-10'); expect(field('status').textContent).toContain('Re-enable approved edits');
});

test('a date typed into a missing slot adds a plot point; ArrowRight keeps focus on the live selected point and skips undated slots', () => {
  const base = response();
  mount({ ...base, graph: { ...base.graph!, steps: base.graph!.steps.map((slot, i) => i === 3 ? { ...slot, capability: 'needs_creation' as const } : i === 4 ? { ...slot, capability: 'replace_pending' as const, editable: true, lock_reason: null } : slot) } });
  expect(host.querySelectorAll('[data-preview-step]')).toHaveLength(4);
  point(1).focus(); const second = point(1);
  expect(key(point(1), 'ArrowRight').defaultPrevented).toBe(true);
  expect(document.activeElement).toBe(point(2)); expect(point(1)).toBe(second);
  key(point(2), 'ArrowRight'); expect(document.activeElement).toBe(point(4));
  key(point(4), 'ArrowRight'); expect(document.activeElement).toBe(point(4));
  field<HTMLSelectElement>('step').value = '3'; field('step').dispatchEvent(new Event('change', { bubbles: true }));
  field<HTMLInputElement>('date').value = '2026-11-12'; field('date').dispatchEvent(new Event('input', { bubbles: true })); field('date').dispatchEvent(new Event('change', { bubbles: true }));
  expect(host.querySelectorAll('[data-preview-step]')).toHaveLength(5);
  expect(host.querySelector('[data-preview-step="3"]')).not.toBeNull();
});

test('the handle publishes the draft and notifies subscribers on every paint until disposed', () => {
  mount();
  const listener = jest.fn(), unsubscribe = handle.subscribe(listener);
  key(point(1), 'ArrowRight', { altKey: true });
  expect(listener).toHaveBeenCalled(); expect(handle.draft()!.dates[1]).toBe('2026-11-07');
  unsubscribe(); listener.mockClear(); key(point(1), 'ArrowRight', { altKey: true }); expect(listener).not.toHaveBeenCalled();
  handle.dispose(); expect(handle.draft()).toBeNull();
});

const tabStops = (): string[] => Array.from(host.querySelectorAll('[data-preview-step]')).filter(item => item.getAttribute('tabindex') === '0').map(item => (item as HTMLElement).dataset['previewStep']!);

test('exactly one plotted point is tabbable, including when the selected step has no date', () => {
  const base = response();
  mount({ ...base, graph: { ...base.graph!, steps: base.graph!.steps.map((slot, i) => i === 3 ? { ...slot, capability: 'needs_creation' as const } : slot) } });
  expect(tabStops()).toEqual(['0']);
  field<HTMLSelectElement>('step').value = '3'; field('step').dispatchEvent(new Event('change', { bubbles: true }));
  expect(host.querySelector('[data-preview-step="3"]')).toBeNull();
  expect(tabStops()).toEqual(['0']);
  field<HTMLSelectElement>('step').value = '4'; field('step').dispatchEvent(new Event('change', { bubbles: true }));
  expect(tabStops()).toEqual(['4']);
});

test('the submit guard is removed on dispose and never stacks across mounts', () => {
  const layout = document.querySelector('.plan-editor-layout')!, remove = jest.spyOn(layout, 'removeEventListener'), add = jest.spyOn(layout, 'addEventListener');
  mount(); handle.dispose(); mount();
  const added = add.mock.calls.filter(call => call[0] === 'submit'), removed = remove.mock.calls.filter(call => call[0] === 'submit');
  expect(added).toHaveLength(2); expect(removed).toHaveLength(1); expect(removed[0]![1]).toBe(added[0]![1]);
  send('pointerdown', point(1), cx(1)); drag(point(1), cx(1), 1, false);
  const submit = new Event('submit', { bubbles: true, cancelable: true }); document.querySelector('form')!.dispatchEvent(submit);
  expect(submit.defaultPrevented).toBe(true);
});
