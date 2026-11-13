/**
 * Tests for chart colors from design tokens (issue #699).
 */
import fs from 'fs';
import path from 'path';
import {
  CHART_TOKEN_FALLBACKS, SURFACE_FALLBACK, applyChartDefaults, chartColor, chartPalette, requireToken,
  surfaceColor, withAlpha, type ChartToken,
} from '../lib/chart-theme';

const SRC = path.join(__dirname, '..');
const STYLES = path.join(SRC, 'styles');
const TOKENS = Object.keys(CHART_TOKEN_FALLBACKS) as ChartToken[];

/** The declarations of the first :root block in base.css. */
function baseRootTokens(): Map<string, string> {
  const css = fs.readFileSync(path.join(STYLES, 'base.css'), 'utf8');
  const root = /:root\s*\{([\s\S]*?)\n\}/.exec(css)![1]!;
  const out = new Map<string, string>();
  for (const m of root.matchAll(/(--cudly-[\w-]+)\s*:\s*([^;]+);/g)) out.set(m[1]!, m[2]!.trim());
  return out;
}

/** Hex / rgb() / hsl() color literals in CSS, ignoring comments, token definitions and var() fallbacks. */
function cssColorLiterals(css: string): string[] {
  const code = css
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .filter((line) => !/^\s*--[\w-]+\s*:/.test(line))
    .join('\n')
    .replace(/var\(--[\w-]+\s*,[^)]*\)/g, '');
  return code.match(/#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(/g) ?? [];
}

afterEach(() => {
  document.documentElement.removeAttribute('style');
});

