jest.mock('../api/client', () => ({ apiRequest: jest.fn() }));

import { getLadderEvents, type LadderEvent } from '../api/ladder';
import { apiRequest } from '../api/client';

beforeEach(() => jest.resetAllMocks());

test('actual API loader rejects incomplete or changing pagination', async () => {
  const event: LadderEvent = { id: 'one', config_id: 'config', run_id: 'run', layer_type: 'compute-sp', term: '1yr', payment_option: 'no-upfront', status: 'scheduled', run_status: 'planned', revision: 0, amount_usd_hr: '1.000000', scheduled_date: '2030-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z' };
  jest.mocked(apiRequest).mockResolvedValueOnce({ total_count: 2, total_usd_hr: '2.000000', events: [event], next_cursor: { id: 'one', created_at: event.created_at } }).mockResolvedValueOnce({ total_count: 2, total_usd_hr: '2.000000', events: [{ ...event, id: 'two' }] });
  expect(await getLadderEvents('account', 'aws')).toHaveLength(2);
  expect(jest.mocked(apiRequest).mock.calls[1]?.[0]).toContain('after_id=one');
  jest.mocked(apiRequest).mockResolvedValueOnce({ total_count: 2, total_usd_hr: '2.000000', events: [event] });
  await expect(getLadderEvents('account', 'aws')).rejects.toThrow('changed');
  jest.mocked(apiRequest).mockResolvedValueOnce({ total_count: 2, total_usd_hr: '2.000000', events: [event], next_cursor: { id: 'one', created_at: event.created_at } }).mockResolvedValueOnce({ total_count: 3, events: [] });
  await expect(getLadderEvents('account', 'aws')).rejects.toThrow('changed');
  const firstPage = Array.from({ length: 100 }, (_, index) => ({ ...event, id: `${index}` }));
  for (const secondTotal of ['101.500000', '101.000000']) {
    jest.mocked(apiRequest).mockResolvedValueOnce({ total_count: 101, total_usd_hr: '101.000000', events: firstPage, next_cursor: { id: '99', created_at: event.created_at } }).mockResolvedValueOnce({ total_count: 101, total_usd_hr: secondTotal, events: [{ ...event, id: '100', amount_usd_hr: '1.500000' }] });
    await expect(getLadderEvents('account', 'aws')).rejects.toThrow('changed');
  }
});
