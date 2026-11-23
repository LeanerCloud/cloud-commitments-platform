/**
 * Permissions helper tests, part 2: isPurchaser() and isRIExchanger(). Part 1 is
 * permissions.test.ts; the ../state mock and user builders live in
 * permissions-harness.ts.
 */

// The harness registers the ../state mock, so it must be imported first.
import {
  ADMIN_GID,
  mockUserWithGroups,
  mockNoUser,
} from './permissions-harness';
import {
  isPurchaser,
  isRIExchanger,
  PURCHASER_GROUP_ID,
  RI_EXCHANGER_GROUP_ID,
} from '../permissions';

describe('permissions', () => {
  describe('isPurchaser', () => {
    test('seeded Purchaser group member (no effectivePermissions yet) returns true via fallback', () => {
      // Pre-bootstrap loading window: effectivePermissions not yet
      // populated. The helper falls back to seeded group membership.
      mockUserWithGroups([PURCHASER_GROUP_ID]);
      expect(isPurchaser()).toBe(true);
    });

    test('user without Purchaser group and no effectivePermissions returns false', () => {
      mockUserWithGroups([ADMIN_GID]); // admin only
      expect(isPurchaser()).toBe(false);
    });

    test('explicit execute:purchases grant in effectivePermissions (custom group) returns true', () => {
      // CR #924 F4: isPurchaser() must reflect effective permissions,
      // not just seeded group ID. A user whose custom group grants
      // execute:purchases is a spender even without PURCHASER_GROUP_ID.
      const customGid = '00000000-0000-5000-8000-00000000beef';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: 'purchases' },
        { action: 'view', resource: 'recommendations' },
      ]);
      expect(isPurchaser()).toBe(true);
    });

    test('explicit approve-any:purchases grant returns true', () => {
      const customGid = '00000000-0000-5000-8000-00000000cafe';
      mockUserWithGroups([customGid], [
        { action: 'approve-any', resource: 'purchases' },
      ]);
      expect(isPurchaser()).toBe(true);
    });

    test('explicit retry-any:purchases grant returns true', () => {
      const customGid = '00000000-0000-5000-8000-00000000face';
      mockUserWithGroups([customGid], [
        { action: 'retry-any', resource: 'purchases' },
      ]);
      expect(isPurchaser()).toBe(true);
    });

    test('wildcard resource on a carved-out action grants Purchaser (matches canAccess semantics)', () => {
      // canAccess('execute', 'purchases') returns true for a
      // {execute, *} entry. isPurchaser() must agree so the two helpers
      // do not diverge when an unusual-but-legal permission shape
      // arrives from the backend.
      const customGid = '00000000-0000-5000-8000-00000000d00d';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: '*' },
      ]);
      expect(isPurchaser()).toBe(true);
    });

    test('admin:* in effectivePermissions WITHOUT explicit carved-out grants returns false', () => {
      // The backend carves the three spending verbs OUT of admin:*.
      // isPurchaser() must reflect that carve-out: a user whose
      // effectivePermissions are admin:* only (no explicit carve-out
      // entries from Purchaser membership) is NOT a spender. Note that
      // {admin, *} does NOT match {execute, *} / {approve-any, *} /
      // {retry-any, *} because the actions differ.
      mockUserWithGroups([ADMIN_GID], [
        { action: 'admin', resource: '*' },
      ]);
      expect(isPurchaser()).toBe(false);
    });

    test('empty effectivePermissions array returns false even with PURCHASER_GROUP_ID', () => {
      // Loading is complete (effectivePermissions is defined and
      // empty); group membership without backend confirmation is not
      // enough.
      mockUserWithGroups([PURCHASER_GROUP_ID], []);
      expect(isPurchaser()).toBe(false);
    });

    test('null user (logged out) returns false', () => {
      mockNoUser();
      expect(isPurchaser()).toBe(false);
    });

    test('explicit execute:ri-exchange grant alone does NOT make isPurchaser() true', () => {
      // isPurchaser() must consult only the three money-spending verbs
      // (PURCHASER_CARVED_OUTS), not the full carved-out set. Holding
      // execute:ri-exchange (issue #1644, a disjoint carve-out with its
      // own group) is not "can spend money" -- if isPurchaser() looped
      // over every carved-out key it would wrongly return true here and
      // the "add yourself to Purchaser" first-run prompt (userActions.ts)
      // would wrongly stay hidden for an RI-Exchanger-only admin.
      const customGid = '00000000-0000-5000-8000-00000000fade';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: 'ri-exchange' },
      ]);
      expect(isPurchaser()).toBe(false);
    });

    test('RI Exchanger group membership alone (no effectivePermissions) does NOT make isPurchaser() true', () => {
      mockUserWithGroups([RI_EXCHANGER_GROUP_ID]);
      expect(isPurchaser()).toBe(false);
    });
  });

  describe('isRIExchanger', () => {
    test('seeded RI Exchanger group member (no effectivePermissions yet) returns true via fallback', () => {
      // Pre-bootstrap loading window: effectivePermissions not yet
      // populated. The helper falls back to seeded group membership.
      mockUserWithGroups([RI_EXCHANGER_GROUP_ID]);
      expect(isRIExchanger()).toBe(true);
    });

    test('user without RI Exchanger group and no effectivePermissions returns false', () => {
      mockUserWithGroups([ADMIN_GID]); // admin only
      expect(isRIExchanger()).toBe(false);
    });

    test('Purchaser group membership alone does NOT make isRIExchanger() true', () => {
      // Inverse of the isPurchaser regression test above: the two
      // carve-outs are granted by disjoint groups in both directions.
      mockUserWithGroups([PURCHASER_GROUP_ID]);
      expect(isRIExchanger()).toBe(false);
    });

    test('explicit execute:ri-exchange grant in effectivePermissions (custom group) returns true', () => {
      const customGid = '00000000-0000-5000-8000-00000000b00c';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: 'ri-exchange' },
        { action: 'view', resource: 'recommendations' },
      ]);
      expect(isRIExchanger()).toBe(true);
    });

    test('wildcard resource on execute grants RI Exchanger (matches canAccess semantics)', () => {
      const customGid = '00000000-0000-5000-8000-00000000c0de';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: '*' },
      ]);
      expect(isRIExchanger()).toBe(true);
    });

    test('explicit execute:purchases grant does NOT make isRIExchanger() true', () => {
      const customGid = '00000000-0000-5000-8000-00000000da7a';
      mockUserWithGroups([customGid], [
        { action: 'execute', resource: 'purchases' },
      ]);
      expect(isRIExchanger()).toBe(false);
    });

    test('admin:* in effectivePermissions WITHOUT explicit carved-out grant returns false', () => {
      mockUserWithGroups([ADMIN_GID], [
        { action: 'admin', resource: '*' },
      ]);
      expect(isRIExchanger()).toBe(false);
    });

    test('empty effectivePermissions array returns false even with RI_EXCHANGER_GROUP_ID', () => {
      mockUserWithGroups([RI_EXCHANGER_GROUP_ID], []);
      expect(isRIExchanger()).toBe(false);
    });

    test('null user (logged out) returns false', () => {
      mockNoUser();
      expect(isRIExchanger()).toBe(false);
    });
  });
});
