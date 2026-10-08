import type { LadderEvent } from './api/ladder';

export const ladderDimensions = ['run_id', 'layer_type', 'term', 'payment_option', 'status'] as const;
export type LadderDimension = typeof ladderDimensions[number];
export type LadderFilters = Record<LadderDimension, string> & { from: string; to: string };

export function emptyLadderFilters(): LadderFilters {
  return { run_id: '', layer_type: '', term: '', payment_option: '', status: '', from: '', to: '' };
}

export function filterLadderEvents(events: readonly LadderEvent[], filters: LadderFilters, except?: LadderDimension): LadderEvent[] {
  return events.filter(event => {
    if (ladderDimensions.some(key => key !== except && filters[key] !== '' && filters[key] !== event[key])) return false;
    const stamp = Date.parse(event.scheduled_date);
    if (filters.from && stamp < Date.parse(`${filters.from}Z`)) return false;
    if (filters.to && stamp > Date.parse(`${filters.to}Z`)) return false;
    return true;
  });
}

export function ladderFilterOptions(events: readonly LadderEvent[], filters: LadderFilters, key: LadderDimension): string[] {
  return Array.from(new Set(filterLadderEvents(events, filters, key).map(event => event[key]))).sort();
}

export function ladderAmountUnits(amount: string): bigint {
  if (!/^(0|[1-9]\d{0,13})(\.\d{1,6})?$/.test(amount)) throw new Error('Invalid planning budget.');
  const [whole = '0', fraction = ''] = amount.split('.');
  return BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'));
}

export function formatLadderUnits(units: bigint): string {
  return `${units / 1000000n}.${(units % 1000000n).toString().padStart(6, '0')}`;
}

export function ladderTotal(events: readonly LadderEvent[]): string {
  return formatLadderUnits(events.reduce((total, event) => total + ladderAmountUnits(event.amount_usd_hr), 0n));
}

export function ladderEditReason(event: LadderEvent, now = Date.now()): string | undefined {
  if (event.run_status !== 'planned') return `Run is ${event.run_status}.`;
  if (event.status !== 'scheduled') return `Tranche is ${event.status}.`;
  if (event.execution_id) return 'An execution is already linked.';
  if (Date.parse(event.scheduled_date) <= now) return 'The scheduled date has passed.';
  if (!Number.isSafeInteger(event.revision) || event.revision < 0) return 'Editing is unavailable until the timeline API is upgraded.';
  return undefined;
}
