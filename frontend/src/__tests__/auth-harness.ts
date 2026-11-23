/**
 * Shared setup for the auth suites: the utils, api, state and toast module
 * mocks. Import this module FIRST so the mocks register before a suite loads
 * ../auth. This file is not a test (jest only collects *.test.ts).
 */
jest.mock('../utils', () => ({
  ...jest.requireActual('../utils'),
  reloadPage: jest.fn(),
}));

// Mock the api module
jest.mock('../api', () => {
  // Mirror the real MFALoginError class shape so production code's
  // `error instanceof api.MFALoginError` branch can be exercised by
  // tests. Defined inside the factory so the inline `class`
  // declaration isn't hoisted past the jest.mock factory boundary.
  class MFALoginError extends Error {
    code: string;
    constructor(code: string) {
      super(code);
      this.name = 'MFALoginError';
      this.code = code;
      Object.setPrototypeOf(this, MFALoginError.prototype);
    }
  }
  return {
    login: jest.fn(),
    logout: jest.fn(),
    requestPasswordReset: jest.fn(),
    resetPassword: jest.fn(),
    getResetTokenStatus: jest.fn(),
    apiRequest: jest.fn(),
    base64Encode: (s: string) => btoa(s),
    MFALoginError,
    setupMFA: jest.fn(),
    enableMFA: jest.fn(),
    disableMFA: jest.fn(),
    regenerateMFARecoveryCodes: jest.fn(),
  };
});

// Mock the state module
jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
  setCurrentUser: jest.fn()
}));

// Mock toast so alert() -> showToast() migrations (finding 11-L3) don't
// require a real DOM toast implementation in jsdom.
export const mockShowToast = jest.fn();
jest.mock('../toast', () => ({
  showToast: (...args: unknown[]) => mockShowToast(...args),
}));
