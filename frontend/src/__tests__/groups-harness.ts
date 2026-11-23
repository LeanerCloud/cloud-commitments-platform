/**
 * Shared mocks and fixtures for the groups*.test.ts suites. Import this module
 * first: its jest.mock calls must register before the suites import the modules
 * under test.
 */
import './setup';

// Mock the api module
jest.mock('../api', () => ({
  listGroups: jest.fn(),
  createGroup: jest.fn(),
  updateGroup: jest.fn(),
  deleteGroup: jest.fn(),
  getGroup: jest.fn(),
  listAccounts: jest.fn(),
}));

// Mock the loadUsers function from userActions
jest.mock('../users/userActions', () => ({
  loadUsers: jest.fn().mockResolvedValue(undefined),
}));

// Mock confirmDialog — groupActions.deleteGroup migrated from window.confirm
// to the shared destructive-confirm dialog; without a mock the dialog waits
// for DOM interaction that never arrives and the tests time out.
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>();
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts)
}));

import * as api from '../api';

// Mock group data
export const mockGroups: api.APIGroup[] = [
  {
    id: 'group-1',
    name: 'Administrators',
    description: 'Admin group with full access',
    permissions: [
      { action: 'execute', resource: '*' },
      { action: 'update', resource: '*' },
    ],
    created_at: '2024-01-15T10:00:00Z',
  },
  {
    id: 'group-2',
    name: 'Viewers',
    description: 'Read-only access',
    permissions: [
      { action: 'view', resource: '*' },
    ],
    created_at: '2024-02-20T14:30:00Z',
  },
  {
    id: 'group-3',
    name: 'Empty Group',
    description: '',
    permissions: [],
  },
];

// Mock users that belong to groups (PR #912: role field removed, groups is the source of truth)
export const mockUsers = [
  { id: 'user-1', email: 'admin@test.com', groups: ['00000000-0000-5000-8000-000000000001', 'group-1'], mfa_enabled: true },
  { id: 'user-2', email: 'viewer@test.com', groups: ['group-2'], mfa_enabled: false },
  { id: 'user-3', email: 'both@test.com', groups: ['group-1', 'group-2'], mfa_enabled: true },
];
