/**
 * Guards the stylesheet import graph (issue #1965). css-loader silently drops
 * an @import that follows a rule, and a CSS file nobody imports never ships,
 * so neither failure shows up in a build.
 */
import fs from 'fs';
import path from 'path';

const srcDir = path.join(__dirname, '..');
const stylesDir = path.join(srcDir, 'styles');

function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '');
}

function statements(file: string): string[] {
  return stripComments(fs.readFileSync(file, 'utf8'))
    .split(/;|\{|\}/)
    .map((s) => s.trim())
    .filter(Boolean);
}

/** Returns import targets in order and fails if anything but @import precedes the last one. */
function importsOf(file: string): string[] {
  const css = stripComments(fs.readFileSync(file, 'utf8')).trim();
  const targets: string[] = [];
  let rest = css;
  const importRe = /^@import\s+(?:url\()?['"]([^'"]+)['"]\)?[^;]*;\s*/;
  for (let m = importRe.exec(rest); m; m = importRe.exec(rest)) {
    targets.push(m[1] as string);
    rest = rest.slice(m[0].length);
  }
  if (/@import\b/.test(rest)) {
    throw new Error(`${path.basename(file)}: an @import follows a rule or declaration`);
  }
  return targets;
}

describe('stylesheet imports', () => {
  test('styles.css and styles/index.css only import before any rule', () => {
    for (const f of [path.join(srcDir, 'styles.css'), path.join(stylesDir, 'index.css')]) {
      expect(() => importsOf(f)).not.toThrow();
      expect(statements(f).every((s) => s.startsWith('@import'))).toBe(true);
    }
  });

  test('every import target exists', () => {
    for (const f of [path.join(srcDir, 'styles.css'), path.join(stylesDir, 'index.css')]) {
      for (const target of importsOf(f)) {
        expect(fs.existsSync(path.resolve(path.dirname(f), target))).toBe(true);
      }
    }
  });

  test('every CSS file under styles/ is imported exactly once', () => {
    const imported = importsOf(path.join(stylesDir, 'index.css')).map((t) => path.basename(t));
    const onDisk = fs.readdirSync(stylesDir).filter((f) => f.endsWith('.css') && f !== 'index.css');
    expect([...imported].sort()).toEqual([...onDisk].sort());
    expect(new Set(imported).size).toBe(imported.length);
  });

  test('styles.css imports styles/index.css and nothing else', () => {
    expect(importsOf(path.join(srcDir, 'styles.css'))).toEqual(['./styles/index.css']);
  });
});
