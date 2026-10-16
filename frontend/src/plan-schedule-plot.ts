import { escapeHtml } from './utils';
import { draftDateDay } from './lib/plan-draft-dates';

export interface SchedulePlotPoint { step: number; date: string; target: number; fixed?: boolean }
export type SchedulePlotBlock = readonly [number, number] | null;
export interface SchedulePlotFrame { width: number; startDay: number; span: number }
export function schedulePlotFrame(dates: readonly string[]): SchedulePlotFrame {
  if (dates.length === 0) throw new Error('No dated steps to plot.');
  const days = dates.map(draftDateDay), first = Math.min(...days), last = Math.max(...days);
  return { width: Math.max(640, dates.length * 48 + 96), startDay: first - 3, span: last - first + 8 };
}
export function schedulePlotX(date: string, frame: SchedulePlotFrame): number {
  return 48 + (draftDateDay(date) - frame.startDay) / frame.span * (frame.width - 96);
}
const LABEL_GAP = 44;
/** Which x-axis date labels fit without overlapping (12px "MM-DD" text); the last point always keeps its label. */
function thinLabels(points: readonly SchedulePlotPoint[], frame: SchedulePlotFrame): boolean[] {
  const xs = points.map(point => schedulePlotX(point.date, frame)), kept: number[] = [];
  xs.forEach((at, index) => {
    const last = index === xs.length - 1;
    while (last && kept.length && at - xs[kept[kept.length - 1]!]! < LABEL_GAP) kept.pop();
    if (!kept.length || at - xs[kept[kept.length - 1]!]! >= LABEL_GAP) kept.push(index);
  });
  return xs.map((_, index) => kept.includes(index));
}
const y = (target: number): number => 260 - target * 2.2;
function staircase(points: readonly SchedulePlotPoint[], frame: SchedulePlotFrame): string {
  let path = points[0]?.step === 1 ? `M 48 ${y(0)}` : '';
  points.forEach((point, index) => {
    const left = schedulePlotX(point.date, frame);
    path += point.step === (points[index - 1]?.step ?? 0) + 1
      ? ` H ${left} V ${y(point.target)}` : ` M ${left} ${y(point.target)}`;
  });
  return path;
}

function handleSpan(points: readonly SchedulePlotPoint[], block: SchedulePlotBlock): { at: [number, number]; visible: boolean } {
  const find = (logical: number | undefined): number => points.findIndex(point => point.step - 1 === logical);
  const first = find(block?.[0]), last = find(block?.[1]);
  return { at: [Math.max(first, 0), last < 0 ? points.length - 1 : last], visible: first >= 0 && last > first };
}

export function renderSchedulePlot(points: readonly SchedulePlotPoint[], frame: SchedulePlotFrame, selected: number, interactive: boolean, block: SchedulePlotBlock): string {
  const { width } = frame, count = points.length, x = (index: number): number => schedulePlotX(points[index]!.date, frame);
  const path = staircase(points, frame), labels = thinLabels(points, frame), span = handleSpan(points, block), hidden = span.visible ? '' : ' hidden';
  return `<svg viewBox="0 0 ${width} 370" width="${width}" role="${interactive ? "group" : "img"}" aria-label="Hypothetical purchase dates">
                ${[0, 20, 40, 60, 80, 100].map(value => `<path class="preview-grid" d="M 48 ${y(value)} H ${width - 24}"/><text x="8" y="${y(value) + 4}">${value}</text>`).join('')}
                <path class="preview-staircase" d="${path}"/>
                ${points.map((point, index) => `<g ${interactive ? `role="button" tabindex="${point.step - 1 === selected ? 0 : -1}"` : ""} data-preview-step="${point.step - 1}" ${point.fixed ? 'data-fixed' : ''} ${point.step - 1 === selected ? 'data-selected' : ''} ${interactive ? `aria-pressed="${point.step - 1 === selected}"` : ""} aria-label="Step ${point.step}, ${escapeHtml(point.date)}, target ${point.target}%">
${interactive && !point.fixed ? `                  <foreignObject class="preview-hit" x="${x(index) - 22}" y="${y(point.target) - 22}" width="44" height="44"><div xmlns="http://www.w3.org/1999/xhtml" class="preview-touch-target"></div></foreignObject>` : ""}
                  <circle class="preview-marker" cx="${x(index)}" cy="${y(point.target)}" r="6"/>
                  <text class="preview-tick" x="${x(index)}" y="292" text-anchor="middle"${labels[index] ? '' : ' hidden'}>${escapeHtml(point.date.slice(5))}</text>
                </g>`).join('')}
                ${interactive && count > 1 ? `<path class="preview-interval"${hidden} d="M ${x(span.at[0])} 336 H ${x(span.at[1])}"/>
                  ${(['start', 'end'] as const).map((boundary, i) => {
                    const left = x(span.at[i]!);
                    return `<g data-preview-boundary="${boundary}" role="button" tabindex="0"${hidden}>
                      <foreignObject class="preview-handle-hit" x="${left - 22}" y="314" width="44" height="44"><div xmlns="http://www.w3.org/1999/xhtml" class="preview-touch-target"></div></foreignObject>
                      <rect class="preview-handle-marker" x="${left - 6}" y="330" width="12" height="12"/>
                      <text x="${left}" y="365" text-anchor="middle">${i === 0 ? 'Start' : 'End'}</text></g>`;
                  }).join('')}` : ''}
              </svg>`;
}

