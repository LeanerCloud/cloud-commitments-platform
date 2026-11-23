/**
 * Shared setup for the Archera comparison suites (archera-comparison*.test.ts):
 * the insurance API mock, the Go golden fixture, the DTO key sets, the clone and
 * mount helpers and the per-test reset. Import this module FIRST so the mock
 * registers before the suite loads ../archera-comparison. This file is not a
 * test (jest only collects *.test.ts).
 */
import * as fs from 'fs';
import * as path from 'path';
import { renderComparisonSection } from '../archera-comparison';
import { getInsuranceComparison, getInsuranceStatus } from '../api/insurance';
import type { ArcheraComparison, ArcheraOffer, ArcheraProductSupport } from '../api/types';

jest.mock('../api/insurance', () => ({
  getInsuranceStatus: jest.fn(),
  getInsuranceComparison: jest.fn(),
}));

export const status = getInsuranceStatus as jest.Mock;
export const comparison = getInsuranceComparison as jest.Mock;

export const GOLDEN_PATH = path.resolve(__dirname, '../../../internal/archera/testdata/comparison.golden.json');
export const golden = JSON.parse(fs.readFileSync(GOLDEN_PATH, 'utf8')) as ArcheraComparison;

// satisfies makes tsc fail when the TS DTO gains or loses a key; the
// runtime check below fails when the Go golden file drifts from the TS keys.
export const TOP_KEYS = {
  title: 1, plan_id: 1, fetched_at: 1, currency: 1, currency_note: 1, premium_included: 1,
  basis_note: 1, delta_basis_note: 1, current: 1, hypotheticals: 1, rows: 1,
  non_gating_disclosure: 1, sponsorship_disclosure: 1,
} satisfies Record<keyof ArcheraComparison, 1>;
export const SUPPORT_KEYS = {
  status: 1, source: 1, evidence: 1, underwriting_allowance: 1, customer_eligibility: 1,
} satisfies Record<keyof ArcheraProductSupport, 1>;

export function clone(): ArcheraComparison {
  return JSON.parse(JSON.stringify(golden)) as ArcheraComparison;
}

export function firstOffer(c: ArcheraComparison): ArcheraOffer {
  const row = c.rows[0];
  if (!row) throw new Error('golden has no rows');
  return row.current;
}

export async function mount(): Promise<HTMLElement> {
  document.body.replaceChildren();
  const root = document.createElement('div');
  document.body.appendChild(root);
  await renderComparisonSection(root);
  return root;
}

export async function clickAndLoad(root: HTMLElement, data: ArcheraComparison): Promise<void> {
  comparison.mockResolvedValueOnce(data);
  root.querySelector('button')!.click();
  await flush();
}

export async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) await Promise.resolve();
}
