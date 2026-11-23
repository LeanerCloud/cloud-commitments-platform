/**
 * Shared setup for the commitment laddering settings suites (ladder*.test.ts):
 * the module mocks, the config fixture and the section renderer. Import this
 * module FIRST so the mocks register before the suite loads ../ladder. This file
 * is not a test (jest only collects *.test.ts).
 */
import { initLadderingSettings } from '../ladder';
import type { LadderConfig } from '../api';
import * as api from '../api';

jest.mock('../api/accounts', () => ({ listAccountsMinimal: jest.fn() }));
jest.mock('../ladder-timeline', () => ({ initLadderTimeline: jest.fn(), refreshLadderTimeline: jest.fn() }));

jest.mock('../api', () => ({
  getLadderConfigs: jest.fn(),
  upsertLadderConfig: jest.fn(),
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
}));

jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn() }));

jest.mock('../permissions', () => ({
  canAccess: jest.fn(() => true),
}));

export const mockShowToast = jest.fn();
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

export function baseConfig(overrides: Partial<LadderConfig> = {}): LadderConfig {
  return {
    cloud_account_id: '11111111-1111-1111-1111-111111111111',
    provider: 'aws',
    enabled: true,
    mode: 'email_approval',
    cadence: 'daily',
    target_coverage: 100,
    buffer_fraction: 0.1,
    baseline_percentile: 5,
    lookback_days: 30,
    buffer_utilization_threshold: 90,
    max_hourly_commit_per_run: 5,
    max_actions_per_run: 10,
    ramp_schedule: { steps: [{ after_days: 0, fraction: 1.0 }] },
    updated_at: '2026-01-15T10:00:00Z',
    ...overrides,
  } as LadderConfig;
}

// Render the full section (incl. the modal form) into the DOM so the exported
// helpers have the elements they read.
export async function renderSection(configs: LadderConfig[] = []): Promise<void> {
  document.body.innerHTML = '<div id="commitment-laddering-settings"></div>';
  (api.getLadderConfigs as jest.Mock).mockResolvedValue(configs);
  await initLadderingSettings(false);
}
