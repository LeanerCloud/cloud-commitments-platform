/**
 * CSS validation tests
 * These tests verify that our CSS styling works correctly
 */
import fs from 'fs';
import path from 'path';

describe('CSS Styles', () => {
  let css: string;
  let responsiveCss: string;
  let componentsCss: string;

  beforeAll(() => {
    // Read all CSS files from the styles directory
    const stylesDir = path.join(__dirname, '..', 'styles');
    const cssFiles = fs.readdirSync(stylesDir)
      .filter(file => file.endsWith('.css'))
      .map(file => fs.readFileSync(path.join(stylesDir, file), 'utf8'));
    css = cssFiles.join('\n');
    responsiveCss = fs.readFileSync(path.join(stylesDir, 'responsive.css'), 'utf8');
    componentsCss = fs.readFileSync(path.join(stylesDir, 'components.css'), 'utf8');
  });

  describe('Required CSS Rules', () => {
    test('has body styles', () => {
      expect(css).toMatch(/body\s*\{/);
    });

    test('has header styles', () => {
      expect(css).toMatch(/header\s*\{/);
    });

    test('has card styles', () => {
      expect(css).toMatch(/\.card\s*\{/);
    });

    test('has button styles', () => {
      expect(css).toMatch(/button\s*\{/);
    });

    test('has primary button variant', () => {
      expect(css).toMatch(/button\.primary\s*\{/);
    });

    test('has danger button variant', () => {
      expect(css).toMatch(/button\.danger\s*\{/);
    });

    test('has modal styles', () => {
      expect(css).toMatch(/\.modal\s*\{/);
    });

    test('has hidden class', () => {
      expect(css).toMatch(/\.hidden\s*\{/);
    });

    test('has tab styles', () => {
      // The tab buttons are the sidebar links (layout.css).
      expect(css).toMatch(/\.app-sidebar-nav \.tab-btn\s*\{/);
      expect(css).toMatch(/\.tab-content\s*\{/);
    });

    test('has form styles', () => {
      expect(css).toMatch(/\.form-section\s*\{/);
    });

    test('has table styles', () => {
      expect(css).toMatch(/table\s*\{/);
      expect(css).toMatch(/th,\s*td\s*\{/);
    });
  });

  describe('CSS Classes', () => {
    test('has savings class with success color token', () => {
      // After issue #340 the literal #34a853 was replaced with the
      // --cudly-success token; assert the .savings rule still binds
      // to a green-family colour via either the token or its literal.
      expect(css).toMatch(/\.savings\s*\{[^}]*color:\s*(var\(--cudly-success\)|#34a853)/);
    });

    test('has error class with error color token', () => {
      // Post-#340 the literal #ea4335 was replaced with the
      // --cudly-error token; #191 moved the text to the darker -fg shade
      // (AA on --cudly-error-bg). Assert any of those forms.
      expect(css).toMatch(/\.error\s*\{[^}]*color:\s*(var\(--cudly-error(-fg)?\)|#ea4335)/);
    });

    test('has provider badge classes', () => {
      expect(css).toMatch(/\.provider-badge\.aws/);
      expect(css).toMatch(/\.provider-badge\.azure/);
      expect(css).toMatch(/\.provider-badge\.gcp/);
    });

    test('has one badge component with a tone per status family (#737)', () => {
      for (const tone of ['success', 'warning', 'danger', 'info', 'muted']) {
        expect(componentsCss).toMatch(new RegExp(`\\.badge-${tone}\\s*\\{[^}]*background:\\s*var\\(--cudly-`));
      }
      // The retired per-page variants must not come back.
      expect(css).not.toMatch(/\.status-badge\b/);
      expect(css).not.toMatch(/\.badge-(admin|user|readonly)\b/);
    });

    test('has toggle switch styles', () => {
      expect(css).toMatch(/\.toggle-label/);
      expect(css).toMatch(/\.slider/);
    });
  });

  describe('Responsive Design', () => {
    test('has responsive breakpoints', () => {
      expect(css).toMatch(/@media\s*\(max-width:\s*768px\)/);
    });

    test('has responsive styles block', () => {
      // Check that the media query contains responsive styles
      expect(css).toMatch(/@media\s*\(max-width:\s*768px\)\s*\{[\s\S]*header\s*\{/);
    });

    test('has responsive table styles', () => {
      // Check that table is styled within the media query
      expect(css).toMatch(/@media\s*\(max-width:\s*768px\)\s*\{[\s\S]*table\s*\{/);
    });
  });

  describe('Layout', () => {
    test('has grid layout for cards', () => {
      expect(css).toMatch(/display:\s*grid/);
    });

    test('has flexbox for headers/controls', () => {
      expect(css).toMatch(/display:\s*flex/);
    });

    test('has box-sizing border-box', () => {
      expect(css).toMatch(/box-sizing:\s*border-box/);
    });
  });

  describe('Color Scheme', () => {
    // After the issue #340 design-token migration, the canonical colours
    // live in :root as `--cudly-*` custom properties. The literals that
    // used to be sprinkled across components.css / settings.css / etc.
    // were replaced with `var(--cudly-*)` references. These tests now
    // assert the token DEFINITIONS exist in base.css's :root block,
    // which is the source of truth.
    test('defines primary token', () => {
      expect(css).toMatch(/--cudly-primary:\s*#/);
    });

    test('defines success token', () => {
      expect(css).toMatch(/--cudly-success:\s*#/);
    });

    test('defines warn token', () => {
      expect(css).toMatch(/--cudly-warn:\s*#/);
    });

    test('defines error token', () => {
      expect(css).toMatch(/--cudly-error:\s*#/);
    });
  });

  describe('Interactive Elements', () => {
    test('has hover states for buttons', () => {
      expect(css).toMatch(/button\)?:hover/);
    });

    test('has hover states for table rows', () => {
      expect(css).toMatch(/tr:hover/);
    });

    test('has transition animations', () => {
      expect(css).toMatch(/transition:/);
    });
  });

  describe('Button Variants', () => {
    test('has success button variant', () => {
      expect(css).toMatch(/button\.success\s*\{/);
    });

    test('has small button variant', () => {
      expect(css).toMatch(/\.btn-small\s*\{/);
    });
  });

  describe('State Classes', () => {
    test('has loading state styles', () => {
      expect(css).toMatch(/\.loading\s*\{/);
    });

    test('has empty state styles', () => {
      expect(css).toMatch(/\.empty\s*\{/);
    });

    test('has error-message styles', () => {
      expect(css).toMatch(/\.error-message\s*\{/);
    });

    test('has success-message styles', () => {
      expect(css).toMatch(/\.success-message\s*\{/);
    });

    test('has help-text styles', () => {
      expect(css).toMatch(/\.help-text\s*\{/);
    });
  });

  describe('Layout Components', () => {
    test('has controls-bar styles', () => {
      expect(css).toMatch(/\.controls-bar\s*\{/);
    });

    test('has filter-group styles', () => {
      expect(css).toMatch(/\.filter-group\s*\{/);
    });

    test('has action-group styles', () => {
      expect(css).toMatch(/\.action-group\s*\{/);
    });

    test('has date-range-picker styles', () => {
      expect(css).toMatch(/\.date-range-picker\s*\{/);
    });
  });

  describe('Form Components', () => {
    test('has form-row styles', () => {
      expect(css).toMatch(/\.form-row\s*\{/);
    });

    test('has input styles', () => {
      expect(css).toMatch(/input\[type/);
    });

    test('has select styles', () => {
      expect(css).toMatch(/select\s*\{/);
    });

    test('has label styles', () => {
      expect(css).toMatch(/label\s*\{/);
    });

    test('has textarea styles in modal', () => {
      expect(css).toMatch(/\.modal-content textarea\s*\{/);
    });
  });

  describe('Settings Components', () => {
    test('has settings-form styles', () => {
      expect(css).toMatch(/\.settings-form\s*\{/);
    });

    test('has settings-category styles', () => {
      expect(css).toMatch(/\.settings-category\s*\{/);
    });

    test('has setting-row styles', () => {
      expect(css).toMatch(/\.setting-row\s*\{/);
    });

    test('has credential-status styles', () => {
      expect(css).toMatch(/\.credential-status/);
    });

    test('has service-defaults-grid styles', () => {
      expect(css).toMatch(/\.service-defaults-grid\s*\{/);
    });

    test('has service-default-card styles', () => {
      expect(css).toMatch(/\.service-default-card\s*\{/);
    });
  });

  describe('Plan Components', () => {
    test('has plan-card styles', () => {
      expect(css).toMatch(/\.plan-card\s*\{/);
    });

    test('has plan-header styles', () => {
      expect(css).toMatch(/\.plan-header\s*\{/);
    });

    test('has plan-body styles', () => {
      expect(css).toMatch(/\.plan-body\s*\{/);
    });

    test('has plan-details styles', () => {
      expect(css).toMatch(/\.plan-details\s*\{/);
    });

    test('has ramp-option styles', () => {
      expect(css).toMatch(/\.ramp-option\s*\{/);
    });
  });

  describe('Modal Components', () => {
    test('has modal-overlay styles', () => {
      expect(css).toMatch(/\.modal-overlay\s*\{/);
    });

    test('has modal-content styles', () => {
      expect(css).toMatch(/\.modal-content\s*\{/);
    });

    test('has modal-buttons styles', () => {
      expect(css).toMatch(/\.modal-buttons\s*\{/);
    });

    test('has modal-wide variant', () => {
      expect(css).toMatch(/\.modal-wide\s*\{/);
    });

    test('has a vw cap on modal-confirm', () => {
      expect(css).toMatch(/\.modal-confirm\s*\{[^}]*max-width:\s*min\(480px,\s*calc\(100vw - 2rem\)\)/);
    });

    test('has a 44px minimum tap target on modal-confirm-close', () => {
      expect(css).toMatch(/\.modal-confirm-close\s*\{[^}]*min-width:\s*44px[^}]*min-height:\s*44px/);
    });

    test('flips modals to a bottom sheet at 480px, winning the cascade', () => {
      // The bottom-sheet rule must live in responsive.css (imported last in
      // index.css), not components.css (imported 3rd). components.css is
      // imported before modals.css and responsive.css, so an equal-specificity
      // .modal-content rule placed there would always lose to the
      // .modal-content{width:95%} 768px rule in responsive.css -- only
      // .modal-confirm would actually flip, leaving every real modal
      // (#user-modal, #purchase-modal, etc.) centered instead of bottom-sheet.
      const bottomSheetQuery = /@media \(max-width: 480px\)\s*\{[^]*?\.modal,\s*\.modal-confirm-backdrop\s*\{[^}]*align-items:\s*flex-end[^}]*\}[^]*?\.modal-content,\s*\.modal-confirm\s*\{[^}]*width:\s*100%[^}]*\}/;

      expect(responsiveCss).toMatch(bottomSheetQuery);
      expect(componentsCss).not.toMatch(bottomSheetQuery);

      // Source-order check: the 480px bottom-sheet block must appear after
      // the existing 768px block within responsive.css, so at <=480px it
      // wins the cascade over the 768px .modal-content{width:95%} rule
      // (equal selector specificity -> later declaration wins).
      const idx768 = responsiveCss.search(/@media \(max-width: 768px\)/);
      const idx480 = responsiveCss.search(/@media \(max-width: 480px\)/);
      expect(idx768).toBeGreaterThan(-1);
      expect(idx480).toBeGreaterThan(idx768);
    });
  });

  describe('CLI Command Styles', () => {
    test('has cli-command styles', () => {
      expect(css).toMatch(/\.cli-command\s*\{/);
    });

    test('has copy-btn styles', () => {
      expect(css).toMatch(/\.copy-btn\s*\{/);
    });
  });

  describe('Tooltip Styles', () => {
    test('has info-icon styles', () => {
      expect(css).toMatch(/\.info-icon\s*\{/);
    });

    test('has tooltip-text styles', () => {
      expect(css).toMatch(/\.tooltip-text\s*\{/);
    });
  });
});
