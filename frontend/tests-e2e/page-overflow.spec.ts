/**
 * Page-level horizontal scroll guard (issue #703).
 *
 * A wide table, a fieldset's min-content width, a hidden-but-laid-out
 * tooltip or a non-wrapping toolbar each push the document wider than the
 * viewport, so the whole page scrolls sideways. jsdom has no layout, so this
 * runs in Chromium and compares the document's scroll width to the viewport.
 */

import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

const PAGES = [
  '/home',
  '/opportunities',
  '/plans',
  '/purchases',
  '/inventory',
  '/admin/general',
  '/admin/purchasing',
  '/admin/accounts',
  '/admin/users',
];
const WIDTHS = [1280, 768, 390];

for (const width of WIDTHS) {
  for (const path of PAGES) {
    test(`${path} does not scroll horizontally at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await seedAuth(page);
      await mockApi(page);
      await page.goto(path);
      await page.waitForLoadState('networkidle');
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }
}
