/**
 * Settings tests: inline range validation on the global numeric settings and SP coverage inputs (#1411).
 * Split from settings.test.ts to stay under the 500-line ceiling; the module
 * mocks and the DOM setup live in settings-harness.ts.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  setupSettingsTest,
} from './settings-harness';
import {
  setupSettingsHandlers,
} from '../settings';

describe('Settings Module', () => {
  beforeEach(() => {
    setupSettingsTest();
  });

  // -----------------------------------------------------------------------
  // Inline range validation — top-level numeric settings (issue #1411)
  // -----------------------------------------------------------------------
  // These inputs have min/max in the test DOM (added alongside the fix) so
  // wireInlineRangeValidation creates the error element and wires the
  // 'input'/'blur' events. Tests confirm the regression is gone: inline
  // error appears immediately as the user types, not only after Save.
  describe('inline range validation on top-level numeric settings (#1411)', () => {
    function fire(el: HTMLInputElement, type: 'input' | 'blur'): void {
      el.dispatchEvent(new Event(type));
    }

    beforeEach(() => {
      setupSettingsHandlers();
    });

    it.each([
      ['setting-notification-days', '0',     'Must be a whole number between 1 and 30'],
      ['setting-notification-days', '31',    'Must be a whole number between 1 and 30'],
      ['setting-recs-stale-hours',  '-1',    'Must be a whole number between 0 and 8760'],
      ['setting-default-coverage',  '101',   'Must be a whole number between 0 and 100'],
    ])('typing %s=%s shows inline error with message %s', (id, val, msg) => {
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = val;
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl).not.toBeNull();
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe(msg);
    });

    it('clearing the default coverage field shows the inline error instead of passing as 0 (#522)', () => {
      const input = document.getElementById('setting-default-coverage') as HTMLInputElement;
      input.value = '';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById('setting-default-coverage-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe('Must be a whole number between 0 and 100');
    });

    it('clearing an optional numeric field stays without an inline error', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBeNull();
    });

    it('typing a fractional value into notification-days is rejected inline', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '1.5';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById('setting-notification-days-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(false);
    });

    it('restoring a valid value clears the inline error', () => {
      const input = document.getElementById('setting-notification-days') as HTMLInputElement;
      input.value = '0';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBe('true');

      input.value = '5';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBeNull();
      const errEl = document.getElementById('setting-notification-days-range-error');
      expect(errEl!.classList.contains('hidden')).toBe(true);
    });
  });

  // -----------------------------------------------------------------------
  // Inline range validation — SP coverage inputs (issue #1411 row 7.13)
  // -----------------------------------------------------------------------
  describe('inline range validation wired for SP coverage inputs (#1411 row 7.13)', () => {
    function fire(el: HTMLInputElement, type: 'input' | 'blur'): void {
      el.dispatchEvent(new Event(type));
    }

    beforeEach(() => {
      setupSettingsHandlers();
    });

    it.each([
      'aws-savings-plans-compute-coverage',
      'aws-savings-plans-ec2instance-coverage',
      'aws-savings-plans-sagemaker-coverage',
      'aws-savings-plans-database-coverage',
    ])('typing 101 into %s shows inline error', (id) => {
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = '101';
      fire(input, 'input');

      expect(input.getAttribute('aria-invalid')).toBe('true');
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl).not.toBeNull();
      expect(errEl!.classList.contains('hidden')).toBe(false);
      expect(errEl!.textContent).toBe('Must be a whole number between 0 and 100');
    });

    it('valid SP coverage value clears any prior error', () => {
      const id = 'aws-savings-plans-compute-coverage';
      const input = document.getElementById(id) as HTMLInputElement;
      input.value = '200';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBe('true');

      input.value = '80';
      fire(input, 'input');
      expect(input.getAttribute('aria-invalid')).toBeNull();
      const errEl = document.getElementById(`${id}-range-error`);
      expect(errEl!.classList.contains('hidden')).toBe(true);
    });
  });
});
