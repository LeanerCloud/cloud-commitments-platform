/**
 * Shared setup for the ladder timeline suites (ladder-timeline*.test.ts): the
 * module mocks, the event and run fixtures and the design-token stand-ins. Import
 * this module FIRST so the mocks register before the suite loads
 * ../ladder-timeline. This file is not a test (jest only collects *.test.ts).
 */
import type { LadderEvent, LadderRun } from '../api/ladder';

jest.mock('chart.js', () => ({ Chart: Object.assign(jest.fn().mockImplementation((_canvas: unknown, config: { data: unknown; options: unknown }) => ({ destroy: jest.fn(), update: jest.fn(), data: config.data, options: config.options })), { register: jest.fn() }), registerables: [] }));
jest.mock('../api/accounts', () => ({ listAccountsMinimal: jest.fn() }));
jest.mock('../api/ladder', () => ({ getLadderEvents: jest.fn(), getLadderRuns: jest.fn(), amendLadderEvent: jest.fn() }));
jest.mock('../permissions', () => ({ canAccess: jest.fn(() => true) }));

export const event: LadderEvent = { id: 'one', config_id: 'config', run_id: 'run', layer_type: 'compute-sp', term: '1yr', payment_option: 'no-upfront', status: 'scheduled', run_status: 'planned', revision: 0, amount_usd_hr: '1.000000', scheduled_date: '2030-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z' };
export const run: LadderRun = { id: 'run', config_id: 'config', status: 'planned', started_at: '2026-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z', baseline_usd_hr: null, existing_usd_hr: null, target_usd_hr: null, gap_usd_hr: null, total_hourly_commit: '1.000000', actions: [] };

// FIXTURE: stand-ins for the design tokens in styles/base.css (jsdom loads no CSS).
export const tokenColors: Record<string, string> = { '--cudly-primary': '#111111', '--cudly-success': '#222222', '--cudly-warn-fg': '#333333', '--cudly-error': '#444444', '--cudly-info': '#555555', '--cudly-text': '#666666', '--cudly-text-muted': '#777777', '--cudly-text-subtle': '#888888' };
