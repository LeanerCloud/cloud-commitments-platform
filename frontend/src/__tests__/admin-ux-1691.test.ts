/** Admin UX nits (issue #1691); jsdom has no layout, so assert the CSS rules and hooks. */
import * as fs from 'fs';
import * as path from 'path';

const read = (f: string): string => fs.readFileSync(path.join(__dirname, '..', f), 'utf8');
const rule = (css: string, sel: string): string => {
  const esc = sel.replace(/[.[\]"=():>*]/g, '\\$&');
  const m = css.match(new RegExp(`(?:^|\\n)${esc}\\s*\\{([^}]*)\\}`));
  return m?.[1] ?? '';
};

test('sidebar is sticky under the top bar', () => {
  const r = rule(read('styles/layout.css'), '.app-sidebar');
  expect(r).toMatch(/position:\s*sticky/);
  expect(r).toMatch(/top:\s*var\(--cudly-topbar-h\)/);
  expect(r).toMatch(/overflow-y:\s*auto/);
});

test('global filters are hidden on the admin tab', () => {
  const r = rule(read('styles/shell.css'), 'body[data-active-tab="admin"] #topbar-filters');
  expect(r).toMatch(/display:\s*none/);
});

test('account tables share a fixed layout and column widths', () => {
  const css = read('styles/components.css');
  expect(rule(css, '.accounts-table')).toMatch(/table-layout:\s*fixed/);
  expect(rule(css, '.accounts-table')).toMatch(/min-width:\s*44rem/);
  for (const n of [2, 3, 4]) {
    expect(rule(css, `.accounts-table th:nth-child(${n})`)).toMatch(/width:\s*\d/);
  }
});

test('users date cells do not wrap', () => {
  expect(rule(read('styles/components.css'), '.users-table td.nowrap')).toMatch(/white-space:\s*nowrap/);
  const ts = read('users/userList.ts');
  expect(ts.match(/<td class="nowrap">/g)).toHaveLength(2);
});
