/**
 * Issue #1691 item 6: markup/CSS assertions for the Edit Account
 * modal autofill hints.
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
