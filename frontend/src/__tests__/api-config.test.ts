/**
 * Unit tests for API module (config, purchase and public info)
 */
import { fetchMock } from './setup';
import {
  getConfig,
  updateConfig,
  executePurchase,
  getPurchaseDetails,
  cancelPurchase,
  getPublicInfo,
  getDeploymentInfo
} from '../api';
import type { Config, Recommendation } from '../api';

describe('Config API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getConfig', () => {
    test('fetches config', async () => {
      await getConfig();
      expect(fetchMock).toHaveBeenCalledWith('/api/config', expect.anything());
    });
  });

  describe('updateConfig', () => {
    test('sends PUT with config data', async () => {
      const config: Config = {
        enabled_providers: ['aws', 'azure'],
        notification_email: 'test@example.com',
        auto_collect: true,
        collection_schedule: 'daily',
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3
      };
      await updateConfig(config);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/config',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(config)
        })
      );
    });
  });
});

describe('Purchase API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('executePurchase', () => {
    test('sends POST with recommendations', async () => {
      const recs: Recommendation[] = [{
        id: 'rec-1',
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
        resource_type: 'm5.large',
        count: 1,
        term: 3,
        payment: 'all-upfront',
        upfront_cost: 100,
        monthly_cost: 0,
        savings: 30,
        selected: true,
        purchased: false,
      }];
      await executePurchase(recs);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/purchases/execute',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ recommendations: recs })
        })
      );
    });
  });

  describe('getPurchaseDetails', () => {
    test('fetches purchase by ID', async () => {
      await getPurchaseDetails('exec-123');
      expect(fetchMock).toHaveBeenCalledWith('/api/purchases/exec-123', expect.anything());
    });
  });

  describe('cancelPurchase', () => {
    test('sends POST to cancel', async () => {
      await cancelPurchase('exec-123');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/purchases/cancel/exec-123',
        expect.objectContaining({ method: 'POST' })
      );
    });
  });
});

describe('Public Info API', () => {
  describe('getPublicInfo', () => {
    test('fetches version and admin_exists without auth (#633: no sensitive fields)', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ version: '1.0.0', admin_exists: true })
      });

      const info = await getPublicInfo();
      expect(info.version).toBe('1.0.0');
      expect(info.admin_exists).toBe(true);
      // Sensitive fields must not be present on the public endpoint response.
      expect((info as unknown as Record<string, unknown>)['api_key_secret_url']).toBeUndefined();
      expect((info as unknown as Record<string, unknown>)['deployment_aws_account_id']).toBeUndefined();
      expect(fetchMock).toHaveBeenCalledWith('/api/info');
    });

    test('throws with the HTTP status on a non-OK response instead of fabricating admin_exists: false', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 502 });

      await expect(getPublicInfo()).rejects.toMatchObject({ status: 502, message: expect.stringContaining('HTTP 502') });
    });
  });

  describe('getDeploymentInfo', () => {
    test('fetches api_key_secret_url and deployment_aws_account_id with auth', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({
          api_key_secret_url: 'https://us-east-1.console.aws.amazon.com/secretsmanager/secret?name=arn:...',
          deployment_aws_account_id: '123456789012',
        })
      });

      const info = await getDeploymentInfo();
      expect(info.api_key_secret_url).toContain('secretsmanager');
      expect(info.deployment_aws_account_id).toBe('123456789012');
      expect(fetchMock).toHaveBeenCalledWith('/api/info/deployment', expect.objectContaining({}));
    });

    test('throws with the HTTP status on a non-OK response', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 401 });

      await expect(getDeploymentInfo()).rejects.toMatchObject({ status: 401 });
    });
  });
});
