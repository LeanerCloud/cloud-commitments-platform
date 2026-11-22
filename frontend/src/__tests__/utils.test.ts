/**
 * Unit tests for utility functions
 */
import {
  formatCurrency,
  formatDate,
  formatDateTime,
  formatTerm,
  getDateParts,
  debounce,
  throttle,
  escapeHtml,
  escapeHtmlAttr,
  parseQueryParams,
  buildUrl,
  deepClone,
  isValidEmail,
  formatRampSchedule,
  getStatusBadge,
  calculatePaybackMonths,
} from '../utils';

describe('formatCurrency', () => {
  test('formats positive numbers correctly', () => {
    expect(formatCurrency(1000)).toBe('$1,000');
    expect(formatCurrency(1234567)).toBe('$1,234,567');
    expect(formatCurrency(99.99)).toBe('$100');
  });

  test('formats zero correctly', () => {
    expect(formatCurrency(0)).toBe('$0');
  });

  test('handles null and undefined with distinct absent marker (11-N2)', () => {
    // Absent/non-finite values now render as '—' to distinguish missing data
    // from a real $0 balance (finding 11-N2, feedback_nullable_not_zero).
    expect(formatCurrency(null as unknown as number)).toBe('—');
    expect(formatCurrency(undefined as unknown as number)).toBe('—');
  });

  test('handles NaN with distinct absent marker (11-N2)', () => {
    expect(formatCurrency(NaN)).toBe('—');
  });

  test('still renders real zero as $0', () => {
    expect(formatCurrency(0)).toBe('$0');
  });

  test('supports custom currency symbol', () => {
    expect(formatCurrency(1000, '€')).toBe('€1,000');
    expect(formatCurrency(500, '£')).toBe('£500');
  });
});

describe('formatDate', () => {
  test('renders canonical "Mon DD, YYYY" form regardless of browser locale', () => {
    // 2024-03-15 → "Mar 15, 2024" in en-US short-month format. The check is
    // locale-invariant (formatDate forces en-US) and timezone-invariant: a
    // bare "YYYY-MM-DD" is parsed as local midnight, so the displayed day
    // stays 15 even in browsers west of UTC (previously rendered "Mar 14").
    expect(formatDate('2024-03-15')).toBe('Mar 15, 2024');
  });

  test('formats Date object into the same canonical form', () => {
    expect(formatDate(new Date('2024-03-15T00:00:00Z'))).toMatch(/^Mar \d{1,2}, 2024$/);
  });

  test('returns empty string for null/undefined', () => {
    expect(formatDate(null as unknown as string)).toBe('');
    expect(formatDate(undefined as unknown as string)).toBe('');
    expect(formatDate('')).toBe('');
  });

  test('returns empty string for invalid date', () => {
    expect(formatDate('not-a-date')).toBe('');
    // Digit-shaped but out-of-range: must not silently roll over (month 13,
    // day 45) into a valid date — parseDateInput round-trips and rejects it.
    expect(formatDate('2024-13-45')).toBe('');
  });

  test('does not shift a bare calendar day across the day boundary', () => {
    // A date-only string is the intended calendar day; it must render the
    // same day regardless of the runner's timezone (regression for the
    // UTC-midnight off-by-one that showed "Mar 14" west of UTC).
    expect(formatDate('2024-01-01')).toBe('Jan 1, 2024');
    expect(formatDate('2024-12-31')).toBe('Dec 31, 2024');
  });
});

describe('formatDateTime', () => {
  test('renders "Mon DD, YYYY, HH:mm" with 24-hour clock', () => {
    // Construct a known UTC instant and assert the format shape. The exact
    // hour digits depend on TZ, so match the structure instead of a literal.
    const result = formatDateTime('2024-03-15T14:30:00Z');
    expect(result).toMatch(/^Mar \d{1,2}, 2024, \d{2}:\d{2}$/);
  });

  test('uses 24-hour clock (no AM/PM)', () => {
    const result = formatDateTime('2024-03-15T14:30:00Z');
    expect(result).not.toMatch(/AM|PM/i);
  });

  test('returns empty string for invalid input', () => {
    expect(formatDateTime(null as unknown as string)).toBe('');
    expect(formatDateTime('')).toBe('');
  });
});

