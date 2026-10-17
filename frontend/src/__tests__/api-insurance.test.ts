import { getInsuranceComparison, getInsuranceStatus, getRecommendationInsuranceComparison } from '../api/insurance';
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
});

test('recommendation comparison encodes the exact stored ID', async () => {
  await getRecommendationInsuranceComparison('stored/id?&');
  expect(apiRequest).toHaveBeenLastCalledWith('/insurance/comparison?recommendation_id=stored%2Fid%3F%26');
});
