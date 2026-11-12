/**
 * Federation panel: the target cloud control is a single-select radio group (#1691).
 */
import { initFederationPanel } from '../federation';

jest.mock('../api', () => ({ getFederationIaC: jest.fn() }));
jest.mock('../toast', () => ({ showToast: jest.fn() }));

describe('federation target cloud pills', () => {
  beforeEach(() => {
    document.body.innerHTML =
      '<div id="federation-target-cloud-pills" role="radiogroup"></div><div id="federation-setup-panel"></div>';
  });

  test('pills are radios with exactly one checked, following clicks', async () => {
    await initFederationPanel('aws');
    const pills = Array.from(document.querySelectorAll<HTMLButtonElement>('#federation-target-cloud-pills button'));
    expect(pills.length).toBeGreaterThanOrEqual(3);
    for (const p of pills) {
      expect(p.getAttribute('role')).toBe('radio');
      expect(p.hasAttribute('aria-pressed')).toBe(false);
    }
    expect(pills.map(p => p.getAttribute('aria-checked'))).toEqual(pills.map((_, i) => (i === 0 ? 'true' : 'false')));

    pills[1]!.click();
    expect(pills.map(p => p.getAttribute('aria-checked'))).toEqual(pills.map((_, i) => (i === 1 ? 'true' : 'false')));
  });
});
