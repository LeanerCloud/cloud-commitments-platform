/**
 * Dark palette tokens (issue #699): key coverage, WCAG contrast, inertness.
 */
import fs from 'fs';
import path from 'path';

const STYLES = path.join(__dirname, '..', 'styles');
const baseCss = fs.readFileSync(path.join(STYLES, 'base.css'), 'utf8');

/** Colour tokens that keep their :root value in the dark scheme on purpose. */
const UNTHEMED = new Set([
  '--cudly-brand-aws', '--cudly-brand-aws-fg', '--cudly-brand-azure', '--cudly-brand-gcp', // brand marks
  '--cudly-mask-opaque', '--cudly-scroll-hint-ink', // alpha-only masks
  '--cudly-shadow-ink-08', '--cudly-shadow-ink-10', '--cudly-shadow-ink-13', '--cudly-shadow-ink-15',
  '--cudly-shadow-ink-18', '--cudly-shadow-ink-20', '--cudly-shadow-ink-40', '--cudly-shadow-scroll',
  '--cudly-scrim', '--cudly-shadow-dialog', // black-ish inks work on both schemes
  '--cudly-on-primary-10', '--cudly-on-primary-15', '--cudly-on-primary-18', '--cudly-on-primary-50',
  '--cudly-on-primary-80', '--cudly-on-primary-95', // white alphas stay white
]);

function declarations(body: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of body.matchAll(/(--cudly-[\w-]+)\s*:\s*([^;]+);/g)) out.set(m[1]!, m[2]!.trim());
  return out;
}

const rootBody = /^:root\s*\{([\s\S]*?)\n\}/m.exec(baseCss)![1]!;
const darkBody = /^:root\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/m.exec(baseCss)![1]!;
const light = declarations(rootBody);
const dark = declarations(darkBody);
const isColour = (v: string): boolean => /#[0-9a-f]{3,8}\b|\brgba?\(/i.test(v);

function luminance(hex: string): number {
  const h = hex.length === 4 ? [...hex.slice(1)].map((c) => c + c).join('') : hex.slice(1);
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

const PAIRS: ReadonlyArray<readonly [string, string]> = [
  ['--cudly-text', '--cudly-surface'], ['--cudly-text', '--cudly-bg'],
  ['--cudly-text-muted', '--cudly-surface'], ['--cudly-text-subtle', '--cudly-surface'],
  ['--cudly-primary-fg', '--cudly-primary'], ['--cudly-primary', '--cudly-surface'],
  ['--cudly-error-fg', '--cudly-error-bg'], ['--cudly-success-fg', '--cudly-success-bg'],
  ['--cudly-warn-fg', '--cudly-warn-bg'],
];

describe('dark palette tokens', () => {
  it('overrides every colour token except the documented unthemed ones', () => {
    const colourTokens = [...light].filter(([, v]) => isColour(v)).map(([k]) => k);
    const missing = colourTokens.filter((k) => !dark.has(k) && !UNTHEMED.has(k));
    expect(missing).toEqual([]);
  });

  it('defines no token that :root lacks, and none of the unthemed ones', () => {
    expect([...dark.keys()].filter((k) => !light.has(k))).toEqual([]);
    expect([...dark.keys()].filter((k) => UNTHEMED.has(k))).toEqual([]);
    expect([...UNTHEMED].filter((k) => !light.has(k))).toEqual([]);
  });

  it('never repeats the light value (a no-op override hides a missing design choice)', () => {
    expect([...dark].filter(([k, v]) => light.get(k) === v).map(([k]) => k)).toEqual([]);
  });

  it.each(PAIRS)('%s on %s reaches WCAG AA 4.5:1 in both schemes', (fg, bg) => {
    for (const [name, tokens] of [['light', light], ['dark', new Map([...light, ...dark])]] as const) {
      const ratio = contrast(tokens.get(fg)!, tokens.get(bg)!);
      expect([name, fg, bg, ratio >= 4.5]).toEqual([name, fg, bg, true]);
    }
  });

  it('declares color-scheme for both schemes', () => {
    expect(rootBody).toMatch(/color-scheme:\s*light;/);
    expect(darkBody).toMatch(/color-scheme:\s*dark;/);
  });

  it('is inert without the data-theme attribute', () => {
    // Every rule that sets a themed token must be the base :root or require data-theme="dark".
    const sheets = fs.readdirSync(STYLES).filter((f) => f.endsWith('.css'))
      .map((f) => fs.readFileSync(path.join(STYLES, f), 'utf8')).join('\n');
    expect(sheets).not.toMatch(/prefers-color-scheme/);
    const themed = [...dark.keys()];
    const rules = [...sheets.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)];
    const offenders = rules
      .filter(([, , body]) => themed.some((k) => new RegExp(`(^|[\\s;])${k}\\s*:`).test(body!)))
      .map(([, sel]) => sel!.trim())
      .filter((sel) => sel !== ':root' && !/^:root\[data-theme="dark"\]$/.test(sel));
    expect(offenders).toEqual([]);
  });
});