describe('chart-theme', () => {
  it('fallbacks match the --cudly-chart-* tokens defined in base.css', () => {
    const defined = baseRootTokens();
    for (const name of TOKENS) expect([name, defined.get(name)]).toEqual([name, CHART_TOKEN_FALLBACKS[name]]);
    const cssChartTokens = [...defined.keys()].filter((k) => k.startsWith('--cudly-chart-'));
    expect(cssChartTokens.sort()).toEqual([...TOKENS].sort());
  });

  it('surfaceColor reads --cudly-surface, falling back to its base.css value', () => {
    expect(baseRootTokens().get('--cudly-surface')).toBe(SURFACE_FALLBACK);
    expect(surfaceColor()).toBe(SURFACE_FALLBACK);
    document.documentElement.style.setProperty('--cudly-surface', '#1f2937');
    expect(surfaceColor()).toBe('#1f2937');
  });

  it('series and layer tokens are #rrggbb so charts can derive shades from them', () => {
    for (const name of TOKENS) {
      if (/text|grid/.test(name)) continue;
      expect(CHART_TOKEN_FALLBACKS[name]).toMatch(/^#[0-9a-f]{6}$/);
    }
  });

  it('chartColor prefers the live token over the fallback', () => {
    expect(chartColor('--cudly-chart-1')).toBe('#1a73e8');
    document.documentElement.style.setProperty('--cudly-chart-1', '#8ab4f8');
    expect(chartColor('--cudly-chart-1')).toBe('#8ab4f8');
    expect(chartPalette()[0]).toBe('#8ab4f8');
    expect(chartPalette()).toHaveLength(10);
  });

  it('requireToken throws for a missing token', () => {
    expect(() => requireToken('--cudly-does-not-exist')).toThrow('Missing CSS token --cudly-does-not-exist');
    document.documentElement.style.setProperty('--cudly-primary', '#2563eb');
    expect(requireToken('--cudly-primary')).toBe('#2563eb');
  });

  it('withAlpha turns #rrggbb into rgba()', () => {
    expect(withAlpha('#1a73e8', 0.1)).toBe('rgba(26,115,232,0.1)');
    expect(withAlpha('not-a-color', 0.5)).toBe('rgba(26,115,232,0.5)');
  });

  it('applyChartDefaults points Chart.js text and gridlines at the tokens', () => {
    const defaults = { color: '#666', borderColor: 'rgba(0,0,0,0.1)' };
    document.documentElement.style.setProperty('--cudly-chart-text', '#e5e7eb');
    applyChartDefaults(defaults);
    expect(defaults).toEqual({ color: '#e5e7eb', borderColor: 'rgba(0, 0, 0, 0.1)' });
    expect(() => applyChartDefaults(undefined)).not.toThrow();
  });

  it('the dashboard follows a themed palette', () => {
    const { savingsLayerColors } = require('../dashboard') as {
      savingsLayerColors: () => { current: string; lowestOption: string; upside: string };
    };
    expect(savingsLayerColors().current).toBe('#2a78d6');
    document.documentElement.style.setProperty('--cudly-chart-layer-current', '#8ab4f8');
    expect(savingsLayerColors().current).toBe('#8ab4f8');
  });
});

describe('color literals (theming ratchet)', () => {
  // Chart modules must take every color from lib/chart-theme.ts.
  it.each(['dashboard.ts', 'modules/savings-history.ts', 'ladder-timeline-chart.ts', 'trends.ts'])('%s has no color literals', (file) => {
    const code = fs.readFileSync(path.join(SRC, file), 'utf8')
      .split('\n')
      .filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line))
      .map((line) => line.replace(/\/\/.*$/, ''))
      .join('\n');
    expect(code.match(/'#[0-9a-fA-F]{3,8}'|\brgba?\(\s*\d/g)).toBeNull();
  });

  // Stylesheet literals bypass the tokens, so a theme cannot reach them.
  // The baseline is the count on main when this ratchet landed. This ceiling
  // may only go down: migrate a literal to a token, then lower it. Raising it
  // means a new hard-coded color; use or add a token instead.
  const CSS_LITERAL_CEILING = 63;

  it(`styles/*.css hold at most ${CSS_LITERAL_CEILING} color literals outside token definitions`, () => {
    const counts = fs.readdirSync(STYLES)
      .filter((f) => f.endsWith('.css'))
      .map((f) => [f, cssColorLiterals(fs.readFileSync(path.join(STYLES, f), 'utf8')).length] as const);
    const total = counts.reduce((n, [, c]) => n + c, 0);
    expect(total).toBeLessThanOrEqual(CSS_LITERAL_CEILING);
  });

  // Light mode must not change when a literal becomes a token: each token
  // holds exactly the text of the literal it replaced.
  const MIGRATED_LITERALS: ReadonlyArray<readonly [string, string]> = [
    ['--cudly-success-strong', '#166534'],
    ['--cudly-neutral-hover-bg', '#e8e8e8'],
    ['--cudly-neutral-hover-border', '#bbb'],
    ['--cudly-warn-strong', '#f9a825'],
    ['--cudly-text-strong', '#222'],
    ['--cudly-text-body', '#444'],
    ['--cudly-text-dark', '#333'],
    ['--cudly-accent-border', '#4a90e2'],
    ['--cudly-accent-fg', '#1a56c4'],
    ['--cudly-surface-faint', '#fafafa'],
    ['--cudly-error-strong', '#d32f2f'],
    ['--cudly-accent-border-soft', '#a9c7f5'],
    ['--cudly-surface-cool', '#f8f9fb'],
    ['--cudly-surface-tint', '#f3f6fb'],
    ['--cudly-surface-tint-hover', '#eaf0fb'],
    ['--cudly-surface-tint-faint', '#fafbfe'],
    ['--cudly-surface-tint-strong', '#f0f4fc'],
    ['--cudly-surface-subtle', '#f0f2f5'],
    ['--cudly-brand-aws', '#ff9900'],
    ['--cudly-brand-aws-fg', '#232f3e'],
    ['--cudly-brand-azure', '#0078d4'],
    ['--cudly-brand-gcp', '#1a73e8'],
    ['--cudly-success-accent', '#4caf50'],
    ['--cudly-border-cool', '#dee2e6'],
    ['--cudly-shadow-ink-18', 'rgba(0, 0, 0, 0.18)'],
    ['--cudly-shadow-ink-08', 'rgba(0, 0, 0, 0.08)'],
    ['--cudly-shadow-ink-15', 'rgba(0,0,0,0.15)'],
    ['--cudly-shadow-ink-13', 'rgba(0,0,0,0.13)'],
    ['--cudly-shadow-ink-20', 'rgba(0,0,0,0.2)'],
    ['--cudly-shadow-ink-10', 'rgba(0,0,0,0.1)'],
    ['--cudly-on-primary-18', 'rgba(255,255,255,0.18)'],
    ['--cudly-on-primary-95', 'rgba(255,255,255,0.95)'],
    ['--cudly-on-primary-15', 'rgba(255,255,255,0.15)'],
    ['--cudly-on-primary-50', 'rgba(255,255,255,0.5)'],
    ['--cudly-on-primary-10', 'rgba(255,255,255,0.1)'],
  ];

  it.each(MIGRATED_LITERALS)('%s keeps the old literal %s', (token, literal) => {
    expect(baseRootTokens().get(token)).toBe(literal);
  });
});
