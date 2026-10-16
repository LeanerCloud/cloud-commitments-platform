import { planEditorReason } from '../lib/plan-editor-reasons';

const expected: Record<string, string> = {
  completed_prefix: 'This step already ran and cannot be changed.',
  completion_unavailable: 'Completion could not be determined, so this step cannot be changed.',
  status_fixed: 'This step is already in progress or finished and cannot be changed.',
  ambiguous_root: 'Several purchases match this step, so it cannot be changed.',
  occupied_capacity: 'Another purchase already uses the capacity for this step.',
  invalid_date: 'This step has no valid date, so it cannot be changed.',
  not_future: "This step's date has already passed, so it cannot be changed.",
  date_order: 'The saved step dates are out of order, so this step cannot be changed.',
  legacy_uncertain: 'This step was approved before approvals were tracked, so it cannot be changed.',
  permission_denied: 'You do not have permission to change this.',
  approved_edits_disabled: 'This is approved. Turn on approved edits to change it.',
  affected_root_fixed: 'A purchase affected by this change is already fixed.',
  progress_or_baseline: 'The plan has progress or a baseline, so this cannot be changed.',
  unsupported_field: 'This field cannot be edited here.',
  unsupported_schedule: 'This plan has a schedule that cannot be edited here.',
};

test('maps every backend reason code to its exact sentence', () => {
  expect(Object.fromEntries(Object.keys(expected).map(code => [code, planEditorReason(code, 'x')]))).toEqual(expected);
  expect(Object.keys(expected)).toHaveLength(15);
});

test('keeps an unknown code visible instead of guessing or hiding it', () => {
  expect(planEditorReason('brand_new_code', 'fallback')).toBe('Locked (brand_new_code)');
  expect(planEditorReason('toString', 'fallback')).toBe('Locked (toString)');
});

test('uses the fallback only when there is no code', () => {
  expect(planEditorReason(null, 'needs creation')).toBe('needs creation');
});
