/**
 * Shared setup for the permissions helper suites (permissions*.test.ts): the
 * mocked current user, the seeded group ids and the user builders. Import this
 * module FIRST in each suite so the ../state mock is registered before the
 * suite loads ../permissions. This file is not a test (jest only collects
 * *.test.ts).
 */
import { ADMINISTRATORS_GROUP_ID } from '../permissions';
import type { PermissionEntry } from '../api/types';

jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
}));

import * as state from '../state';

export const ADMIN_GID = ADMINISTRATORS_GROUP_ID;
// Standard Users group seeded by migration 000057. Must NOT collide with
// PURCHASER_GROUP_ID ('...005'); otherwise the fallback path (effectivePermissions
// absent) would treat the standard-user fixture as a Purchaser and let the
// carved-out money-spending verbs through (CR finding, PR #924).
export const STD_GID = '00000000-0000-5000-8000-000000000002';
export const RO_GID = '00000000-0000-5000-8000-000000000006';

export const mockUserWithGroups = (groups: string[], effectivePermissions?: PermissionEntry[]) => {
  (state.getCurrentUser as jest.Mock).mockReturnValue(
    { id: 'u1', email: 'u@example.com', groups, effectivePermissions },
  );
};

export const mockNoUser = () => {
  (state.getCurrentUser as jest.Mock).mockReturnValue(null);
};
