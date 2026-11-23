/**
 * Shared setup for the users*.test.ts files: module mocks and the confirmDialog
 * mock. Import it first so the mocks are registered before ../users/* loads.
 */

import './setup';

// Mock the api module
jest.mock('../api', () => ({
  listUsers: jest.fn(),
  listGroups: jest.fn(),
  deleteUser: jest.fn(),
  updateUser: jest.fn(),
  createUser: jest.fn(),
  getUser: jest.fn(),
}));

// Mock the groups/groupList module
jest.mock('../groups/groupList', () => ({
  renderGroups: jest.fn(),
}));

// Q7: userActions.deleteUser / bulkDeleteUsers migrated from window.confirm
// to confirmDialog. Mock it so existing tests don't hang waiting for a
// real modal click.
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));
