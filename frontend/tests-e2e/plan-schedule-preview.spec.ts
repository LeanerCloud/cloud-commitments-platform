import { test, expect, Locator } from '@playwright/test';

test.use({ hasTouch: true });
import { mockApi, seedAuth } from './fixtures/recs';
import { response as editorResponse } from '../src/__tests__/fixtures/plan-editor';

const noTickOverlap = async (plot: Locator, expectThinned = false): Promise<void> => {
  const boxes = await plot.locator('.preview-tick:not([hidden])').evaluateAll(ticks => ticks.map(tick => { const box = tick.getBoundingClientRect(); return [box.left, box.right]; }));
  expect(boxes.length).toBeGreaterThan(0);
  const thinned = await plot.locator('.preview-tick[hidden]').evaluateAll(ticks => ticks.map(tick => { const box = tick.getBoundingClientRect(); return box.width * box.height; }));
  if (expectThinned) expect(thinned.length).toBeGreaterThan(0);
  expect(thinned.every(area => area === 0)).toBe(true);
  boxes.sort((a, b) => a[0]! - b[0]!).slice(1).forEach((box, i) => expect(box[0]!).toBeGreaterThanOrEqual(boxes[i]![1]!));
};

for (const width of [1440, 1280, 390, 320]) test.describe(`viewport ${width}px`, () => {
  test.use({ isMobile: width <= 390 });
  test(`inline Create/Edit schedule preview at ${width}px (mock API, no writes)`, async ({ page, isMobile }, testInfo) => {
    test.setTimeout(90_000);
    await page.setViewportSize({ width, height: 900 });
    const errors: string[] = [], writes: string[] = [], previews: string[] = [];
    const inViewport = async (locator: Locator): Promise<void> => {
      await expect(locator).toBeVisible();
      const box = (await locator.boundingBox())!, view = page.viewportSize()!;
      expect(box.x).toBeGreaterThanOrEqual(0); expect(box.y).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(view.width); expect(box.y + box.height).toBeLessThanOrEqual(view.height);
    };
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    page.on('requestfailed', request => errors.push(request.url()));
    page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`); });
    page.on('request', request => {
      if (!request.url().includes('/api/') || request.method() === 'GET') return;
      (request.url().endsWith('/editor/preview') ? previews : writes).push(request.url());
    });
    await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.fallback() : route.abort());
    await seedAuth(page);
    await mockApi(page);
    const plan = { id: 'draft-plan', name: 'Existing plan', enabled: true, auto_purchase: false, notification_days_before: 3,
      services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 80 } },
      ramp_schedule: { type: 'weekly', current_step: 1, total_steps: 4, step_interval_days: 7, percent_per_step: 25 } };
    const editor = { ...editorResponse(), plan: { ...editorResponse().plan, id: plan.id, name: plan.name } };
    await page.route('**/api/plans**', route => route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/accounts') ? []
      : new URL(route.request().url()).pathname.endsWith('/draft-plan/editor') ? editor
      : new URL(route.request().url()).pathname.endsWith('/draft-plan') ? plan : { plans: [plan] } }));
    await page.route('**/api/purchases/planned', route => route.fulfill({ json: { purchases: [] } }));
    await page.route('**/api/commitment-options**', route => route.fulfill({ json: { status: 'ok', aws: {} } }));
    await page.goto('/plans');
    const scripts = await page.locator('script[src]').evaluateAll(elements => elements.map(element => (element as HTMLScriptElement).src));
    expect(scripts.some(source => /app\.[a-f0-9]+\.js/.test(source))).toBe(true);
    await page.emulateMedia({ reducedMotion: width > 390 ? 'no-preference' : 'reduce' });
    await page.locator('#new-plan-btn').click();
    const parent = page.locator('#plan-modal');
    await expect(parent).toBeVisible();
    await expect(parent.locator('#plan-name')).toBeFocused();
    const advanced = parent.locator('#plan-advanced-settings'), summary = advanced.locator(':scope > summary');
    await expect(advanced).not.toHaveAttribute('open', '');
    await summary.focus(); await summary.press('Enter'); await expect(advanced).toHaveAttribute('open', '');
    await expect(parent.locator('#plan-description')).toBeDisabled();
    await expect(advanced).toContainText('Descriptions are unavailable and are not saved');
    const payment = await parent.locator('#plan-payment').inputValue();
    await advanced.scrollIntoViewIfNeeded(); await page.screenshot({ path: testInfo.outputPath('create-advanced.png'), fullPage: true });
    await summary.press('Space'); await expect(advanced).not.toHaveAttribute('open', '');
    expect(await parent.locator('#plan-payment').inputValue()).toBe(payment);

    await page.locator('#plan-name').fill('Capacity ladder');
    await parent.locator('label.ramp-option').filter({ hasText: 'Weekly 25%' }).click();
    const panel = parent.locator('#plan-editor-panel'), date = panel.locator('[data-editor="date"]');
    await expect(panel).toBeVisible(); await expect(panel).toContainText('Save Plan does not save date changes');
    await expect(panel.locator('[data-preview-step]')).toHaveCount(4);
    await noTickOverlap(panel);
    await expect(panel.locator('svg [role="button"]')).toHaveCount(6);
    const node = await date.elementHandle(), initial = await date.inputValue();
    await date.focus(); await date.press('ArrowLeft'); await date.press('Backspace');
    expect(await date.evaluate(element => element.validity.badInput)).toBe(true);
    expect(await date.evaluate(element => element.form)).toBeNull();
    await expect(parent.locator('button[type="submit"]')).toBeDisabled();
    expect(await parent.locator('#plan-form').evaluate(element => (element as HTMLFormElement).checkValidity())).toBe(true);
    const next = new Date(Date.parse(`${initial}T00:00:00Z`) + 86_400_000).toISOString().slice(0, 10);
    await date.fill(next); await date.press('Enter');
    await expect(date).toBeFocused(); expect(await node!.evaluate(element => element.isConnected)).toBe(true);
    await expect(panel.locator('[data-editor="changes"] summary')).toHaveText('Local date changes (1)');
    await expect(panel.locator('[data-preview-step="0"]')).toHaveAttribute('aria-label', new RegExp(next));
    await parent.locator('#plan-account-search').fill('AWS');
    await parent.locator('.account-suggestion-item').first().click(); await expect(parent.locator('button[type="submit"]')).toBeEnabled();
    await summary.click();
    await parent.locator('#plan-notify-days').fill('1.5'); await summary.click();
    await parent.locator('button[type="submit"]').click();
    await expect(advanced).toHaveAttribute('open', '');
    await expect(parent.locator('#plan-notify-days')).toBeFocused();
    expect(await parent.locator('#plan-notify-days').evaluate(element => element.validity.stepMismatch)).toBe(true);
    await parent.locator('#plan-notify-days').fill('3'); await summary.click();
    await parent.locator('#plan-name').fill(''); await parent.locator('button[type="submit"]').click();
    await expect(advanced).not.toHaveAttribute('open', '');
    await expect(parent.locator('#plan-name')).toBeFocused(); await parent.locator('#plan-name').fill('Capacity ladder');
    await expect(panel.locator('.editor-help summary')).toHaveText('Keyboard and touch help');
    const svg = panel.locator('svg'), point = panel.locator('[data-preview-step="1"]');
    const originalDate = next.slice(0, 10), add = (value: string, days: number) => new Date(Date.parse(value) + days * 86400000).toISOString().slice(0, 10);
    const hit = point.locator('.preview-hit'); await hit.scrollIntoViewIfNeeded();
    const box = (await hit.boundingBox())!, plot = (await svg.boundingBox())!;
    const span = (Date.parse(await panel.locator('[data-editor="end"]').inputValue()) - Date.parse(await panel.locator('[data-editor="start"]').inputValue())) / 86400000 + 8;
    const day = plot.width * 544 / 640 / span, x = box.x + box.width / 2, y = box.y + box.height / 2;
    await page.mouse.move(x, y); await page.mouse.down();
    await page.mouse.move(x + day, y); await expect(date).toBeDisabled();
    expect(await parent.locator('#plan-form').evaluate(element => element.inert)).toBe(true);
    const dragDate = await date.inputValue();
    expect(await parent.locator('button[type="submit"]').evaluate(element => element.inert)).toBe(true);
    expect(await parent.locator('#plan-form').evaluate(element => element.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))).toBe(false);
    await page.keyboard.press('Escape'); await page.mouse.up();
    await expect(parent).toBeVisible(); await expect(date).toBeEnabled();
    expect(await parent.locator('#plan-form').evaluate(element => element.inert)).toBe(false);
    await expect(parent.locator('button[type="submit"]')).toBeEnabled();
    await point.press('Alt+ArrowRight'); await expect(date).toHaveValue(dragDate);
    const handle = panel.locator('[data-preview-boundary="end"]'); await handle.press('Enter');
    await expect(panel.locator('[data-editor="end"]')).toBeFocused();
    const endHit = handle.locator('.preview-handle-hit'); await endHit.scrollIntoViewIfNeeded();
    const endBox = (await endHit.boundingBox())!, endBefore = await panel.locator('[data-editor="end"]').inputValue();
    await page.mouse.move(endBox.x + 22, endBox.y + 22); await page.mouse.down();
    await page.mouse.move(endBox.x + 22 + day, endBox.y + 22); await page.mouse.up();
    await expect(panel.locator('[data-editor="end"]')).toHaveValue(add(endBefore, 1));
    await hit.scrollIntoViewIfNeeded(); const cancelBox = (await hit.boundingBox())!;
    await page.mouse.move(cancelBox.x + 22, cancelBox.y + 22); await page.mouse.down();
    await parent.locator('.plan-editor-layout').evaluate(element => { element.scrollTop += 30; });
    await expect(date).toBeEnabled(); await expect(panel.locator('[data-editor="error"]')).toContainText('Graph moved'); await page.mouse.up();
    if (width <= 390) {
      await hit.scrollIntoViewIfNeeded(); const touchBox = (await hit.boundingBox())!;
      const session = await page.context().newCDPSession(page), beforeTouch = await date.inputValue();
      try {
        const tx = touchBox.x + touchBox.width / 2, ty = touchBox.y + touchBox.height / 2;
        await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: tx, y: ty, id: 1 }] });
        await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: tx + day, y: ty, id: 1 }] });
        await expect(date).toHaveValue(add(beforeTouch, 1));
        await session.send('Input.dispatchTouchEvent', { type: 'touchCancel', touchPoints: [] });
        await expect(date).toHaveValue(beforeTouch); await expect(date).toBeEnabled();
        await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: tx, y: ty, id: 1 }] });
        await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: tx + day, y: ty, id: 1 }] });
        await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
        await expect(date).toHaveValue(add(beforeTouch, 1)); await expect(date).toBeEnabled();
      } finally { await session.detach(); }
    }
    await panel.locator('[data-editor="reset"]').click();
    await expect(panel.locator('[data-editor="start"]')).toHaveValue(add(originalDate, -1));
    expect(await parent.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
    await parent.locator('.plan-editor-layout').evaluate(element => { element.scrollTop = 0; });
    const graphBox = (await panel.boundingBox())!, settingsBox = (await parent.locator('#plan-form').boundingBox())!;
    if (width > 800) { expect(graphBox.x + graphBox.width).toBeLessThanOrEqual(settingsBox.x); expect(Math.round(settingsBox.width)).toBe(300); }
    else expect(settingsBox.y).toBeGreaterThanOrEqual(graphBox.y + graphBox.height);
    await page.screenshot({ path: testInfo.outputPath('create-inline-graph.png'), fullPage: true });
    await panel.locator('.editor-help').scrollIntoViewIfNeeded(); await page.screenshot({ path: testInfo.outputPath('create-inline-help.png'), fullPage: true });
    await parent.locator('#close-plan-modal-btn').scrollIntoViewIfNeeded();
    const footer = (await parent.locator('.modal-buttons').boundingBox())!;
    expect(footer.y + footer.height).toBeLessThanOrEqual(900);
    await page.screenshot({ path: testInfo.outputPath('create-inline-actions.png'), fullPage: true });
    await parent.locator('#close-plan-modal-btn').focus(); await page.keyboard.press('Tab');
    await expect(parent.locator('button[type="submit"]')).toBeFocused(); await page.keyboard.press('Tab');
    await expect(panel.locator('.schedule-preview-scroll')).toBeFocused();
    await page.keyboard.press('Shift+Tab'); await expect(parent.locator('button[type="submit"]')).toBeFocused();
    await page.keyboard.press('Shift+Tab'); await expect(parent.locator('#close-plan-modal-btn')).toBeFocused();
    await summary.click(); await expect(advanced).toHaveAttribute('open', '');
    await hit.scrollIntoViewIfNeeded(); const closeBox = (await hit.boundingBox())!;
    await page.mouse.move(closeBox.x + 22, closeBox.y + 22); await page.mouse.down();
    expect(await parent.locator('#plan-form').evaluate(element => element.inert)).toBe(true);
    await parent.locator('#close-plan-modal-btn').evaluate(element => (element as HTMLButtonElement).click());
    await page.mouse.up(); await expect(parent).toBeHidden();
    expect(await parent.locator('#plan-form').evaluate(element => element.inert)).toBe(false);
    await page.locator('#new-plan-btn').click(); await parent.locator('#plan-name').fill('Capacity ladder');
    await expect(advanced).not.toHaveAttribute('open', '');
    await parent.locator('label.ramp-option').filter({ hasText: 'Weekly 25%' }).click();
    await expect(panel.locator('[data-editor="changes"] summary')).toHaveText('Local date changes (0)');
    await expect(panel.locator('[data-editor="date"]')).toBeEnabled();

    // Inline preview contract: exactly one dialog, no popup element, no
    // launcher button, and form values untouched by panel interactions.
    const formFields = '#plan-form input, #plan-form select, #plan-form textarea';
    const before = await parent.locator(formFields).evaluateAll(elements => elements.map(element => (element as HTMLInputElement).value));
    await expect(page.locator('[role="dialog"]:visible')).toHaveCount(1);
    await expect(page.locator('#plan-schedule-preview')).toHaveCount(0);
    await expect(page.locator('#preview-plan-schedule-btn')).toHaveCount(0);
    await expect(panel.locator('.editor-target')).toContainText('target 80%');

    // Point selection and keyboard navigation stay inside the one dialog.
    const third = panel.locator('[data-preview-step="2"]');
    await third.locator('.preview-hit').click();
    await expect(panel.locator('[data-editor="step"]')).toHaveValue('2');
    await expect(third).toHaveAttribute('aria-pressed', 'true');
    await third.press('ArrowRight');
    await expect(panel.locator('[data-preview-step="3"]')).toBeFocused();
    await expect(panel.locator('[data-editor="step"]')).toHaveValue('3');

    // Range inputs redistribute the contiguous block; an inverted range
    // reports inline and keeps the typed values for correction.
    const rangeStart = panel.locator('[data-editor="start"]'), rangeEnd = panel.locator('[data-editor="end"]');
    const original = await rangeStart.inputValue();
    const addDays = (value: string, days: number): string => new Date(Date.parse(`${value}T00:00:00Z`) + days * 86_400_000).toISOString().slice(0, 10);
    await rangeStart.fill(addDays(original, 25)); await rangeStart.dispatchEvent('change');
    await expect(panel.locator('[data-editor="error"]')).toContainText('calendar day');
    await expect(rangeStart).toHaveValue(addDays(original, 25));
    await expect(panel.locator('[data-editor="step"]')).toBeDisabled();
    await rangeEnd.fill(addDays(original, 32)); await rangeEnd.dispatchEvent('change');
    await expect(rangeEnd).toBeFocused();
    await expect(panel.locator('[data-preview-step="2"]')).toHaveAttribute('aria-label', new RegExp(addDays(original, 30)));
    await expect(panel.locator('[data-editor="changes"] summary')).toHaveText('Local date changes (4)');
    await page.screenshot({ path: testInfo.outputPath('inline-preview-redistributed.png'), fullPage: true });
    await panel.locator('[data-editor="reset"]').click();
    await expect(rangeStart).toHaveValue(original);
    await expect(panel.locator('[data-editor="changes"] summary')).toHaveText('Local date changes (0)');
    expect(await parent.locator(formFields).evaluateAll(elements => elements.map(element => (element as HTMLInputElement).value))).toEqual(before);

    // Escape with no active drag closes the one and only dialog.
    await page.keyboard.press('Escape');
    await expect(parent).toBeHidden();
    await expect(page.locator('#new-plan-btn')).toBeFocused();
    await page.locator('[data-action="edit-plan"][data-id="draft-plan"]').click();
    const edit = page.locator('#plan-modal'), stored = edit.locator('#plan-editor-panel');
    await expect(page.locator('[role="dialog"]:visible')).toHaveCount(1);
    await expect(page.locator('#plan-schedule-preview')).toHaveCount(0);
    await expect(page.locator('#preview-plan-schedule-btn')).toHaveCount(0);
    await expect(stored.locator('[data-edit="heading"]')).toBeFocused();
    await expect(edit.locator('#plan-form')).toBeVisible();
    await expect(edit.locator('#plan-name')).toHaveValue('Existing plan');
    await expect(edit.locator('#plan-term')).toBeDisabled();
    await expect(stored.locator('.preview-tick[hidden]')).toHaveCount(1);
    await noTickOverlap(stored, true);
    const card = edit.locator('label.ramp-option').first();
    expect(await card.evaluate(element => { const style = getComputedStyle(element); return [style.opacity, style.cursor]; })).toEqual(['0.6', 'not-allowed']);
    await expect(edit.locator('#plan-max-step-commitment-reason')).toHaveText('Per-step cap editing is not available yet');
    const storedBox = (await stored.boundingBox())!, formBox = (await edit.locator('#plan-form').boundingBox())!;
    if (width > 800) { expect(storedBox.x + storedBox.width).toBeLessThanOrEqual(formBox.x); expect(Math.round(formBox.width)).toBe(300); }
    else expect(formBox.y).toBeGreaterThanOrEqual(storedBox.y + storedBox.height);
    expect(await edit.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
    await expect(edit.locator('button[type="submit"]')).toBeDisabled();
    await expect(stored).toContainText('Saving changes is not available');
    const reason = page.locator('#plan-review-reason');
    await expect(reason).toHaveText('Choose a date for step 4.'); await inViewport(reason);
    await expect(edit.locator('button[type="submit"]')).toHaveAttribute('aria-describedby', 'plan-review-reason');
    await page.screenshot({ path: testInfo.outputPath('edit-review-disabled-reason.png') });
    await expect(stored.locator('[data-edit="service"] option')).toHaveCount(2);
    await expect(stored.locator('[data-edit="slots"] li')).toHaveCount(5);
    const selected = stored.locator('[data-edit="step"]'), proposed = stored.locator('[data-edit="date"]');
    await expect(proposed).toBeDisabled();
    await selected.selectOption('1'); await expect(proposed).toBeEnabled();
    await expect(proposed).toHaveValue('2026-11-06');
    const handles = stored.locator('[data-preview-boundary]');
    await expect(handles).toHaveCount(2);
    for (const handle of await handles.all()) expect(await handle.evaluate(element => getComputedStyle(element).display)).toBe('none');
    await stored.locator('[data-preview-step="1"]').focus();
    for (let presses = 0; presses < 6; presses++) {
      await page.keyboard.press('Tab');
      expect(await page.evaluate(() => document.activeElement?.closest('[data-preview-boundary]') !== null)).toBe(false);
    }
    const marker = stored.locator('[data-preview-step="1"] .preview-marker');
    await marker.scrollIntoViewIfNeeded();
    const mark = (await marker.boundingBox())!, tick = (await stored.locator('[data-preview-step="2"] .preview-marker').boundingBox())!;
    const dayPx = (tick.x - mark.x) / 3, startX = mark.x + mark.width / 2, startY = mark.y + mark.height / 2;
    await page.mouse.move(startX, startY); await page.mouse.down(); await page.mouse.move(startX + dayPx * 1.2, startY, { steps: 4 });
    await expect(proposed).toHaveValue('2026-11-07');
    await page.screenshot({ path: testInfo.outputPath('edit-during-drag.png') });
    await page.mouse.up();
    await expect(proposed).toHaveValue('2026-11-07');
    await expect(stored.locator('[data-edit="changes"]')).toContainText('Step 2: 2026-11-06 → 2026-11-07');
    await page.screenshot({ path: testInfo.outputPath('edit-after-drag.png'), fullPage: true });
    const fixed = stored.locator('[data-preview-step="0"]');
    expect(await fixed.evaluate(element => element.hasAttribute('data-fixed'))).toBe(true);
    await stored.locator('[data-edit="reset"]').click();
    await expect(proposed).toHaveValue('2026-11-06');
    await stored.locator('[data-edit="service"]').selectOption('disabledOther');
    await expect(stored.locator('[data-preview-step="1"]')).toHaveAttribute('aria-label', /target 30%/);
    await selected.selectOption('2'); await expect(proposed).toBeDisabled();
    const consent = stored.locator('[data-edit="approved"]'); await expect(consent).not.toBeChecked();
    await consent.check(); await expect(proposed).toBeEnabled();
    await proposed.fill('2026-11-10'); await proposed.dispatchEvent('change');
    await consent.uncheck(); await expect(proposed).toBeDisabled();
    await expect(stored.locator('[data-edit="status"]')).toContainText('Re-enable approved edits');
    await stored.locator('[data-edit="reset"]').click(); await expect(proposed).toHaveValue('2026-11-09');
    await selected.selectOption('3'); await expect(proposed).toHaveValue('');
    await proposed.fill('2026-11-12'); await proposed.dispatchEvent('change');
    await expect(stored.locator('[data-edit="slots"]')).toContainText('2026-11-12');
    await selected.selectOption('4'); await expect(proposed).toBeDisabled();
    await stored.getByText('Retained execution history', { exact: true }).click();
    await expect(stored.locator('[data-edit="history"]')).toContainText('Step 8; failed');
    await stored.getByText('Stored settings and accounts', { exact: true }).click();
    await expect(stored.locator('[data-edit="settings"]')).toContainText('unspecified / none');
    await expect(edit.locator('button[type="submit"]')).toHaveText('Review changes');
    await page.route('**/api/plans/draft-plan/editor/preview', route => route.fulfill({ json: { reviewed_diff: 'sha256:' + 'a'.repeat(64),
      required_cancellation_ids: [], required_reapproval_ids: [], diff: { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [],
        totals: { moved: 3, created: 0, canceled: 0, approvals_affected: 0 } } } }));
    await expect(edit.locator('button[type="submit"]')).toBeEnabled();
    await edit.locator('button[type="submit"]').click();
    const totals = page.locator('#plan-review-feedback [role="status"]');
    await expect(totals).toHaveText('3 dates moved; 0 steps removed; 0 approvals affected'); await inViewport(totals);
    await page.screenshot({ path: testInfo.outputPath('edit-review-totals.png') });
    expect(previews).toHaveLength(1);
    await expect(edit).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('edit-complete-get.png'), fullPage: true });
    expect(await edit.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
    await page.keyboard.press('Escape'); await expect(edit).toBeHidden();
    await page.locator('#new-plan-btn').click();
    await expect(page.locator('#plan-provider')).toHaveValue('aws');
    await expect(page.locator('#plan-term')).toHaveValue('3');
    await expect(page.locator('#plan-payment')).toHaveValue('no-upfront');
    expect(await parent.locator('label.ramp-option').first().evaluate(element => { const style = getComputedStyle(element); return [style.opacity, style.cursor]; })).toEqual(['1', 'pointer']);
    const dense = parent.locator('#plan-editor-panel');
    await parent.locator('label.ramp-option').filter({ hasText: 'Define your own schedule' }).click();
    await page.locator('#ramp-step-percent').fill('1');
    await expect(dense.locator('[data-preview-step]')).toHaveCount(100);
    await noTickOverlap(dense);
    expect((await dense.locator('svg').boundingBox())!.width).toBeGreaterThanOrEqual(4896);
    expect(await dense.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
    if (width <= 390) {
      const scroll = dense.locator('.schedule-preview-scroll');
      await scroll.scrollIntoViewIfNeeded();
      const box = (await scroll.boundingBox())!, svgBox = (await dense.locator('svg').boundingBox())!;
      const y = svgBox.y + svgBox.height * 304 / 370, left = box.x + 40, right = box.x + box.width - 40;
      const hit = await page.evaluate(({ x, y }) => { const element = document.elementFromPoint(x, y); return { tag: element?.tagName, control: !!element?.closest("[data-preview-step],[data-preview-boundary]"), action: element ? getComputedStyle(element).touchAction : null }; }, { x: right, y });
      expect(hit.tag).toBe('svg'); expect(hit.control).toBe(false); expect(hit.action).toBe('auto');
      const beforeScroll = await scroll.evaluate(element => element.scrollLeft);
      const labels = await dense.locator('[data-preview-step]').evaluateAll(elements => elements.map(element => element.getAttribute('aria-label')));
      const session = await page.context().newCDPSession(page);
      try {
        await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: right, y, id: 1 }] });
        for (let move = 1; move <= 12; move++) { await page.evaluate(() => new Promise(requestAnimationFrame)); await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: right + (left - right) * move / 12, y, id: 1 }] }); }
        await session.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
        await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
        await expect.poll(() => scroll.evaluate(element => element.scrollLeft)).toBeGreaterThan(beforeScroll);
        expect(await dense.locator('[data-preview-step]').evaluateAll(elements => elements.map(element => element.getAttribute('aria-label')))).toEqual(labels);
      } finally { await session.detach(); }
    }
    await dense.locator('[data-preview-step="0"]').focus();
    await page.keyboard.press('ArrowRight');
    await expect(dense.locator('[data-editor="step"]')).toHaveValue('1');
    await expect(dense.locator('[data-preview-step="1"]')).toBeFocused();
    await expect(dense.locator('[data-preview-step="1"] .preview-touch-target')).toHaveCSS('outline-width', '2px');
    await expect(dense.locator('[data-preview-step="1"] .preview-touch-target')).toHaveCSS('outline-style', 'solid');
    // The handle group's own outline stays off; the 44x44 touch target draws
    // the ring so the Start/End label and blank band are never outlined.
    const denseEnd = dense.locator('[data-preview-boundary="end"]');
    await denseEnd.focus();
    await expect(denseEnd.locator('.preview-touch-target')).toHaveCSS('outline-width', '2px');
    expect(await denseEnd.evaluate(element => getComputedStyle(element).outlineStyle)).toBe('none');
    await parent.locator('label.ramp-option').filter({ hasText: 'Weekly 25%' }).click();
    await page.locator('#plan-coverage').fill('0');
    await expect(dense.locator('[data-preview-step]')).toHaveCount(4);
    const step = dense.locator('[data-editor="step"]');
    const firstDate = await dense.locator('[data-editor="start"]').inputValue();
    const after = (days: number): string => new Date(Date.parse(`${firstDate}T00:00:00Z`) + days * 86_400_000).toISOString().slice(0, 10);
    await dense.locator('[data-editor="end"]').fill(after(365)); await dense.locator('[data-editor="end"]').dispatchEvent('change');
    for (const index of [1, 2]) {
      await step.selectOption(String(index));
      await dense.locator('[data-editor="date"]').fill(after(index)); await dense.locator('[data-editor="date"]').dispatchEvent('change');
    }
    const positions = await dense.locator('.preview-hit').evaluateAll(elements => elements.map(element => Number(element.getAttribute('x')) + 22));
    expect(positions[1]! - positions[0]!).toBeLessThan(3);
    for (let index = 0; index < 4; index++) {
      await step.selectOption(String(index));
      await expect(dense.locator('[data-editor="date"]')).toHaveValue(after(index === 3 ? 365 : index));
    }
    await dense.locator('[data-preview-step="0"]').focus();
    for (let index = 1; index < 4; index++) {
      await page.keyboard.press('ArrowRight');
      await expect(dense.locator('[data-editor="step"]')).toHaveValue(String(index));
    }
    await parent.locator('label.ramp-option:has(input[value="immediate"])').click();
    await expect(dense.locator('[data-editor="error"]')).toContainText('Rebuild proposed dates');
    await dense.locator('[data-editor="rebuild"]').click();
    await expect(dense.locator('[data-preview-step]')).toHaveCount(1);
    await expect(dense.locator('[data-editor="end-label"]')).toBeHidden();
    await expect(dense.locator('[data-editor="end"]')).toBeDisabled();
    await dense.locator('[data-editor="start"]').fill(after(3)); await dense.locator('[data-editor="start"]').dispatchEvent('change');
    await expect(dense.locator('[data-editor="date"]')).toHaveValue(after(3));
    await page.screenshot({ path: testInfo.outputPath('inline-preview-single.png'), fullPage: true });
    await page.keyboard.press('Escape');
    await expect(parent).toBeHidden();
    await expect(page.locator('#new-plan-btn')).toBeFocused();
    expect(writes).toEqual([]);
    expect(errors).toEqual([]);
    await testInfo.attach('production-preview-receipt', { body: JSON.stringify({ width, isMobile, scripts, writes, errors }), contentType: 'application/json' });
  });
});
