/**
 * Keyboard access to the plan ramp options (issue #750).
 *
 * The ramp radios were `display: none`, which removed them from the tab order
 * so the cards could only be chosen with a mouse. jsdom has no layout or tab
 * order, so this runs in Chromium: Tab must land on a ramp radio and the card
 * must draw a visible focus ring.
 */

import { test, expect } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

test('ramp option radios are reachable by Tab and show a focus ring', async ({ page }) => {
  await seedAuth(page);
  await mockApi(page);
  await page.goto('/plans');
  await page.getByRole('button', { name: 'New Plan' }).click();

  const radio = page.locator('.ramp-option input[type="radio"]').first();
  await expect(radio).toBeAttached();
  for (let i = 0; i < 80; i++) {
    if (await radio.evaluate((el) => el === document.activeElement)) break;
    await page.keyboard.press('Tab');
  }
  await expect(radio).toBeFocused();

  // box-shadow animates with the card's transition; wait for the settled ring.
  await expect
    .poll(() => radio.evaluate((el) => getComputedStyle(el.closest('.ramp-option')!).boxShadow))
    .toContain('rgb(37, 99, 235)');
});
