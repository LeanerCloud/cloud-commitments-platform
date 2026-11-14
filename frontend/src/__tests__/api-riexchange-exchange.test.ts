/**
 * Request bodies the AWS RI exchange client posts (#1957, A12-043). Field
 * names mirror internal/api/handler_ri_exchange.go: ExchangeQuoteRequestBody
 * and ExchangeExecuteRequestBody (ri_ids, targets[{offering_id,count}],
 * target_offering_id, target_count, region, max_payment_due_usd).
 */
import { apiRequest } from '../api/client';
import { getExchangeQuote, executeExchange, approveRIExchange } from '../api/riexchange';

jest.mock('../api/client', () => ({ apiRequest: jest.fn() }));

beforeEach(() => (apiRequest as jest.Mock).mockReset().mockResolvedValue({ status: 'completed' }));

const bodyOf = (call: number): Record<string, unknown> =>
  JSON.parse((apiRequest as jest.Mock).mock.calls[call]![1].body as string);

describe('AWS RI exchange request bodies', () => {
  it('posts the quote singleton shape to /ri-exchange/quote', async () => {
    await getExchangeQuote({ ri_ids: ['ri-1'], target_offering_id: 'off-1', target_count: 3, region: 'us-west-2' });
    expect((apiRequest as jest.Mock).mock.calls[0]![0]).toBe('/ri-exchange/quote');
    expect((apiRequest as jest.Mock).mock.calls[0]![1].method).toBe('POST');
    expect(bodyOf(0)).toEqual({ ri_ids: ['ri-1'], target_offering_id: 'off-1', target_count: 3, region: 'us-west-2' });
  });

  it('posts the quote targets[] shape without legacy singleton fields', async () => {
    await getExchangeQuote({ ri_ids: ['ri-1'], targets: [{ offering_id: 'a', count: 1 }, { offering_id: 'b', count: 2 }] });
    const body = bodyOf(0);
    expect(body.targets).toEqual([{ offering_id: 'a', count: 1 }, { offering_id: 'b', count: 2 }]);
    expect(body).not.toHaveProperty('target_offering_id');
    expect(body).not.toHaveProperty('target_count');
  });

  it('posts the execute body with the spend cap and region to /ri-exchange/execute', async () => {
    await executeExchange({
      ri_ids: ['ri-1'], target_offering_id: 'off-1', target_count: 3, max_payment_due_usd: '12.50', region: 'us-west-2',
    });
    expect((apiRequest as jest.Mock).mock.calls[0]![0]).toBe('/ri-exchange/execute');
    expect((apiRequest as jest.Mock).mock.calls[0]![1].method).toBe('POST');
    expect(bodyOf(0)).toEqual({
      ri_ids: ['ri-1'], target_offering_id: 'off-1', target_count: 3, max_payment_due_usd: '12.50', region: 'us-west-2',
    });
  });

  it('posts a bodiless POST to /ri-exchange/approve/<encoded id>', async () => {
    await approveRIExchange('a/b');
    const [path, opts] = (apiRequest as jest.Mock).mock.calls[0]!;
    expect(path).toBe('/ri-exchange/approve/a%2Fb');
    expect(opts).toEqual({ method: 'POST' });
  });
});
