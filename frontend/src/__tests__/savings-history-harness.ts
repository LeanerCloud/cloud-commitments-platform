/**
 * Shared setup for the savings history suites (savings-history*.test.ts): the
 * chart.js, api and state module mocks and the shared chart instance handle.
 * Import this module FIRST so the mocks register before the suites load the
 * module under test. This file is not a test (jest only collects *.test.ts).
 */

// Mock Chart.js BEFORE importing the module
// Define mock instance that will be shared
export const mockChartInstance = {
  destroy: jest.fn(),
  update: jest.fn()
};

// Mock chart.js module - must define MockChart inside the factory
jest.mock('chart.js', () => {
  const MockChart = jest.fn().mockImplementation(() => mockChartInstance) as jest.Mock & { register: jest.Mock };
  MockChart.register = jest.fn();
  return {
    Chart: MockChart,
    registerables: []
  };
});

// Mock the api module
jest.mock('../api', () => ({
  getSavingsAnalytics: jest.fn()
}));

// Mock the state module. Defaults (empty provider, no accounts) keep the
// existing loadSavingsHistory tests unchanged: with these defaults
// loadSavingsHistory adds neither `provider` nor `account_ids` to the
// request, so the `objectContaining({ interval })` assertions still hold.
jest.mock('../state', () => ({
  subscribeProvider: jest.fn(),
  subscribeAccount: jest.fn(),
  getCurrentProvider: jest.fn(() => ''),
  getCurrentAccountIDs: jest.fn(() => [])
}));
