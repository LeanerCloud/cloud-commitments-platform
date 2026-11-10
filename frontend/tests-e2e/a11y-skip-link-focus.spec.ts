/**
 * The "Skip to main content" link must move keyboard focus into the main
 * content (issue #1730). Before the fix the URL hash changed but
 * document.activeElement stayed on <body>, so the next Tab restarted from the
 * top of the page. Driven with real key presses: jsdom does not move focus.
 */

import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

test('the skip link moves focus to the main content and the next Tab stays inside it', async ({ page }) => {
  await seedAuth(page);
  await mockApi(page);
  await page.goto('/');

  const skip = page.locator('a.skip-to-main');
  await page.keyboard.press('Tab');
  await expect(skip).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(page.locator('#main-content')).toBeFocused();

  await page.keyboard.press('Tab');
  expect(await page.evaluate(() => document.getElementById('main-content')!.contains(document.activeElement)
    && document.activeElement !== document.getElementById('main-content'))).toBe(true);
});
