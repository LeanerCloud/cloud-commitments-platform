/**
 * Plans module tests: plan health-score badge and inline schedule preview tests.
 * Split from plans.test.ts to stay under the 500-line ceiling; the module
 * mocks live in plans-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
} from './plans-harness';
import {
  loadPlans,
  closePlanModal,
  openCreatePlanModal,
  _resetRampHandlersForTest,
} from '../plans';

import * as api from '../api';

describe('Plans Module', () => {
  beforeEach(() => {
    mockOpenArcheraOfferModal.mockClear();
    // Reset DOM with full form structure
    document.body.innerHTML = `
      <div id="plans-list"></div>
      <div id="planned-purchases-list"></div>
      <div id="plan-modal" class="hidden">
        <h3 id="plan-modal-title">Create Plan</h3>
        <form id="plan-form">
          <input type="hidden" id="plan-id">
          <input type="text" id="plan-name">
          <textarea id="plan-description"></textarea>
          <p id="plan-selection-groups-intro" hidden></p>
          <ul id="plan-selection-groups" hidden></ul>
          <div id="plan-service-fields">
          <select id="plan-provider">
            <option value="aws">AWS</option>
            <option value="azure">Azure</option>
            <option value="gcp">GCP</option>
          </select>
          <select id="plan-service">
            <optgroup label="AWS Services" data-provider="aws">
              <option value="ec2">EC2</option>
              <option value="rds">RDS</option>
            </optgroup>
            <!-- Issue #241: mirrors the real markup's AWS Savings Plans
                 optgroup, whose human-readable label ("Savings Plans") does
                 not contain "aws" -- only the data-provider attribute ties
                 it to the AWS provider. -->
            <optgroup label="Savings Plans" data-provider="aws">
              <option value="savings-plans-compute">Compute SP</option>
            </optgroup>
            <optgroup label="Azure Services" data-provider="azure">
              <option value="compute">Compute</option>
            </optgroup>
            <optgroup label="GCP Services" data-provider="gcp">
              <option value="compute">Compute</option>
            </optgroup>
          </select>
          <select id="plan-term">
            <option value="1">1 Year</option>
            <option value="3">3 Years</option>
          </select>
          <select id="plan-payment">
            <option value="no-upfront">No Upfront</option>
            <option value="partial-upfront">Partial Upfront</option>
            <option value="all-upfront">All Upfront</option>
          </select>
          </div>
          <input type="number" id="plan-coverage" value="80">
          <input type="checkbox" id="plan-auto-purchase">
          <input type="text" id="plan-max-step-commitment">
          <p id="plan-auto-purchase-state"></p>
          <input type="number" id="plan-notify-days" value="3">
          <input type="checkbox" id="plan-enabled" checked>
          <input type="radio" name="ramp-schedule" value="immediate" checked>
          <input type="radio" name="ramp-schedule" value="weekly-25pct">
          <input type="radio" name="ramp-schedule" value="monthly-10pct">
          <input type="radio" name="ramp-schedule" value="custom">
          <div id="custom-ramp-config" class="hidden">
            <input type="number" id="ramp-step-percent" value="20">
            <input type="number" id="ramp-interval-days" value="7">
          </div>
          <!-- Target Accounts section (universal-plans fix). The hidden
               plan-account-ids field is the contract between renderPlan
               AccountChips and savePlan; the submit button's disabled
               state is recomputed every time the chip list changes. -->
          <div id="plan-accounts-selected" class="selected-accounts"></div>
          <input type="hidden" id="plan-account-ids" value="">
          <div id="plan-filters-notice" class="hidden"></div>
          <button type="submit">Save plan</button>
        </form>
      </div>
      <div id="purchase-modal" class="hidden"></div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    // Default confirmDialog to confirmed so tests that don't override it
    // behave like the old window.confirm() returning true (finding 11-L2).
    mockConfirmDialog.mockResolvedValue(true);
    // Reset ramp-handlers install-once guard: DOM is rebuilt each beforeEach,
    // so static modal elements are fresh and need listeners re-attached (H3 fix).
    _resetRampHandlersForTest();
  });

  // Issue #340 follow-up (PR #376): per-plan health-score badge.
  describe('plan health-score badge', () => {
    // Mirror of the band thresholds in plans.ts. Declared here rather than
    // imported because plans.ts does not export them; if they diverge the
    // boundary cases below fail, which is the point.
    const HEALTH_SCORE_GOOD_BOUNDARY = 80;
    const HEALTH_SCORE_WARN_BOUNDARY = 50;

    const basePlan = {
      id: 'plan-1',
      name: 'Test Plan',
      enabled: true,
      auto_purchase: true,
      services: {
        ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 }
      },
      ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }
    };

    // The plan's own status badge (Active, success tone) shares the card, so
    // assert on the health badge element rather than on the card's markup.
    const healthBadge = (): Element | undefined => Array.from(
      document.querySelectorAll('#plans-list .badge'),
    ).find((b) => (b.textContent ?? '').startsWith('Health:'));

    test('renders a green badge for a healthy score (>= 80)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: 90, health_factors: [] }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-success');
      expect(list?.innerHTML).toContain('Health: 90');
    });

    test('renders an amber badge for a medium score (50-79)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 65,
          health_factors: [{ code: 'behind_schedule', penalty: 20, note: 'on step 1, expected step 3 by now' }]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-warning');
      expect(list?.innerHTML).toContain('Health: 65');
    });

    test('renders a red badge for a poor score (< 50)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 30,
          health_factors: [
            { code: 'overdue', penalty: 30, note: 'next purchase date has passed' },
            { code: 'failed_executions', penalty: 40, note: '4 failed execution(s)' }
          ]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-danger');
      expect(list?.innerHTML).toContain('Health: 30');
    });

    test('omits the badge entirely when health_score is absent (pre-feature API response)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ ...basePlan }] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).not.toContain('Health:');
    });

    // Regression guard for the fabricated-score bug: when the backend can't
    // compute a score it sends null, and the UI must say so. Pre-fix the
    // backend substituted 100 on a store failure, painting every plan with a
    // confident green "healthy" badge built from data it never read.
    test('renders an explicit unknown badge when health_score is null', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: null }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      // Critically, it must NOT be dressed up as a healthy score.
      expect(healthBadge()?.className).toBe('badge badge-muted');
      expect(list?.innerHTML).not.toContain('Health: 100');
    });

    // Band boundaries, not just mid-band samples: with only 90/65/30 the
    // suite cannot tell `>=` from `>`, so flipping either comparison would
    // silently reclassify every plan sitting exactly on a threshold.
    test.each([
      [HEALTH_SCORE_GOOD_BOUNDARY, 'badge-success'],
      [HEALTH_SCORE_GOOD_BOUNDARY - 1, 'badge-warning'],
      [HEALTH_SCORE_WARN_BOUNDARY, 'badge-warning'],
      [HEALTH_SCORE_WARN_BOUNDARY - 1, 'badge-danger'],
    ])('score %i sits in the %s band', async (score, expectedClass) => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: score, health_factors: [] }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe(`badge ${expectedClass}`);
      expect(list?.innerHTML).toContain(`Health: ${score}`);
    });

    // A score outside the 0-100 the API contracts to send is no more
    // trustworthy than a NaN. Banding it would paint -1 red and 101 green as
    // though both had been measured.
    test.each([-1, 101, 1000])('renders unknown for out-of-range score %i', async (score) => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: score }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      expect(list?.innerHTML).toContain('badge-muted');
      expect(list?.innerHTML).not.toContain(`Health: ${score}`);
    });

    // A garbled score off the wire is an uncomputable score, so it must
    // reach the same "unknown" badge rather than silently vanishing (which
    // reads as "this deploy predates the feature").
    test('renders unknown rather than dropping the badge for a non-finite score', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: NaN }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      expect(list?.innerHTML).not.toContain('Health: NaN');
    });

    test('escapes health factor notes in the tooltip (XSS regression)', async () => {
      const maliciousNote = '"><img src=x onerror=alert(1)>';
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 40,
          health_factors: [{ code: 'stalled', penalty: 15, note: maliciousNote }]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // Pre-fix, the unescaped `"` would close the title attribute early and
      // the rest would parse as a live <img onerror> element. Post-fix the
      // quote is entity-encoded so the whole malicious string stays inert
      // text inside the title attribute -- assert on the parsed DOM (not the
      // raw HTML string, since HTML serializers are free to leave `<`/`>`
      // un-re-escaped inside an already-safe attribute value).
      expect(list?.querySelector('img')).toBeNull();
      const badge = list?.querySelector('.badge-danger[title]');
      expect(badge?.getAttribute('title')).toContain(maliciousNote);
    });
  });

  describe('inline schedule preview', () => {
    const restartNotice = 'restarts from today for a plan already in progress; executed purchases are not shown';
    const host = (): HTMLElement => document.getElementById('plan-editor-panel')!;
    const points = (): number => host().querySelectorAll('[data-preview-step]').length;
    const backendPlan = (ramp: Record<string, unknown>, coverage: number | null = 80) => ({
      id: 'plan-1', name: 'Plan', enabled: true, auto_purchase: false, notification_days_before: 3,
      services: { ec2: { provider: 'aws', service: 'ec2', term: 1, payment: 'all-upfront', ...(coverage === null ? {} : { coverage }) } },
      ramp_schedule: ramp,
    });
    const openEdit = async (plan: unknown): Promise<void> => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ id: 'plan-1', name: 'Plan', enabled: true, auto_purchase: false, services: {} }] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue(plan);
      await loadPlans();
      expect(points()).toBe(0);
      (document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement).click();
      await jest.advanceTimersByTimeAsync(50);
    };
    beforeEach(() => {
      jest.useFakeTimers({ now: new Date('2026-10-15T12:00:00Z') });
      const form = document.getElementById('plan-form')!.innerHTML;
      document.body.innerHTML = `<div id="plans-list"></div><div id="planned-purchases-list"></div>
        <div id="plan-modal" class="modal hidden"><div class="modal-content"><h2 id="plan-modal-title">Create purchase plan</h2>
        <div class="plan-editor-layout"><section id="plan-editor-panel" hidden></section><form id="plan-form">${form}</form></div>
        <div class="modal-buttons"><button type="button" id="close-plan-modal-btn">Cancel</button><button type="submit" form="plan-form">Save</button></div>
        </div></div><div id="purchase-modal" class="hidden"></div>`;
      _resetRampHandlersForTest();
    });
    afterEach(() => { closePlanModal(); jest.useRealTimers(); });

    test.each([
      ['weekly 25%', { type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, 4],
      ['custom 33% / 14 days', { type: 'custom', percent_per_step: 33, step_interval_days: 14 }, 3],
      ['monthly 10%', { type: 'monthly', percent_per_step: 10, step_interval_days: 30 }, 10],
    ])('Edit shows the %s schedule preview and the restart notice', async (_name, ramp, count) => {
      await openEdit(backendPlan(ramp));
      expect(points()).toBe(count);
      expect(host().hidden).toBe(false);
      expect(host().querySelector('.editor-draft-notice')!.textContent).toContain(restartNotice);
      expect(api.createPlan).not.toHaveBeenCalled(); expect(api.updatePlan).not.toHaveBeenCalled();
      expect(api.createPlannedPurchases).not.toHaveBeenCalled();
    });

    const checkedRamp = (): string => document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked')!.value;
    const customFields = (): string[] => ['ramp-step-percent', 'ramp-interval-days'].map(id => (document.getElementById(id) as HTMLInputElement).value);

    test.each([
      ['weekly 20% / 7d', { type: 'weekly', percent_per_step: 20, step_interval_days: 7 }, 'custom', ['20', '7'], 5],
      ['monthly 5% / 30d', { type: 'monthly', percent_per_step: 5, step_interval_days: 30 }, 'custom', ['5', '30'], 20],
      ['weekly 25% on a 14 day interval', { type: 'weekly', percent_per_step: 25, step_interval_days: 14 }, 'custom', ['25', '14'], 4],
      ['monthly 10% on a 15 day interval', { type: 'monthly', percent_per_step: 10, step_interval_days: 15 }, 'custom', ['10', '15'], 10],
      ['weekly 25% / 7d', { type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, 'weekly-25pct', null, 4],
      ['monthly 10% / 30d', { type: 'monthly', percent_per_step: 10, step_interval_days: 30 }, 'monthly-10pct', null, 10],
      ['immediate', { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }, 'immediate', null, 1],
    ])('Edit prefills a stored %s ramp as %s (#1156)', async (_name, ramp, radio, fields, count) => {
      await openEdit(backendPlan(ramp));
      expect(checkedRamp()).toBe(radio);
      if (fields) expect(customFields()).toEqual(fields);
      expect(points()).toBe(count);
    });

    test('Edit with absent coverage shows the validation message and no points', async () => {
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, null));
      expect(points()).toBe(0);
      expect(host().querySelector('[data-editor="summary"]')!.textContent).not.toBe('');
    });

    // Panel and modal listeners on the form (input/change bubble, invalid/submit capture), keyed by type:capture.
    const trackFormListeners = () => {
      const form = document.getElementById('plan-form')!;
      const owned = new Set(['input:false', 'change:false', 'invalid:true', 'submit:true']);
      const key = (type: string, o?: boolean | AddEventListenerOptions): string => `${type}:${o === true || (typeof o === 'object' && o.capture === true)}`;
      const entries: { key: string; fn: unknown }[] = [];
      const add = form.addEventListener.bind(form), remove = form.removeEventListener.bind(form);
      jest.spyOn(form, 'addEventListener').mockImplementation((type: string, fn: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) => {
        if (owned.has(key(type, o))) entries.push({ key: key(type, o), fn });
        add(type, fn, o);
      });
      jest.spyOn(form, 'removeEventListener').mockImplementation((type: string, fn: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) => {
        const index = entries.findIndex(entry => entry.key === key(type, o) && entry.fn === fn);
        if (index >= 0) entries.splice(index, 1);
        remove(type, fn, o);
      });
      return entries;
    };

    test('closing the Edit modal removes every listener the panel and the modal added', async () => {
      const entries = trackFormListeners();
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }));
      expect(entries.map(entry => entry.key).sort()).toEqual(['change:false', 'input:false', 'invalid:true', 'submit:true']);
      closePlanModal();
      expect(entries).toEqual([]);
      expect(host().hidden).toBe(true); expect(host().childElementCount).toBe(0);
      expect(host().closest('.plan-editor-layout')!.classList.contains('has-editor-draft')).toBe(false);
    });

    test('Edit then Create without closing leaves exactly one panel listening and no Edit notice', async () => {
      const entries = trackFormListeners();
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }));
      openCreatePlanModal();
      expect(entries.map(entry => entry.key).sort()).toEqual(['change:false', 'input:false', 'invalid:true', 'submit:true']);
      expect(host().querySelector('.editor-draft-notice')!.textContent).not.toContain('restarts from today');
      const coverage = document.getElementById('plan-coverage') as HTMLInputElement;
      coverage.value = '60'; coverage.dispatchEvent(new Event('input', { bubbles: true }));
      expect(host().querySelector('[data-editor="summary"]')!.textContent).toContain('Target 60%');
    });

    test('Create previews without the restart notice', () => {
      openCreatePlanModal();
      expect(host().hidden).toBe(false);
      expect(host().querySelector('.editor-draft-notice')!.textContent).not.toContain('restarts from today');
    });
  });
});
