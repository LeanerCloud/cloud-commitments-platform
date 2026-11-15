/**
 * Reads the required numeric fields of the per-account ladder config form.
 *
 * A cleared <input type="number"> has value '', and Number('') is 0. The
 * backend rejects an explicit 0 for most of these fields, but accepts it for
 * buffer_fraction as a deliberate "no buffer", so a field the user emptied
 * and forgot about silently removed the over-commitment safety buffer. An
 * empty or non-numeric field now blocks the save with a message naming it.
 * A missing element (form not rendered) still falls back to the documented
 * default.
 */
import type { LadderConfig } from './api';

export type LadderFormNumbers = Pick<LadderConfig,
  'target_coverage' | 'buffer_fraction' | 'baseline_percentile' | 'lookback_days'
  | 'buffer_utilization_threshold' | 'max_actions_per_run'>;

export type LadderNumbersResult =
  | { ok: true; values: LadderFormNumbers }
  | { ok: false; message: string; field: HTMLInputElement | null };

const FIELDS: ReadonlyArray<[keyof LadderFormNumbers, string, string, number]> = [
  ['target_coverage', 'ladder-cfg-target-coverage', 'Target coverage', 100],
  ['buffer_fraction', 'ladder-cfg-buffer-fraction', 'Buffer fraction', 0.10],
  ['baseline_percentile', 'ladder-cfg-baseline-percentile', 'Baseline percentile', 5],
  ['lookback_days', 'ladder-cfg-lookback-days', 'Lookback days', 30],
  ['buffer_utilization_threshold', 'ladder-cfg-buf-util-threshold', 'Buffer utilization threshold', 90],
  ['max_actions_per_run', 'ladder-cfg-max-actions', 'Max actions per run', 10],
];

export function readLadderNumbers(): LadderNumbersResult {
  const values: Partial<LadderFormNumbers> = {};
  for (const [key, id, label, fallback] of FIELDS) {
    const field = document.getElementById(id) as HTMLInputElement | null;
    const raw = field ? field.value.trim() : String(fallback);
    const n = Number(raw);
    if (raw === '' || !Number.isFinite(n)) {
      return { ok: false, message: `${label} is required. Enter a number before saving.`, field };
    }
    values[key] = n;
  }
  return { ok: true, values: values as LadderFormNumbers };
}

const CEILING_MIN = 1e-6;
const CEILING_MAX = 1e12;
// Plain decimals only: no exponent, sign or separators, at most 6 decimals
// (the API stores micro-dollars and refuses finer input).
const CEILING_FORMAT = /^(\d+(\.\d{0,6})?|\.\d{1,6})$/;

export type SweepCeilingPatch = Pick<Partial<LadderConfig>, 'max_hourly_commit_per_sweep'>;

export type SweepCeilingResult =
  | { ok: true; patch: SweepCeilingPatch }
  | { ok: false; message: string };

/**
 * Maps the per-sweep ceiling field to the PUT semantics: key omitted =
 * unchanged, null = clear, number = set. `initial` is the value the form was
 * opened with, so an untouched field sends no key.
 */
export function parseSweepCeiling(rawInput: string, initial: string): SweepCeilingResult {
  const raw = rawInput.trim();
  if (raw === initial.trim()) return { ok: true, patch: {} };
  if (raw === '') return { ok: true, patch: { max_hourly_commit_per_sweep: null } };
  const n = Number(raw);
  if (!CEILING_FORMAT.test(raw) || !(n >= CEILING_MIN && n <= CEILING_MAX)) {
    return {
      ok: false,
      message: 'Max hourly commit per sweep must be between 0.000001 and 1000000000000 $/hr, with at most 6 decimals, or blank to clear it.',
    };
  }
  return { ok: true, patch: { max_hourly_commit_per_sweep: n } };
}
