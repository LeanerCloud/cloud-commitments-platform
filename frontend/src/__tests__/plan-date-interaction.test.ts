import { wirePlanDateInteraction, PlanDateInteraction } from '../plan-date-interaction';
import { renderSchedulePlot, schedulePlotFrame, updateSchedulePlot, SchedulePlotPoint } from '../plan-schedule-plot';

const dates = ['2026-10-20', null, '2026-11-03', '2026-11-10', '2026-11-17'];
const plotted = [0, 2, 3, 4];
const points = (): SchedulePlotPoint[] => plotted.map(i => ({ step: i + 1, date: dates[i]!, target: (i + 1) * 20, fixed: i === 0 || i === 4 }));
const frame = schedulePlotFrame(plotted.map(i => dates[i]!));
let svg: SVGSVGElement, calls: { [name: string]: jest.Mock }, captured: jest.Mock, dispose: () => void;
const send = (type: string, target: Element, x: number): Event => {
  const event = Object.assign(new Event(type, { bubbles: true, cancelable: true }), { pointerId: 1, isPrimary: true, button: 0, clientX: x, clientY: 0 });
  target.dispatchEvent(event); return event;
};
const step = (index: number): SVGGElement => svg.querySelector<SVGGElement>(`[data-preview-step="${index}"]`)!;
const key = (target: Element, name: string): void => { target.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true })); };
beforeEach(() => {
  document.body.innerHTML = `<div class="modal">${renderSchedulePlot(points(), frame, 2, true, [2, 3])}</div>`;
  svg = document.querySelector('svg')!;
  captured = jest.fn();
  Object.assign(svg, { getScreenCTM: () => ({ inverse: () => ({}) }), createSVGPoint: () => ({ x: 0, y: 0, matrixTransform() { return { x: this.x }; } }),
    setPointerCapture: captured, hasPointerCapture: () => false, releasePointerCapture: jest.fn() });
  calls = { select: jest.fn(), gesture: jest.fn(), apply: jest.fn(), restore: jest.fn(), validate: jest.fn(), focusDate: jest.fn(), error: jest.fn() };
  const binding: PlanDateInteraction = { ...calls, read: () => ({ dates, blocked: false, plotted, block: [2, 3], movable: (index: number) => index === 2 || index === 3 }) } as unknown as PlanDateInteraction;
  dispose = wirePlanDateInteraction(svg, binding);
});
afterEach(() => dispose());

test('a fixed point selects without starting a gesture, capturing the pointer or blocking native pan', () => {
  const event = send('pointerdown', step(0), 100);
  expect(calls.select).toHaveBeenCalledWith(0);
  expect(calls.gesture).not.toHaveBeenCalled();
  expect(captured).not.toHaveBeenCalled();
  expect(event.defaultPrevented).toBe(false);
  send('pointerdown', step(2), 100);
  expect(calls.gesture).toHaveBeenCalledTimes(1);
  expect(captured).toHaveBeenCalledTimes(1);
});

test('boundary handles drag the block bounds, not the first and last indexes', () => {
  const start = svg.querySelector('[data-preview-boundary="start"]')!, end = svg.querySelector('[data-preview-boundary="end"]')!;
  send('pointerdown', start, 100); send('pointermove', svg, 140);
  expect(calls.apply).toHaveBeenLastCalledWith(expect.objectContaining({ index: 2, boundary: 'start', before: dates }));
  send('pointercancel', svg, 140);
  send('pointerdown', end, 100); send('pointermove', svg, 140);
  expect(calls.apply).toHaveBeenLastCalledWith(expect.objectContaining({ index: 3, boundary: 'end' }));
});

test('arrow navigation walks plotted steps and keeps the focused point', () => {
  step(2).focus(); key(step(2), 'ArrowRight');
  expect(calls.select).toHaveBeenLastCalledWith(3);
  expect(document.activeElement).toBe(step(3));
  key(step(4), 'ArrowRight'); expect(calls.select).toHaveBeenLastCalledWith(4);
  key(step(2), 'ArrowLeft'); expect(calls.select).toHaveBeenLastCalledWith(0);
});

test('validation goes through the binding after a drag and on cancel', () => {
  send('pointerdown', step(2), 100); send('pointerup', svg, 100);
  expect(calls.validate).toHaveBeenCalledTimes(1);
  calls['validate']!.mockImplementation(() => { throw new Error('bad'); });
  send('pointerdown', step(3), 100); send('pointercancel', svg, 100);
  expect(calls.error).toHaveBeenLastCalledWith('Draft dates are no longer future dates. Close and reopen for fresh dates.');
});

test('plot places handles at the block ends and keeps nodes while the block moves', () => {
  const handle = (name: string): Element => svg.querySelector(`[data-preview-boundary="${name}"]`)!;
  const x = (index: number): number => Number(step(index).querySelector('circle')!.getAttribute('cx'));
  expect(handle('start').hasAttribute('hidden')).toBe(false);
  expect(Number(handle('start').querySelector('rect')!.getAttribute('x'))).toBe(x(2) - 6);
  expect(Number(handle('end').querySelector('rect')!.getAttribute('x'))).toBe(x(3) - 6);
  expect(step(0).hasAttribute('data-fixed')).toBe(true);
  expect(step(0).querySelector('.preview-hit')).toBeNull();
  expect(step(2).hasAttribute('data-fixed')).toBe(false);
  expect(step(2).querySelector('.preview-hit')).not.toBeNull();
  step(2).focus();
  const before = [step(2), handle('start')];
  updateSchedulePlot(svg, points(), frame, 3, true, [3, 4]);
  expect([step(2), handle('start')]).toEqual(before);
  expect(document.activeElement).toBe(step(2));
  expect(Number(handle('start').querySelector('rect')!.getAttribute('x'))).toBe(x(3) - 6);
  updateSchedulePlot(svg, points(), frame, 3, true, null);
  expect(handle('start').hasAttribute('hidden') && handle('end').hasAttribute('hidden') && svg.querySelector('.preview-interval')!.hasAttribute('hidden')).toBe(true);
  updateSchedulePlot(svg, points(), frame, 3, true, [2, 2]);
  expect(handle('end').hasAttribute('hidden')).toBe(true);
});

test('Alt+Arrow moves only movable steps', () => {
  step(0).dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, cancelable: true }));
  expect(calls.apply).not.toHaveBeenCalled();
  step(2).dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, cancelable: true }));
  expect(calls.apply).toHaveBeenCalledTimes(1);
  expect(calls.apply).toHaveBeenCalledWith(expect.objectContaining({ index: 2, boundary: null }));
});
