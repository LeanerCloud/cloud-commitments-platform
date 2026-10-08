import type { LadderEvent } from '../api/ladder';
import { emptyLadderFilters, filterLadderEvents, ladderFilterOptions, ladderTotal, ladderEditReason } from '../ladder-timeline-filters';

const event: LadderEvent = {
  id: 'one', config_id: 'config', run_id: 'run', layer_type: 'compute-sp', term: '1yr',
  payment_option: 'no-upfront', status: 'scheduled', run_status: 'planned', revision: 0,
  amount_usd_hr: '0.123456', scheduled_date: '2030-01-01T12:00:00Z', created_at: '2026-01-01T00:00:00Z',
};

test('combined filters and cascading options preserve the original events', () => {
  const events = [event, { ...event, id: 'two', term: '3yr', payment_option: 'all-upfront' }];
  const original = JSON.stringify(events);
  const filters = { ...emptyLadderFilters(), term: '1yr', payment_option: 'no-upfront' };
  expect(filterLadderEvents(events, filters)).toEqual([event]);
  expect(ladderFilterOptions(events, filters, 'payment_option')).toEqual(['no-upfront']);
  filters.payment_option = 'all-upfront';
  expect(filterLadderEvents(events, filters)).toEqual([]);
  expect(filterLadderEvents(events, emptyLadderFilters())).toHaveLength(2);
  expect(JSON.stringify(events)).toBe(original);
});

test('six decimal totals and UTC date boundaries stay exact', () => {
  expect(ladderTotal([event, event, event])).toBe('0.370368');
  expect(ladderTotal([])).toBe('0.000000');
  expect(filterLadderEvents([event], { ...emptyLadderFilters(), from: '2030-01-01T12:00:00', to: '2030-01-01T12:00:00' })).toEqual([event]);
  expect(filterLadderEvents([event], { ...emptyLadderFilters(), from: '2030-01-01T12:00:01' })).toEqual([]);
});

test('eligibility covers status, execution, deadline, and unavailable revision API', () => {
  expect(ladderEditReason(event)).toBeUndefined();
  expect(ladderEditReason({ ...event, run_status: 'awaiting_approval' })).toContain('awaiting_approval');
  expect(ladderEditReason({ ...event, status: 'completed' })).toContain('completed');
  expect(ladderEditReason({ ...event, execution_id: 'execution' })).toContain('execution');
  expect(ladderEditReason(event, Date.parse(event.scheduled_date))).toContain('passed');
  expect(ladderEditReason({ ...event, revision: undefined as unknown as number })).toContain('upgraded');
});
