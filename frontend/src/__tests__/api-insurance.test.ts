import { getInsuranceComparison, getInsuranceRecommendation, getInsuranceStatus } from '../api/insurance';
import { apiRequest } from '../api/client';

jest.mock('../api/client', () => ({ apiRequest: jest.fn() }));

describe('insurance api', () => {
  beforeEach(() => jest.resetAllMocks());

  it('status is a plain GET with no query or body', async () => {
    await getInsuranceStatus();
    expect(apiRequest).toHaveBeenCalledWith('/insurance/status');
  });

  it('comparison is a plain GET with no query or body', async () => {
    await getInsuranceComparison();
    expect(apiRequest).toHaveBeenCalledWith('/insurance/comparison');
  });

  it('recommendation quote is a GET on the encoded id and forwards the signal', async () => {
    const ac = new AbortController();
    await getInsuranceRecommendation('a/b c', ac.signal);
    expect(apiRequest).toHaveBeenCalledWith('/insurance/recommendations/a%2Fb%20c', { signal: ac.signal });
    await getInsuranceRecommendation('x');
    expect(apiRequest).toHaveBeenLastCalledWith('/insurance/recommendations/x', {});
  });
});
