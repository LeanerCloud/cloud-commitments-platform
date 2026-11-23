/**
 * Tests for users utils and state
 */

// The harness registers the module mocks, so it must be imported first.
import './users-harness';

import * as userUtils from '../users/utils';

// ============================================================================
// UTILS TESTS
// ============================================================================
describe('users/utils', () => {

  describe('formatRelativeTime', () => {
    it('should return "Just now" for very recent times', () => {
      const now = new Date();
      expect(userUtils.formatRelativeTime(now.toISOString())).toBe('Just now');
    });

    it('should return "Just now" for times less than 1 minute ago', () => {
      const date = new Date(Date.now() - 30 * 1000); // 30 seconds ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('Just now');
    });

    it('should return minutes ago for times within an hour', () => {
      const date = new Date(Date.now() - 30 * 60 * 1000); // 30 mins ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('30m ago');
    });

    it('should return 1m ago for exactly 1 minute', () => {
      const date = new Date(Date.now() - 60 * 1000); // 1 minute ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('1m ago');
    });

    it('should return 59m ago for 59 minutes', () => {
      const date = new Date(Date.now() - 59 * 60 * 1000); // 59 mins ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('59m ago');
    });

    it('should return hours ago for times within a day', () => {
      const date = new Date(Date.now() - 5 * 60 * 60 * 1000); // 5 hours ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('5h ago');
    });

    it('should return 1h ago for exactly 1 hour', () => {
      const date = new Date(Date.now() - 60 * 60 * 1000); // 1 hour ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('1h ago');
    });

    it('should return 23h ago for 23 hours', () => {
      const date = new Date(Date.now() - 23 * 60 * 60 * 1000); // 23 hours ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('23h ago');
    });

    it('should return days ago for times within a week', () => {
      const date = new Date(Date.now() - 3 * 24 * 60 * 60 * 1000); // 3 days ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('3d ago');
    });

    it('should return 1d ago for exactly 1 day', () => {
      const date = new Date(Date.now() - 24 * 60 * 60 * 1000); // 1 day ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('1d ago');
    });

    it('should return 6d ago for 6 days', () => {
      const date = new Date(Date.now() - 6 * 24 * 60 * 60 * 1000); // 6 days ago
      expect(userUtils.formatRelativeTime(date.toISOString())).toBe('6d ago');
    });

    it('should return formatted date for times 7 days or older', () => {
      const date = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000); // 7 days ago
      const result = userUtils.formatRelativeTime(date.toISOString());
      // Canonical en-US short-month form: "Mar 15, 2026"
      expect(result).toMatch(/^[A-Z][a-z]{2} \d{1,2}, \d{4}$/);
    });

    it('should return formatted date for older times', () => {
      const date = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000); // 10 days ago
      const result = userUtils.formatRelativeTime(date.toISOString());
      expect(result).toMatch(/^[A-Z][a-z]{2} \d{1,2}, \d{4}$/);
    });

    it('should handle dates far in the past', () => {
      const date = new Date('2020-01-01T00:00:00Z');
      const result = userUtils.formatRelativeTime(date.toISOString());
      // TZ-adjusted — could be Dec 31 2019 or Jan 1 2020. Match shape + year.
      expect(result).toMatch(/^[A-Z][a-z]{2} \d{1,2}, (2019|2020)$/);
    });
  });

  describe('escapeHtml', () => {
    it('should escape HTML special characters', () => {
      expect(userUtils.escapeHtml('<script>alert("xss")</script>')).toBe(
        '&lt;script&gt;alert(&quot;xss&quot;)&lt;/script&gt;'
      );
    });

    it('should handle normal text', () => {
      expect(userUtils.escapeHtml('Hello World')).toBe('Hello World');
    });

    it('should escape ampersands', () => {
      expect(userUtils.escapeHtml('Tom & Jerry')).toBe('Tom &amp; Jerry');
    });

    // Regression for ARCH-07 (issue #1195): the previous local DOM
    // round-trip implementation left both quote characters unescaped,
    // so values interpolated inside double-quoted attributes (e.g. the
    // aria-label carrying the user email in userList.ts) could break
    // out of the attribute and inject markup.
    it('should escape single quotes', () => {
      const result = userUtils.escapeHtml("It's a test");
      expect(result).toBe('It&#39;s a test');
    });

    it('should escape double quotes', () => {
      const result = userUtils.escapeHtml('Say "Hello"');
      expect(result).toBe('Say &quot;Hello&quot;');
    });

    it('issue #1195: quote in attribute position cannot break out of the attribute', () => {
      const malicious = 'a@b.com" onmouseover="alert(1)';
      const div = document.createElement('div');
      div.innerHTML = `<button aria-label="${userUtils.escapeHtml(malicious)}">x</button>`;
      const btn = div.querySelector('button');
      expect(btn?.getAttribute('onmouseover')).toBeNull();
      expect(btn?.getAttribute('aria-label')).toBe(malicious);
    });

    it('should handle empty string', () => {
      expect(userUtils.escapeHtml('')).toBe('');
    });

    it('should handle multiple special characters', () => {
      expect(userUtils.escapeHtml('<div class="test">&</div>')).toBe(
        '&lt;div class=&quot;test&quot;&gt;&amp;&lt;/div&gt;'
      );
    });
  });

  describe('showError', () => {
    beforeEach(() => {
      jest.useFakeTimers();
    });

    afterEach(() => {
      jest.useRealTimers();
    });

    it('should create and display error toast', () => {
      userUtils.showError('Test error message');

      const toast = document.querySelector('.toast-error');
      expect(toast).toBeTruthy();
      // New toast DOM includes icon + close button siblings, so textContent
      // is a concatenation. The message is in its own .toast-message child.
      expect(toast?.querySelector('.toast-message')?.textContent).toBe('Test error message');
      expect(toast?.classList.contains('toast')).toBe(true);
    });

    it('should remove error toast after timeout', () => {
      userUtils.showError('Test error');

      expect(document.querySelector('.toast-error')).toBeTruthy();

      // Q4 default for errors is 30s; plus the transition-end fallback (200ms).
      jest.advanceTimersByTime(30_000);
      jest.advanceTimersByTime(200);

      expect(document.querySelector('.toast-error')).toBeFalsy();
    });

    it('should handle multiple error toasts', () => {
      userUtils.showError('Error 1');
      userUtils.showError('Error 2');

      const toasts = document.querySelectorAll('.toast-error');
      expect(toasts.length).toBe(2);
    });
  });

  describe('showSuccess', () => {
    beforeEach(() => {
      jest.useFakeTimers();
    });

    afterEach(() => {
      jest.useRealTimers();
    });

    it('should create and display success toast', () => {
      userUtils.showSuccess('Success message');

      const toast = document.querySelector('.toast-success');
      expect(toast).toBeTruthy();
      expect(toast?.querySelector('.toast-message')?.textContent).toBe('Success message');
      expect(toast?.classList.contains('toast')).toBe(true);
    });

    it('should remove success toast after timeout', () => {
      userUtils.showSuccess('Success');

      expect(document.querySelector('.toast-success')).toBeTruthy();

      // Q4: showSuccess uses a 5s timeout + 200ms transition fallback.
      jest.advanceTimersByTime(5_000);
      jest.advanceTimersByTime(200);

      expect(document.querySelector('.toast-success')).toBeFalsy();
    });

    it('should handle multiple success toasts', () => {
      userUtils.showSuccess('Success 1');
      userUtils.showSuccess('Success 2');

      const toasts = document.querySelectorAll('.toast-success');
      expect(toasts.length).toBe(2);
    });
  });
});
