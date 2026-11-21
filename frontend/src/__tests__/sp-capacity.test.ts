import { savingsPlanSizedCents, savingsPlanSizedHourly } from '../sp-capacity';

// Expected values are the Go sizer's own output (planstep.exactSPHourly) for
// the same inputs, so the UI shows what the backend buys (#1959).
describe('savingsPlanSizedCents mirrors planstep.exactSPHourly', () => {
  test.each([
    [0.58, 50, 29],
    [1.005, 50, 50],
    [0.015, 100, 1],
    [0.025, 60, 1],
    [1.995, 33, 65],
    [0.07, 50, 3],
    [0.29, 99, 28],
    [12.345, 7, 86],
    [1e-7, 50, 0],
  ])('%s/hr at %s%% is %s cents', (hourly, pct, cents) => {
    expect(savingsPlanSizedCents(hourly, pct)).toBe(cents);
  });

  test('below one cent has no sized hourly; otherwise whole cents', () => {
    expect(savingsPlanSizedHourly(0.01, 50)).toBeNull();
    expect(savingsPlanSizedHourly(0.02, 50)).toBe(0.01);
    expect(savingsPlanSizedHourly(0.58, 50)).toBe(0.29);
  });

  test('rejects a fractional capacity percent', () => {
    expect(() => savingsPlanSizedCents(1, 50.5)).toThrow();
  });
});
