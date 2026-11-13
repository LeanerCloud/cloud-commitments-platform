/**
 * Issue #1691 items 6 and 7: markup/CSS assertions for the Edit Account
 * modal autofill hints and the group card header wrapping.
 */
import * as fs from 'fs';
import * as path from 'path';

const read = (rel: string): string => fs.readFileSync(path.join(__dirname, '..', rel), 'utf-8');

describe('#1691 item 6: account form autofill hints', () => {
  let doc: Document;
  beforeAll(() => {
    doc = new DOMParser().parseFromString(read('index.html'), 'text/html');
  });

  it('turns autofill off on the form', () => {
    expect(doc.getElementById('account-form')?.getAttribute('autocomplete')).toBe('off');
  });

  it.each(['account-name', 'account-external-id'])('%s opts out of autofill', (id) => {
    expect(doc.getElementById(id)?.getAttribute('autocomplete')).toBe('off');
  });

  it.each(['account-aws-secret-access-key', 'account-azure-client-secret'])(
    '%s uses new-password so no saved login is offered',
    (id) => {
      expect(doc.getElementById(id)?.getAttribute('autocomplete')).toBe('new-password');
    },
  );
});

describe('#1691 item 7: group card header', () => {
  const css = read('styles/settings.css');

  it('lets the text block shrink so the actions stay on the top row', () => {
    expect(css).toMatch(/\.group-card-header\s*>\s*div:first-child\s*\{[^}]*min-width:\s*0/);
    expect(css).toMatch(/\.group-card-header\s*>\s*div:first-child\s*\{[^}]*flex:\s*1 1/);
  });

  it('keeps the actions pinned right', () => {
    expect(css).toMatch(/\.group-card-actions\s*\{[^}]*margin-left:\s*auto/);
  });
});
