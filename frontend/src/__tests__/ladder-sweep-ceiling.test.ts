import { parseSweepCeiling } from '../ladder-form-numbers';

describe('parseSweepCeiling', () => {
  test('untouched field sends no key (stored value)', () => {
    const r = parseSweepCeiling('2.5', '2.5');
    expect(r).toEqual({ ok: true, patch: {} });
    expect(Object.keys((r as { patch: object }).patch)).toHaveLength(0);
  });

  test('untouched empty field sends no key (NULL stays NULL)', () => {
    expect(parseSweepCeiling('  ', '')).toEqual({ ok: true, patch: {} });
  });

  test('cleared field sends null', () => {
    expect(parseSweepCeiling('', '2.5')).toEqual({ ok: true, patch: { max_hourly_commit_per_sweep: null } });
  });

  test('a number sends the number', () => {
    expect(parseSweepCeiling('3', '')).toEqual({ ok: true, patch: { max_hourly_commit_per_sweep: 3 } });
    expect(parseSweepCeiling('0.000001', '')).toEqual({ ok: true, patch: { max_hourly_commit_per_sweep: 1e-6 } });
    expect(parseSweepCeiling('1000000000000', '1')).toEqual({ ok: true, patch: { max_hourly_commit_per_sweep: 1e12 } });
    expect(parseSweepCeiling('.5', '')).toEqual({ ok: true, patch: { max_hourly_commit_per_sweep: 0.5 } });
  });

  test.each(['abc', 'NaN', '1e3', '0.0000001', '1.1234567', '0', '0.0000009', '-1', '1000000000001', '1,5', 'Infinity', '0x10'])(
    'rejects %s', raw => {
      expect(parseSweepCeiling(raw, '')).toEqual({ ok: false, message: expect.stringContaining('between 0.000001') });
    });
});