describe('formatTerm', () => {
  test('renders "1 Year" (singular) and "3 Years" (plural)', () => {
    expect(formatTerm(1)).toBe('1 Year');
    expect(formatTerm(3)).toBe('3 Years');
  });

  test('rounds floats to nearest integer for pluralization', () => {
    expect(formatTerm(1.0)).toBe('1 Year');
    expect(formatTerm(2.9)).toBe('3 Years');
  });

  test('returns empty string for null/undefined/NaN', () => {
    expect(formatTerm(null)).toBe('');
    expect(formatTerm(undefined)).toBe('');
    expect(formatTerm(NaN)).toBe('');
  });

  test('handles arbitrary positive integers', () => {
    expect(formatTerm(5)).toBe('5 Years');
    expect(formatTerm(0)).toBe('0 Years');
  });
});

describe('getDateParts', () => {
  test('returns day and month for valid date', () => {
    const result = getDateParts('2024-03-15');
    expect(result.day).toBe(15);
    // Pinned to en-US (#1728): assert the literal abbreviation, not just
    // truthiness, so a locale regression here fails this test directly.
    expect(result.month).toBe('Mar');
  });

  test('returns zeros for null/undefined', () => {
    expect(getDateParts(null as unknown as string)).toEqual({ day: 0, month: '' });
    expect(getDateParts(undefined as unknown as string)).toEqual({ day: 0, month: '' });
  });

  test('returns zeros for invalid date', () => {
    expect(getDateParts('invalid')).toEqual({ day: 0, month: '' });
    // Out-of-range digits must be rejected, not rolled over.
    expect(getDateParts('2024-13-45')).toEqual({ day: 0, month: '' });
  });

  test('keeps a bare calendar day stable across timezones', () => {
    // Regression: a date-only string parsed as UTC midnight previously
    // reported the previous day's date west of UTC.
    expect(getDateParts('2024-03-15').day).toBe(15);
    expect(getDateParts('2024-01-01').day).toBe(1);
  });
});

describe('locale independence (issue #1728)', () => {
  // formatCurrency and getDateParts must render the same digits/month
  // abbreviation no matter what locale the host browser (or, for tests,
  // the machine running jest) defaults to. The pre-fix code asked for the
  // host default explicitly -- `toLocaleString(undefined, ...)` in
  // formatCurrency, `toLocaleString('default', ...)` in getDateParts --
  // so a real regression only shows up when that default isn't en-US.
  // Rather than depend on the CI runner's own locale (which is en-US, so
  // these tests would stay green with the bug present -- see PR #1732),
  // simulate a de-DE host default by intercepting the "no explicit
  // locale" call shape and rerouting it to a real de-DE formatter, while
  // leaving the fixed code's explicit 'en-US' calls untouched.
  const realNumberToLocaleString = Number.prototype.toLocaleString;
  const realDateToLocaleString = Date.prototype.toLocaleString;

  beforeEach(() => {
    jest.spyOn(Number.prototype, 'toLocaleString').mockImplementation(
      function (this: number, locale?: Intl.LocalesArgument, options?: Intl.NumberFormatOptions) {
        const hostDefault = locale === undefined ? 'de-DE' : locale;
        return realNumberToLocaleString.call(this, hostDefault, options);
      }
    );
    jest.spyOn(Date.prototype, 'toLocaleString').mockImplementation(
      function (this: Date, locale?: Intl.LocalesArgument, options?: Intl.DateTimeFormatOptions) {
        const hostDefault = locale === undefined || locale === 'default' ? 'de-DE' : locale;
        return realDateToLocaleString.call(this, hostDefault, options);
      }
    );
  });

  afterEach(() => {
    jest.restoreAllMocks();
  });

  test('formatCurrency keeps en-US comma grouping under a de-DE host default', () => {
    // de-DE would render this "1.000" (period as thousands separator);
    // confirms formatCurrency's explicit 'en-US' argument, not the host
    // default, drives the output.
    expect(formatCurrency(1000)).toBe('$1,000');
  });

  test('getDateParts keeps the en-US month abbreviation under a de-DE host default', () => {
    // de-DE would render March as "Mär"; confirms getDateParts passes an
    // explicit 'en-US' locale rather than 'default'.
    expect(getDateParts('2024-03-15').month).toBe('Mar');
  });
});

