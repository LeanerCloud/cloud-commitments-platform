/**
 * CSS organization and hygiene tests (split from css.test.ts, issue #1965).
 */
import fs from 'fs';
import path from 'path';

describe('CSS File Organization', () => {
  const stylesDir = path.join(__dirname, '..', 'styles');

  function readCssFile(filename: string): string {
    return fs.readFileSync(path.join(stylesDir, filename), 'utf8');
  }

  test('base.css contains reset and typography', () => {
    const base = readCssFile('base.css') + readCssFile('base-utilities.css');
    expect(base).toMatch(/\*\s*\{[^}]*box-sizing:\s*border-box/);
    expect(base).toMatch(/body\s*\{/);
    expect(base).toMatch(/\.hidden\s*\{/);
    expect(base).toMatch(/\.loading\s*\{/);
    expect(base).toMatch(/\.error\s*\{/);
  });

  test('layout.css contains header and main styles', () => {
    const layout = readCssFile('layout.css');
    expect(layout).toMatch(/header\s*\{/);
    expect(layout).toMatch(/main\s*\{/);
  });

  test('components.css contains cards and buttons', () => {
    const components = readCssFile('components.css');
    expect(components).toMatch(/\.card\s*\{/);
    expect(components).toMatch(/button\s*\{/);
    expect(components).toMatch(/button\.primary\s*\{/);
    expect(components).toMatch(/\.provider-badge/);
    expect(components).toMatch(/\.badge\s*\{/);
  });

  test('forms.css contains form elements', () => {
    const forms = readCssFile('forms.css');
    expect(forms).toMatch(/input/);
    expect(forms).toMatch(/select\s*\{/);
    expect(forms).toMatch(/label\s*\{/);
    expect(forms).toMatch(/\.toggle-label/);
    expect(forms).toMatch(/\.slider\s*\{/);
  });

  test('tables.css contains table styles', () => {
    const tables = readCssFile('tables.css');
    expect(tables).toMatch(/table\s*\{/);
    expect(tables).toMatch(/th,?\s*td\s*\{/);
    expect(tables).toMatch(/tr:hover/);
  });

  test('modals.css contains modal styles', () => {
    const modals = readCssFile('modals.css');
    expect(modals).toMatch(/\.modal\s*\{/);
    expect(modals).toMatch(/\.modal-content\s*\{/);
    expect(modals).toMatch(/\.modal-overlay\s*\{/);
    expect(modals).toMatch(/\.modal-buttons\s*\{/);
  });

  test('tabs.css contains tab navigation styles', () => {
    const tabs = readCssFile('tabs.css');
    expect(tabs).toMatch(/\.tab-content\s*\{/);
    // Issue #725: the dead top-bar rules must not return; .tab-btn.active
    // leaked font-weight 600 onto the selected sidebar item.
    expect(tabs).not.toMatch(/\.tabs\s*\{/);
    expect(tabs).not.toMatch(/(^|\n)\.tab-btn[^\n]*\{/);
  });

  test('plans.css contains plan-specific styles', () => {
    const plans = readCssFile('plans.css');
    expect(plans).toMatch(/\.plan-card\s*\{/);
    expect(plans).toMatch(/\.ramp-option/);
  });

  test('settings.css contains settings form styles', () => {
    const settings = readCssFile('settings.css');
    expect(settings).toMatch(/\.settings-form\s*\{/);
    expect(settings).toMatch(/\.settings-category\s*\{/);
    expect(settings).toMatch(/\.setting-row\s*\{/);
    expect(settings).toMatch(/\.credential-status/);
  });

  test('responsive.css contains media queries', () => {
    const responsive = readCssFile('responsive.css');
    expect(responsive).toMatch(/@media\s*\(max-width:\s*768px\)/);
  });

  test('index.css imports all other files', () => {
    const index = readCssFile('index.css');
    expect(index).toMatch(/@import\s+['"]\.\/base\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/layout\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/components\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/forms\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/tables\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/modals\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/tabs\.css['"]/);
    expect(index).toMatch(/@import\s+['"]\.\/responsive\.css['"]/);
  });
});

describe('Touch Target Minimums (issue #983 / PR #989)', () => {
  let css: string;

  beforeAll(() => {
    const stylesDir = path.join(__dirname, '..', 'styles');
    const cssFiles = fs.readdirSync(stylesDir)
      .filter(file => file.endsWith('.css'))
      .map(file => fs.readFileSync(path.join(stylesDir, file), 'utf8'));
    css = cssFiles.join('\n');
  });

  test('coarse-pointer media query sets a 44x44 minimum on generic button selectors', () => {
    // PR #989 added this rule so touch devices get HIG/WCAG-compliant tap
    // targets. .btn-small and .toggle-password render as plain <button>
    // elements (recommendations.ts, auth.ts), so they inherit the minimum
    // from `button` here rather than needing a class-specific rule
    // (verified: neither declares its own min-height/min-width, see below).
    // .modal-confirm-close is deliberately excluded from that guarantee:
    // PR #985 (bottom-sheet modal, issue #985) gives it an explicit
    // min-width/min-height: 44px of its own (needed because it is
    // absolutely positioned, not sized by content), which is a stronger
    // guarantee than the generic coarse-pointer rule, not a regression.
    const media = css.match(/@media\s*\(pointer:\s*coarse\)\s*\{([\s\S]*?)\n\}/);
    expect(media).not.toBeNull();
    const block = media?.[1] ?? '';
    expect(block).toMatch(/\bbutton\b/);
    expect(block).toMatch(/\[role="button"\]/);
    expect(block).toMatch(/min-height:\s*44px/);
    expect(block).toMatch(/min-width:\s*44px/);
  });

  test.each(['.btn-small', '.toggle-password'])(
    '%s does not declare its own min-height/height (would override the coarse-pointer 44px minimum by specificity)',
    (selector) => {
      const escaped = selector.replace('.', '\\.');
      const rule = css.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
      expect(rule).not.toBeNull();
      const declarations = rule?.[1] ?? '';
      const sizingProps = declarations
        .split(';')
        .map(decl => decl.split(':')[0]?.trim())
        .filter(prop => /^(min-|max-)?(height|width)$/.test(prop ?? ''));
      expect(sizingProps).toEqual([]);
    }
  );
});

describe('CSS Applied Correctly', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <style>
        .hidden { display: none; }
        .card { background: white; padding: 1rem; }
        .savings { color: #34a853; }
        .error { color: #ea4335; }
        .tab-content { display: none; }
        .tab-content.active { display: block; }
      </style>
      <div class="card">Test Card</div>
      <div class="hidden">Hidden Element</div>
      <div class="savings">$100</div>
      <div class="error">Error message</div>
      <div class="tab-content">Tab 1</div>
      <div class="tab-content active">Tab 2</div>
    `;
  });

  test('hidden class hides elements', () => {
    const hidden = document.querySelector('.hidden') as HTMLElement;
    const computedStyle = window.getComputedStyle(hidden);
    expect(computedStyle.display).toBe('none');
  });

  test('tab-content is hidden by default', () => {
    const tab = document.querySelector('.tab-content:not(.active)') as HTMLElement;
    const computedStyle = window.getComputedStyle(tab);
    expect(computedStyle.display).toBe('none');
  });

  test('active tab is visible', () => {
    const activeTab = document.querySelector('.tab-content.active') as HTMLElement;
    const computedStyle = window.getComputedStyle(activeTab);
    expect(computedStyle.display).toBe('block');
  });
});

// Issue #725: table and CSS hygiene. Source-text checks, since JSDOM does not
// lay out tables or resolve the cascade across the imported files.
describe('CSS hygiene (issue #725)', () => {
  const stylesDir = path.join(__dirname, '..', 'styles');
  const files = fs.readdirSync(stylesDir).filter(f => f.endsWith('.css'));
  const read = (f: string) => fs.readFileSync(path.join(stylesDir, f), 'utf8');
  const all = files.map(read).join('\n');
  const rule = (css: string, selector: RegExp) => css.match(new RegExp(`(^|\\n)${selector.source}\\s*\\{([^}]*)\\}`))?.[2] ?? '';

  test('tables use the separate border model so the radius clips the header corners', () => {
    const table = rule(read('tables.css'), /table/);
    expect(table).toMatch(/border-collapse:\s*separate/);
    expect(table).toMatch(/border-spacing:\s*0/);
    // clip, not hidden: hidden would turn the table into a scroll container
    // and break the sticky headers inside scrolling modals.
    expect(table).toMatch(/overflow:\s*clip/);
  });

  // One header case everywhere: the label as written (Title Case). The
  // uppercase transform only reached headers without a sort button.
  test('table headers are small, semibold, muted and not uppercased', () => {
    const th = rule(read('tables.css'), /th/);
    expect(th).toMatch(/font-size:\s*var\(--cudly-fs-xs\)/);
    expect(th).toMatch(/font-weight:\s*var\(--cudly-fw-semibold\)/);
    expect(th).not.toMatch(/text-transform:\s*uppercase/);
    expect(th).toMatch(/color:\s*var\(--cudly-text-muted\)/);
  });

  test('savings row stripes survive the separate border model', () => {
    const tables = read('tables.css');
    expect(tables).not.toMatch(/tr\.(high|medium)-savings\s*\{[^}]*border-left/);
    expect(tables).toMatch(/tr\.high-savings > :first-child\s*\{[^}]*inset 3px 0 0/);
  });

  test('the ramp option block exists once', () => {
    expect(all.match(/(^|\n)\.ramp-options\s*\{/g)).toHaveLength(1);
    expect(all.match(/(^|\n)#custom-ramp-config\s*\{/g)).toHaveLength(1);
  });

  test('no transition animates `all` or omits its property', () => {
    const decls = all.match(/(^|\n)\s*transition:[^;]+;/g) ?? [];
    expect(decls.length).toBeGreaterThan(0);
    for (const d of decls) {
      expect(d).not.toMatch(/transition:\s*all\b/);
      expect(d).not.toMatch(/transition:\s*[\d.]+m?s/);
    }
  });

  test('transitions use the shared motion tokens', () => {
    expect(read('base.css')).toMatch(/--cudly-ease:/);
    const decls = all.match(/(^|\n)\s*transition:[^;]+;/g) ?? [];
    for (const d of decls) {
      expect(d).toMatch(/var\(--cudly-ease\)/);
      expect(d).not.toMatch(/\d+(\.\d+)?m?s\b/);
    }
  });
});

describe('Toast stack at narrow widths (issue #1736)', () => {
  const stylesDir = path.join(__dirname, '..', 'styles');
  const responsive = fs.readFileSync(path.join(stylesDir, 'responsive.css'), 'utf8');
  const components = fs.readFileSync(path.join(stylesDir, 'components.css'), 'utf8');

  test('<=480px spans the viewport with a gutter and clears the safe area', () => {
    const m = responsive.match(/@media \(max-width: 480px\) \{\s*\.toast-container \{([^}]*)\}/);
    expect(m).not.toBeNull();
    const body = m![1];
    expect(body).toMatch(/left:\s*var\(--cudly-sp-3\)/);
    expect(body).toMatch(/right:\s*var\(--cudly-sp-3\)/);
    expect(body).toMatch(/max-width:\s*none/);
    expect(body).toMatch(/bottom:\s*calc\(var\(--toast-bottom\)\s*\+\s*env\(safe-area-inset-bottom\)\)/);
    expect(body).not.toMatch(/#[0-9a-fA-F]{3,6}\b|rgba?\(/);
  });

  test('<=480px caps the stack below the top bar and scrolls (issue #1945)', () => {
    const m = responsive.match(/@media \(max-width: 480px\) \{\s*\.toast-container \{([^}]*)\}/);
    expect(m).not.toBeNull();
    const body = m![1];
    expect(body).toMatch(/max-height:\s*max\(6rem,\s*calc\(100vh\s*-\s*var\(--cudly-topbar-h\)/);
    expect(body).toMatch(/max-height:\s*max\(6rem,\s*calc\(100dvh\s*-\s*var\(--cudly-topbar-h\)/);
    expect(body).toMatch(/overflow-y:\s*auto/);
  });

  test('<=480px keeps one toast visible and on screen when toast.ts lifts the stack (issue #1945)', () => {
    const m = responsive.match(/@media \(max-width: 480px\) \{\s*\.toast-container \{([^}]*)\}/);
    const body = m![1];
    // The cap has a floor taller than one single-line toast (~63px = 3.9rem).
    expect(body).toMatch(/max-height:\s*max\(6rem,/);
    // The lifted bottom offset is clamped so floor-height stack top stays >= 0.
    expect(body).toMatch(/bottom:\s*min\(calc\(var\(--toast-bottom\)[^;]*\),\s*calc\(100dvh\s*-\s*6rem\)\)/);
  });

  test('desktop rule keeps the bottom-right 400px stack', () => {
    const m = components.match(/\.toast-container \{([^}]*)\}/);
    expect(m).not.toBeNull();
    expect(m![1]).toMatch(/--toast-max-width:\s*400px/);
    expect(m![1]).toMatch(/bottom:\s*var\(--toast-bottom\)/);
    expect(m![1]).toMatch(/right:\s*1rem/);
    expect(m![1]).not.toMatch(/\bleft:/);
  });
});

describe('Review Purchase totals band on phones (issue #1654)', () => {
  const stylesDir = path.join(__dirname, '..', 'styles');
  const modals = fs.readFileSync(path.join(stylesDir, 'modals.css'), 'utf8');

  test('the band stays pinned to the scroller left edge', () => {
    const m = modals.match(/#purchase-modal-commitment-band \{([^}]*)\}/);
    expect(m).not.toBeNull();
    expect(m![1]).toMatch(/position:\s*sticky/);
    expect(m![1]).toMatch(/left:\s*0/);
  });

  test('each tile may shrink below its content width', () => {
    const components = fs.readFileSync(path.join(stylesDir, 'components-chip-select.css'), 'utf8');
    const m = components.match(/\.commitment-summary-stat \{([^}]*)\}/);
    expect(m).not.toBeNull();
    expect(m![1]).toMatch(/min-width:\s*0/);
  });

  test('<=480px stacks the totals tiles into one column', () => {
    const responsive = fs.readFileSync(path.join(stylesDir, 'responsive.css'), 'utf8');
    const block = responsive.match(/@media \(max-width: 480px\) \{\s*\.modal, \.modal-confirm-backdrop \{[\s\S]*?\n\}\n/);
    expect(block).not.toBeNull();
    const band = block![0].match(/\.commitment-summary-band \{([^}]*)\}/);
    expect(band).not.toBeNull();
    expect(band![1]).toMatch(/grid-template-columns:\s*minmax\(0,\s*1fr\)/);
  });
});
