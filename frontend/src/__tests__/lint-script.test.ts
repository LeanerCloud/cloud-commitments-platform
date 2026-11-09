/**
 * Guard the lint script's glob (issue #1645).
 *
 * npm runs scripts through sh, which has no globstar: an unquoted
 * `eslint src/**\/*.ts` expands `**` as `*`, so ESLint never saw the top-level
 * src/*.ts files and CI's `npm run lint` passed over them. A quoted glob (or a
 * plain directory) is expanded by ESLint itself, so the script must leave no
 * unquoted `*` for sh to expand.
 */
import fs from 'fs';
import path from 'path';

describe('package.json lint script', () => {
  it('leaves no unquoted glob for sh to expand', () => {
    const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', '..', 'package.json'), 'utf8'));
    const lint: string = pkg.scripts.lint;
    const unquoted = lint.replace(/'[^']*'|"[^"]*"/g, '');
    expect(unquoted).not.toContain('*');
    expect(lint).toContain('eslint');
  });
});
