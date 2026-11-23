/**
 * Group edit regression (#1629): values the permission selects cannot represent
 * (unrecognised action/resource pairs and XSS breakout payloads) are flagged,
 * escaped and round-tripped unchanged. Split from
 * group-edit-unrepresentable-permissions.test.ts to stay under the 500-line
 * ceiling; see that file for the full issue description.
 */

// The harness registers the module mocks, so it must be imported first.
import { setUpModalDom, openEditDescriptionOnlyAndSave } from './group-edit-unrepresentable-permissions-harness';
import * as api from '../api';
import * as groupState from '../groups/state';
import * as groupModals from '../groups/groupModals';
import { ALL_ACTIONS, ALL_RESOURCES } from '../permissions';

describe('regression: group edit no longer drops/widens unrepresentable permissions (#1629)', () => {
  beforeEach(() => {
    setUpModalDom();
    groupState.setCurrentEditingGroup(null);
    jest.clearAllMocks();
  });

  test('a genuinely unrecognised action/resource pair round-trips unchanged instead of being dropped or widened', async () => {
    // Simulates a future backend verb the frontend's ALL_ACTIONS/
    // ALL_RESOURCES hasn't been taught yet (or legacy/foreign data) --
    // the defense-in-depth path, independent of whether the known-vocabulary
    // list is currently complete.
    const foreignPermission: api.Permission = { action: 'time-travel', resource: 'flux-capacitor' };
    const group: api.APIGroup = {
      id: 'exotic-group-id',
      name: 'Exotic',
      description: 'Old description',
      permissions: [foreignPermission],
      created_at: '2024-01-01T00:00:00Z',
    };

    await openEditDescriptionOnlyAndSave(group, 'New description');

    expect(api.updateGroup).toHaveBeenCalledWith('exotic-group-id', {
      name: 'Exotic',
      description: 'New description',
      permissions: [foreignPermission],
    });
  });

  test('the unrecognised value is visibly flagged in the select, not silently presented as a normal option', async () => {
    const foreignPermission: api.Permission = { action: 'time-travel', resource: 'flux-capacitor' };
    const group: api.APIGroup = {
      id: 'exotic-group-id',
      name: 'Exotic',
      description: 'Old description',
      permissions: [foreignPermission],
      created_at: '2024-01-01T00:00:00Z',
    };

    (api.getGroup as jest.Mock).mockResolvedValue(group);
    await groupModals.openEditGroupModal(group.id);

    const actionSelect = document.querySelector('.perm-action') as HTMLSelectElement;
    const resourceSelect = document.querySelector('.perm-resource') as HTMLSelectElement;
    expect(actionSelect.value).toBe('time-travel');
    expect(resourceSelect.value).toBe('flux-capacitor');
    // The selected option's own label carries a visible warning, not a
    // plain/normal-looking label, so an admin scanning the closed <select>
    // sees something is off before ever opening the dropdown.
    expect(actionSelect.selectedOptions[0]?.textContent).toContain('not recognized');
    expect(resourceSelect.selectedOptions[0]?.textContent).toContain('not recognized');
  });

  // F2 (adversarial review of this PR): the escaping above is correct today
  // but nothing was pinning it. buildActionOptions/buildResourceOptions in
  // groupModals.ts interpolate the raw stored permission value into both an
  // HTML attribute (`value="..."`) and a text label (`⚠ ... (not
  // recognized...)`) for any value outside ALL_ACTIONS/ALL_RESOURCES --
  // exactly the "API string reaching innerHTML" pattern #1727 fixed twice
  // elsewhere in this codebase a few hours before this file was written.
  // These five breakout payloads (from the reviewer) pin the strong
  // properties rather than just "the string appears somewhere": no element
  // was parsed out of the payload, the selected <option> gained no
  // attributes beyond value/selected, its label has zero child elements,
  // and the value is preserved byte-for-byte through the DOM and through
  // an actual save.
  describe('F2: unrecognised-value option escaping holds under XSS breakout payloads', () => {
    const BREAKOUT_PAYLOADS: Array<[string, string]> = [
      ['element injection', '"><img src=x onerror=alert(1)>'],
      ['attribute injection onto the <option> tag', '" autofocus onfocus=alert(1) x="'],
      ['raw script tag', '<script>alert(1)</script>'],
      ['single-quote context breakout', "'><svg onload=alert(1)>"],
      ['closing-tag option injection', '</option><option value="x" selected>evil</option>'],
    ];

    test.each(BREAKOUT_PAYLOADS)('%s does not inject markup and round-trips unchanged', async (_label, payload) => {
      const group: api.APIGroup = {
        id: 'breakout-group-id',
        name: 'Breakout',
        description: 'Old description',
        permissions: [{ action: payload, resource: payload }],
        created_at: '2024-01-01T00:00:00Z',
      };

      (api.getGroup as jest.Mock).mockResolvedValue(group);
      await groupModals.openEditGroupModal(group.id);

      const actionSelect = document.querySelector('.perm-action') as HTMLSelectElement;
      const resourceSelect = document.querySelector('.perm-resource') as HTMLSelectElement;

      // No breakout payload parsed as a live element anywhere in the modal.
      // svg.icon is the Remove button's own trash icon (src/icons.ts), not injected markup.
      expect(document.querySelectorAll('img, script, svg:not(.icon), iframe, style').length).toBe(0);

      // No extra <option> was smuggled in via the closing-tag breakout: the
      // action select is ALL_ACTIONS + the empty placeholder + the one
      // fallback option for this unrecognised value; the resource select is
      // ALL_RESOURCES + the one fallback (it has no placeholder).
      expect(actionSelect.options.length).toBe(ALL_ACTIONS.length + 1 + 1);
      expect(resourceSelect.options.length).toBe(ALL_RESOURCES.length + 1);

      const selectedAction = actionSelect.selectedOptions[0];
      const selectedResource = resourceSelect.selectedOptions[0];
      expect(selectedAction).toBeDefined();
      expect(selectedResource).toBeDefined();

      // The attribute-injection payload gained no new attributes on the
      // <option> element itself: exactly value + selected, nothing else
      // (no autofocus/onfocus/etc smuggled in).
      expect(Array.from(selectedAction!.attributes).map(a => a.name).sort()).toEqual(['selected', 'value']);
      expect(Array.from(selectedResource!.attributes).map(a => a.name).sort()).toEqual(['selected', 'value']);

      // The label rendered as text, not markup: zero child elements.
      expect(selectedAction!.children.length).toBe(0);
      expect(selectedResource!.children.length).toBe(0);

      // The value round-trips byte-identically through the DOM.
      expect(actionSelect.value).toBe(payload);
      expect(resourceSelect.value).toBe(payload);

      // ...and through an actual save: the exact payload reaches the API
      // call, neither dropped nor mangled by an unrelated description edit.
      (document.getElementById('group-description') as HTMLTextAreaElement).value = 'New description';
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await groupModals.saveGroup(event);

      expect(api.updateGroup).toHaveBeenCalledWith('breakout-group-id', {
        name: 'Breakout',
        description: 'New description',
        permissions: [{ action: payload, resource: payload }],
      });
    });
  });
});
