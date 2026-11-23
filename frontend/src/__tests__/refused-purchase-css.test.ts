/** Height caps for long failure text (issue #1694); jsdom has no layout, so assert the CSS rules. */
import * as fs from 'fs';
import * as path from 'path';

const stylesDir = path.join(__dirname, '../styles');
const css = fs.readdirSync(stylesDir).filter((f) => /^components.*\.css$/.test(f)).sort().map((f) => fs.readFileSync(path.join(stylesDir, f), 'utf8')).join('\n');
const rule = (sel: string): string => {
  const m = css.match(new RegExp(`(?:^|\\n)${sel.replace('.', '\\.')}\\s*\\{([^}]*)\\}`));
  return m?.[1] ?? '';
};

test('toast message is height-capped and scrolls', () => {
  const r = rule('.toast-message');
  expect(r).toMatch(/max-height:\s*\d/);
  expect(r).toMatch(/overflow-y:\s*auto/);
});

test('history status description is line-clamped', () => {
  const r = rule('.history-status-desc');
  expect(r).toMatch(/-webkit-line-clamp:\s*\d/);
  expect(r).toMatch(/overflow:\s*hidden/);
});
