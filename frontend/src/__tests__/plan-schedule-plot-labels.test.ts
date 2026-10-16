import { renderSchedulePlot, schedulePlotFrame, updateSchedulePlot, schedulePlotX } from '../plan-schedule-plot';

const points = (dates: string[]) => dates.map((date, index) => ({ step: index + 1, date, target: (index + 1) * 20 }));
const mount = (dates: string[]): SVGSVGElement => {
  document.body.innerHTML = renderSchedulePlot(points(dates), schedulePlotFrame(dates), 0, true, null);
  return document.querySelector('svg')!;
};
const shown = (svg: SVGSVGElement): string[] => Array.from(svg.querySelectorAll('.preview-tick')).filter(tick => !tick.hasAttribute('hidden')).map(tick => tick.textContent!);

test('close dates never both keep a label, the last point always does, and every point keeps its date in its aria-label', () => {
  const dates = ['2026-10-01', '2026-11-06', '2026-11-09'], svg = mount(dates);
  expect(shown(svg)).toEqual(['10-01', '11-09']);
  expect(Array.from(svg.querySelectorAll('[data-preview-step]')).map(step => step.getAttribute('aria-label'))).toEqual([
    'Step 1, 2026-10-01, target 20%', 'Step 2, 2026-11-06, target 40%', 'Step 3, 2026-11-09, target 60%']);
});

test('well spaced dates keep every label', () => {
  expect(shown(mount(['2026-11-06', '2026-11-20', '2026-12-04']))).toEqual(['11-06', '11-20', '12-04']);
});

test('no kept labels are closer than the label gap, for a dense ramp', () => {
  const dates = Array.from({ length: 30 }, (_, i) => new Date(Date.UTC(2026, 10, 6 + i * 2)).toISOString().slice(0, 10)), svg = mount(dates);
  const frame = schedulePlotFrame(dates), xs = Array.from(svg.querySelectorAll('.preview-tick')).filter(tick => !tick.hasAttribute('hidden')).map(tick => Number(tick.getAttribute('x')));
  expect(xs.length).toBeGreaterThan(1);
  xs.slice(1).forEach((at, i) => expect(at - xs[i]!).toBeGreaterThanOrEqual(44));
  expect(xs[xs.length - 1]).toBeCloseTo(schedulePlotX(dates[29]!, frame));
});

test('updating the plot after a drag re-thins the labels', () => {
  const far = ['2026-10-01', '2026-11-06', '2026-11-20'], svg = mount(far);
  expect(shown(svg)).toEqual(['10-01', '11-06', '11-20']);
  const near = ['2026-10-01', '2026-11-06', '2026-11-09'];
  updateSchedulePlot(svg, points(near), schedulePlotFrame(near), 0, true, null);
  expect(shown(svg)).toEqual(['10-01', '11-09']);
});