describe('debounce', () => {
  beforeEach(() => {
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  test('delays function execution', () => {
    const fn = jest.fn();
    const debouncedFn = debounce(fn, 100);

    debouncedFn();
    expect(fn).not.toHaveBeenCalled();

    jest.advanceTimersByTime(100);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  test('resets timer on subsequent calls', () => {
    const fn = jest.fn();
    const debouncedFn = debounce(fn, 100);

    debouncedFn();
    jest.advanceTimersByTime(50);
    debouncedFn();
    jest.advanceTimersByTime(50);
    expect(fn).not.toHaveBeenCalled();

    jest.advanceTimersByTime(50);
    expect(fn).toHaveBeenCalledTimes(1);
  });
});

describe('throttle', () => {
  beforeEach(() => {
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  test('executes immediately on first call', () => {
    const fn = jest.fn();
    const throttledFn = throttle(fn, 100);

    throttledFn();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  test('limits execution rate', () => {
    const fn = jest.fn();
    const throttledFn = throttle(fn, 100);

    throttledFn();
    throttledFn();
    throttledFn();
    expect(fn).toHaveBeenCalledTimes(1);

    jest.advanceTimersByTime(100);
    throttledFn();
    expect(fn).toHaveBeenCalledTimes(2);
  });
});

describe('escapeHtml', () => {
  test('escapes HTML special characters', () => {
    expect(escapeHtml('<script>alert("xss")</script>')).toBe('&lt;script&gt;alert(&quot;xss&quot;)&lt;/script&gt;');
  });

  test('escapes ampersands', () => {
    expect(escapeHtml('A & B')).toBe('A &amp; B');
  });

  test('escapes quotes', () => {
    expect(escapeHtml('"test"')).toBe('&quot;test&quot;');
  });

  test('returns empty string for null/undefined', () => {
    expect(escapeHtml(null as unknown as string)).toBe('');
    expect(escapeHtml(undefined as unknown as string)).toBe('');
    expect(escapeHtml('')).toBe('');
  });

  test('passes through safe strings', () => {
    expect(escapeHtml('Hello World')).toBe('Hello World');
    expect(escapeHtml('123')).toBe('123');
  });
});

describe('escapeHtmlAttr', () => {
  test('encodes double-quote to &quot; (attribute boundary safety)', () => {
    // A raw " would terminate the surrounding attribute and allow markup injection.
    expect(escapeHtmlAttr('"')).toBe('&quot;');
    expect(escapeHtmlAttr('a" onmouseover="alert(1)')).toBe('a&quot; onmouseover=&quot;alert(1)');
  });

  test('encodes single-quote to &#39;', () => {
    expect(escapeHtmlAttr("'")).toBe('&#39;');
    expect(escapeHtmlAttr("it's")).toBe('it&#39;s');
  });

  test('still encodes & < > (inherits from escapeHtml)', () => {
    expect(escapeHtmlAttr('&')).toBe('&amp;');
    expect(escapeHtmlAttr('<img>')).toBe('&lt;img&gt;');
    expect(escapeHtmlAttr('x"><img src=x onerror=alert(1)>')).toBe('x&quot;&gt;&lt;img src=x onerror=alert(1)&gt;');
  });

  test('returns empty string for null/undefined', () => {
    expect(escapeHtmlAttr(null as unknown as string)).toBe('');
    expect(escapeHtmlAttr(undefined as unknown as string)).toBe('');
    expect(escapeHtmlAttr('')).toBe('');
  });

  test('passes through safe strings unchanged', () => {
    expect(escapeHtmlAttr('abc-123_OK')).toBe('abc-123_OK');
  });
});

describe('parseQueryParams', () => {
  test('parses query string correctly', () => {
    const result = parseQueryParams('?foo=bar&baz=qux');
    expect(result).toEqual({ foo: 'bar', baz: 'qux' });
  });

  test('handles empty query string', () => {
    expect(parseQueryParams('')).toEqual({});
    expect(parseQueryParams('?')).toEqual({});
  });

  test('decodes URL-encoded values', () => {
    const result = parseQueryParams('?name=hello%20world');
    expect(result.name).toBe('hello world');
  });
});

describe('buildUrl', () => {
  test('builds URL with parameters', () => {
    const result = buildUrl('/api/test', { foo: 'bar', baz: 'qux' });
    expect(result).toContain('/api/test');
    expect(result).toContain('foo=bar');
    expect(result).toContain('baz=qux');
  });

  test('skips null and empty values', () => {
    const result = buildUrl('/api/test', { foo: 'bar', empty: '', nil: null });
    expect(result).toContain('foo=bar');
    expect(result).not.toContain('empty=');
    expect(result).not.toContain('nil=');
  });
});

describe('deepClone', () => {
  test('clones simple objects', () => {
    const obj = { a: 1, b: 2 };
    const clone = deepClone(obj);
    expect(clone).toEqual(obj);
    expect(clone).not.toBe(obj);
  });

  test('clones nested objects', () => {
    const obj = { a: { b: { c: 1 } } };
    const clone = deepClone(obj);
    expect(clone).toEqual(obj);
    clone.a.b.c = 2;
    expect(obj.a.b.c).toBe(1);
  });

  test('clones arrays', () => {
    const arr = [1, 2, [3, 4]] as [number, number, number[]];
    const clone = deepClone(arr);
    expect(clone).toEqual(arr);
    (clone[2] as number[])[0] = 99;
    expect((arr[2] as number[])[0]).toBe(3);
  });

  test('returns primitives as-is', () => {
    expect(deepClone(null)).toBe(null);
    expect(deepClone(42)).toBe(42);
    expect(deepClone('test')).toBe('test');
  });
});

describe('isValidEmail', () => {
  test('validates correct email formats', () => {
    expect(isValidEmail('test@example.com')).toBe(true);
    expect(isValidEmail('user.name@domain.co.uk')).toBe(true);
    expect(isValidEmail('user+tag@example.org')).toBe(true);
  });

  test('rejects invalid email formats', () => {
    expect(isValidEmail('notanemail')).toBe(false);
    expect(isValidEmail('missing@domain')).toBe(false);
    expect(isValidEmail('@nodomain.com')).toBe(false);
    expect(isValidEmail('spaces in@email.com')).toBe(false);
  });

  test('returns false for empty/null values', () => {
    expect(isValidEmail('')).toBe(false);
    expect(isValidEmail(null as unknown as string)).toBe(false);
    expect(isValidEmail(undefined as unknown as string)).toBe(false);
  });
});

describe('formatRampSchedule', () => {
  test('formats known schedules', () => {
    expect(formatRampSchedule('immediate')).toBe('Immediate');
    expect(formatRampSchedule('weekly-25pct')).toBe('Weekly 25%');
    expect(formatRampSchedule('monthly-10pct')).toBe('Monthly 10%');
    expect(formatRampSchedule('custom')).toBe('Custom');
  });

  test('returns input for unknown schedules', () => {
    expect(formatRampSchedule('unknown')).toBe('unknown');
  });

  test('handles null/undefined', () => {
    expect(formatRampSchedule(null as unknown as string)).toBe('Unknown');
    expect(formatRampSchedule(undefined as unknown as string)).toBe('Unknown');
  });
});

describe('getStatusBadge', () => {
  test('returns the muted tone for disabled plans', () => {
    const result = getStatusBadge(false, true);
    expect(result.class).toBe('muted');
    expect(result.label).toBe('Disabled');
  });

  test('returns the success tone for enabled + auto purchase', () => {
    const result = getStatusBadge(true, true);
    expect(result.class).toBe('success');
    expect(result.label).toBe('Active');
  });

  test('returns the info tone (not a warning) for enabled without auto purchase', () => {
    const result = getStatusBadge(true, false);
    expect(result.class).toBe('info');
    expect(result.label).toBe('Manual');
  });
});

describe('calculatePaybackMonths', () => {
  test('calculates correct payback period', () => {
    expect(calculatePaybackMonths(1200, 100)).toBe(12);
    expect(calculatePaybackMonths(600, 100)).toBe(6);
    expect(calculatePaybackMonths(150, 100)).toBe(2);
  });

  test('rounds up partial months', () => {
    expect(calculatePaybackMonths(550, 100)).toBe(6);
  });

  test('returns 0 for zero/negative savings', () => {
    expect(calculatePaybackMonths(1000, 0)).toBe(0);
    expect(calculatePaybackMonths(1000, -50)).toBe(0);
  });

  test('returns 0 for zero/negative upfront', () => {
    expect(calculatePaybackMonths(0, 100)).toBe(0);
    expect(calculatePaybackMonths(-100, 100)).toBe(0);
  });
});
