import type { PlanEditorFailure } from './api/plan-editor';

// What the footer does after a failed preview or Save: reload the editor, redo the review, resend the frozen request, focus the date, or stop.
export type FailureAction = 'reload' | 'review' | 'retry' | 'focus-date' | 'sign-in' | 'gone' | 'none';
export interface FailureView { readonly text: string; readonly action: FailureAction }

const MAYBE_CREATED = 'This plan may already have been created. Check the plans list before saving again.';
const reloadHint = 'Reload to see its current state.';

// Server messages are shown as text only; every other sentence here is fixed. `create` is false for Edit.
export function describeFailure(failure: PlanEditorFailure, phase: 'preview' | 'save', create: boolean): FailureView {
  const { message, code } = failure, view = (text: string, action: FailureAction = 'none'): FailureView => ({ text, action });
  switch (failure.kind) {
    case 'stale':
      if (create) return code === 'plan_changed' && phase === 'save' ? view(MAYBE_CREATED) : view(message);
      return code === 'plan_changed' ? view(`This plan changed since you opened it. ${reloadHint} Changes made here are not applied.`, 'reload') : view(`${message} ${reloadHint}`, 'reload');
    case 'locked': return create ? view(message) : view(`${message} Reload to see which fields are locked.`, 'reload');
    case 'sequence': return view(message, 'focus-date');
    case 'review': return phase === 'save' ? view('The plan changed after this review, so the review was refreshed. Check the changes and tick the boxes again.', 'review')
      : view('The plan changed while you were reviewing. Try Review changes again.');
    case 'too-large': return code === null ? view('This change is too large to save.') : view(`${message} This plan has more purchase history than the editor can save.`);
    case 'unavailable': return view('The plan editor is not available right now. Try again later.');
    case 'busy': return phase === 'save' ? view('The server is busy and did not apply your change. Wait a moment, then use Save changes to retry.', 'retry')
      : view('The server is busy. Wait a moment, then try Review changes again.');
    case 'unauthenticated': return view('Your session expired. Reload the page and sign in again; unsaved changes in this dialog cannot be kept.', 'sign-in');
    case 'not-found': return create ? view(message) : view('This plan no longer exists or is out of scope. Close this dialog.', 'gone');
    case 'uncertain':
      if (phase === 'preview') return view('The preview did not complete. Nothing was saved; try Review changes again.');
      return view(create ? MAYBE_CREATED : message, 'retry');
    default: return view(message); // unsupported, forbidden, invalid, server: the server's sentence, draft kept
  }
}
