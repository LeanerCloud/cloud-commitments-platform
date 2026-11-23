/**
 * Inventory coverage bar tone (issue #726) and stale-response handling
 * (#134). Split from inventory.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { buildInventoryDOM, makeCommitment, clearDOM, makeProviderSection } from './inventory-harness';
import { loadActiveCommitments, loadCoverageBreakdown } from '../inventory';
import * as api from '../api';
import * as state from '../state';
import type { ProviderCoverageSection } from '../api';

// Issue #726: the bar was success-green at every value. It is neutral now,
// marks the service's coverage target, and turns amber/red far below it.
describe('loadCoverageBreakdown — coverage bar target and tone', () => {
  beforeEach(() => {
    buildInventoryDOM();
    (api.getCoverageBreakdown as jest.Mock).mockReset().mockResolvedValue({
      providers: [makeProviderSection('aws', [
        { service: 'ec2', covered_monthly: 900, on_demand_monthly: 100, coverage_pct: 90 },
        { service: 'rds', covered_monthly: 500, on_demand_monthly: 500, coverage_pct: 50 },
        { service: 'lambda', covered_monthly: 5, on_demand_monthly: 95, coverage_pct: 5 },
      ], 60)],
    });
    (state.subscribeProvider as jest.Mock).mockReturnValue(jest.fn());
    (state.subscribeAccount as jest.Mock).mockReturnValue(jest.fn());
    (state.getCurrentProvider as jest.Mock).mockReturnValue('');
    (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
  });

  afterEach(() => {
    clearDOM();
    (api.getConfig as jest.Mock).mockReset();
  });

  const bars = () => Array.from(document.querySelectorAll<HTMLElement>('#coverage-providers .coverage-bar'));

  test('uses the per-service target, else the global default, and tones by distance', async () => {
    (api.getConfig as jest.Mock).mockResolvedValue({
      global: { default_coverage: 80 },
      services: [{ provider: 'aws', service: 'rds', coverage: 60, enabled: true, term: 1, payment: 'no-upfront' }],
    });

    await loadCoverageBreakdown();

    const [ec2, rds, lambda] = bars();
    // ec2: 90% vs the 80% default -> on track, tick at 80%.
    expect(ec2!.className).toBe('coverage-bar coverage-bar--on-track');
    expect(ec2!.querySelector<HTMLElement>('.coverage-bar-target')!.style.left).toBe('80%');
    // rds: 50% vs its own 60% target -> 0.83 of target, still on track.
    expect(rds!.className).toBe('coverage-bar coverage-bar--on-track');
    expect(rds!.querySelector<HTMLElement>('.coverage-bar-target')!.style.left).toBe('60%');
    // lambda: 5% vs 80% -> far below, and the label says so.
    expect(lambda!.className).toBe('coverage-bar coverage-bar--far-below');
    expect(lambda!.getAttribute('aria-label')).toBe('5.0% covered, target 80%, far below target');
  });

  test('a config that cannot be read leaves the bars neutral with no target tick', async () => {
    (api.getConfig as jest.Mock).mockRejectedValue(new Error('403 forbidden'));

    await loadCoverageBreakdown();

    expect(bars()).toHaveLength(3);
    for (const bar of bars()) {
      expect(bar.className).toBe('coverage-bar coverage-bar--on-track');
      expect(bar.querySelector('.coverage-bar-target')).toBeNull();
    }
    expect(document.querySelector('#coverage-providers .error')).toBeNull();
  });
});

describe('stale responses never overwrite a newer scope (#134)', () => {
  beforeEach(() => {
    clearDOM();
    buildInventoryDOM();
    jest.clearAllMocks();
  });

  function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: Error) => void } {
    let resolve!: (v: T) => void;
    let reject!: (e: Error) => void;
    const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
    return { promise, resolve, reject };
  }

  test('active commitments: a slow first response is discarded', async () => {
    const slow = deferred<api.InventoryCommitment[]>();
    (api.listActiveCommitments as jest.Mock)
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce([makeCommitment({ account_name: 'GCP Newer' })]);

    const first = loadActiveCommitments();
    await loadActiveCommitments();
    slow.resolve([makeCommitment({ account_name: 'AWS Stale' })]);
    await first;

    const text = document.getElementById('active-commitments-list')!.textContent ?? '';
    expect(text).toContain('GCP Newer');
    expect(text).not.toContain('AWS Stale');
  });

  test('coverage: a slow first response is discarded', async () => {
    const slow = deferred<{ providers: ProviderCoverageSection[] }>();
    const section = (provider: string): ProviderCoverageSection =>
      ({ provider, services: [{ service: 'ec2', coverage_percent: 10 }] } as unknown as ProviderCoverageSection);
    (api.getConfig as jest.Mock).mockResolvedValue(null);
    (api.getCoverageBreakdown as jest.Mock)
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce({ providers: [section('gcp')] });

    const first = loadCoverageBreakdown();
    await loadCoverageBreakdown();
    slow.resolve({ providers: [section('aws')] });
    await first;

    const text = document.getElementById('coverage-providers')!.textContent?.toLowerCase() ?? '';
    expect(text).toContain('gcp');
    expect(text).not.toContain('aws');
  });

  test('active commitments: a slow first request rejecting after a newer success leaves the DOM alone', async () => {
    const slow = deferred<api.InventoryCommitment[]>();
    (api.listActiveCommitments as jest.Mock)
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce([makeCommitment({ account_name: 'GCP Newer' })]);

    const first = loadActiveCommitments();
    await loadActiveCommitments();
    const container = document.getElementById('active-commitments-list')!;
    const before = container.innerHTML;
    slow.reject(new Error('stale boom'));
    await first;

    expect(container.innerHTML).toBe(before);
    expect(container.textContent).toContain('GCP Newer');
    expect(container.textContent).not.toContain('stale boom');
    expect(container.querySelector('.error')).toBeNull();
  });

  test('coverage: a slow first request rejecting after a newer success leaves the DOM alone', async () => {
    const slow = deferred<{ providers: ProviderCoverageSection[] }>();
    const section = {
      provider: 'gcp', services: [{ service: 'ec2', coverage_percent: 10 }],
    } as unknown as ProviderCoverageSection;
    (api.getConfig as jest.Mock).mockResolvedValue(null);
    (api.getCoverageBreakdown as jest.Mock)
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce({ providers: [section] });

    const first = loadCoverageBreakdown();
    await loadCoverageBreakdown();
    const container = document.getElementById('coverage-providers')!;
    const before = container.innerHTML;
    slow.reject(new Error('stale boom'));
    await first;

    expect(container.innerHTML).toBe(before);
    expect(container.textContent?.toLowerCase()).toContain('gcp');
    expect(container.textContent).not.toContain('stale boom');
    expect(container.querySelector('.error')).toBeNull();
  });
});
