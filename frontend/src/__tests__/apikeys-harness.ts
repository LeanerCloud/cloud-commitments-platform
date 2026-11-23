/**
 * Shared setup for the API keys suites (apikeys*.test.ts): the module mocks and
 * the confirmDialog mock handle. Import this module FIRST so the mocks register
 * before the suites load ../apikeys. This file is not a test (jest only collects
 * *.test.ts).
 */

// Mock the api module
jest.mock('../api', () => ({
  getApiKeys: jest.fn(),
  getApiKeysUsageStats: jest.fn(),
  createApiKey: jest.fn(),
  revokeApiKey: jest.fn(),
  deleteApiKey: jest.fn(),
  getUserPermissions: jest.fn(),
  // Real implementation (not mocked): createApiKey encodes the password
  // field with this before building the request, so tests need the real
  // encoding to assert on the resulting request shape.
  base64Encode: (str: string) => Buffer.from(str, 'binary').toString('base64')
}));

// confirmDialog (introduced in P2) is used for revoke/delete in place of
// native window.confirm. Mock it so tests can flip approve/cancel via
// mockResolvedValueOnce without having to drive the actual modal UI.
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));
