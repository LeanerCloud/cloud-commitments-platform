/**
 * #1050: a 200 approve response can carry a completed exchange, a failed one,
 * or one whose outcome AWS never confirmed. Only "completed" reads as success.
 */
jest.mock('../api/client', () => ({ apiRequest: jest.fn() }));

import { apiRequest } from '../api/client';
import { approveRIExchange } from '../api/riexchange';
import { approveResultToast, parseRIExchangeApproveResult } from '../riexchange-approve-outcome';

describe('parseRIExchangeApproveResult', () => {
  it.each([
    ['a completed body', { status: 'completed', exchange_id: 'exch-1' }, { status: 'completed' }],
    ['an outcome_unknown body', { status: 'outcome_unknown', message: 'check ri-1' }, { status: 'outcome_unknown', message: 'check ri-1' }],
    ['a failed body', { status: 'failed', reason: 'cap' }, { status: 'failed', reason: 'cap' }],
    ['a non-string status', { status: 7 }, { status: '' }],
    ['null', null, { status: '' }],
    ['a string', 'ok', { status: '' }],
  ])('reads %s', (_label, body, want) => {
    expect(parseRIExchangeApproveResult(body)).toEqual(want);
  });
});

describe('approveResultToast', () => {
  it('reports success only for completed', () => {
    expect(approveResultToast({ status: 'completed' })).toEqual({ kind: 'success', message: 'RI exchange approved and executed.', timeout: 5_000 });
  });

  it('keeps an unknown outcome on screen as a warning with the server message', () => {
    expect(approveResultToast({ status: 'outcome_unknown', message: 'outcome unknown: check ri-1' }))
      .toEqual({ kind: 'warning', message: 'outcome unknown: check ri-1', timeout: null });
    expect(approveResultToast({ status: 'outcome_unknown' }).message).toContain('Check the source RIs at AWS');
  });

  it('shows a failed exchange as an error with its reason', () => {
    expect(approveResultToast({ status: 'failed', reason: 'daily cap exceeded' }))
      .toEqual({ kind: 'error', message: 'RI exchange failed: daily cap exceeded', timeout: null });
  });

  it.each(['', 'ok', 'pending'])('treats status %p as an unexpected response, never success', status => {
    const toast = approveResultToast({ status });
    expect(toast.kind).toBe('error');
    expect(toast.timeout).toBeNull();
  });
});

describe('approveRIExchange', () => {
  it('returns the parsed approve body to the history button', async () => {
    (apiRequest as jest.Mock).mockResolvedValue({ status: 'outcome_unknown', message: 'check ri-1' });
    await expect(approveRIExchange('id-1')).resolves.toEqual({ status: 'outcome_unknown', message: 'check ri-1' });
    expect(apiRequest).toHaveBeenCalledWith('/ri-exchange/approve/id-1', expect.objectContaining({ method: 'POST' }));
  });
});