export function updateSchedulePlot(svg: SVGSVGElement, points: readonly SchedulePlotPoint[], frame: SchedulePlotFrame, selected: number, interactive: boolean, block: SchedulePlotBlock): void {
  const span = handleSpan(points, block);
  const labels = thinLabels(points, frame), buttons = svg.querySelectorAll<SVGGElement>('[data-preview-step]');
  buttons.forEach((button, index) => {
    const point = points[index]!, left = schedulePlotX(point.date, frame);
    button.querySelector('.preview-hit')?.setAttribute('x', String(left - 22));
    button.querySelector('.preview-hit')?.setAttribute('y', String(y(point.target) - 22));
    button.querySelector('.preview-marker')!.setAttribute('cx', String(left));
    button.querySelector('.preview-marker')!.setAttribute('cy', String(y(point.target)));
    const tick = button.querySelector('.preview-tick')!;
    tick.setAttribute('x', String(left)); tick.textContent = point.date.slice(5); tick.toggleAttribute('hidden', !labels[index]);
    button.dataset.previewStep = String(point.step - 1);
    button.setAttribute('aria-label', `Step ${point.step}, ${point.date}, target ${point.target}%`);
    button.toggleAttribute('data-fixed', !!point.fixed);
    button.toggleAttribute('data-selected', point.step - 1 === selected);
    if (interactive) { button.setAttribute('aria-pressed', String(point.step - 1 === selected)); button.setAttribute('tabindex', point.step - 1 === selected ? '0' : '-1'); }
  });
  svg.querySelector('.preview-staircase')!.setAttribute('d', staircase(points, frame));
  svg.querySelectorAll<SVGGElement>('[data-preview-boundary]').forEach(handle => {
    const boundary = handle.dataset.previewBoundary!, index = span.at[boundary === 'start' ? 0 : 1];
    const left = schedulePlotX(points[index]!.date, frame);
    handle.querySelector('.preview-handle-hit')!.setAttribute('x', String(left - 22));
    handle.querySelector('.preview-handle-marker')!.setAttribute('x', String(left - 6));
    handle.querySelector('text')!.setAttribute('x', String(left));
    handle.toggleAttribute('hidden', !span.visible);
    handle.setAttribute('aria-label', `Interval ${boundary}, ${points[index]!.date}, UTC. Arrows move a day; Enter edits date.`);
  });
  const interval = svg.querySelector('.preview-interval');
  interval?.setAttribute('d', `M ${schedulePlotX(points[span.at[0]]!.date, frame)} 336 H ${schedulePlotX(points[span.at[1]]!.date, frame)}`);
  interval?.toggleAttribute('hidden', !span.visible);
}
