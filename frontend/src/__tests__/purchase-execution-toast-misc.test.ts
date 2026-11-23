/**
 * handleExecutePurchase double-submit guard, payment coercion disclosure and
 * missing payment option toast tests. Split from purchase-execution-toast.test.ts
 * to stay under the 500-line ceiling; the module mocks and helpers live in the
 * harness.
 */

// The harness registers the module mocks, so it must be imported first.
import {
  buildMinimalRec,
  setup,
} from './purchase-execution-toast-harness';
import * as api from '../api';
import * as recs from '../recommendations';
import * as plans from '../plans';
import { confirmDialog } from '../confirmDialog';

// ── tests ─────────────────────────────────────────────────────────────────────

// #644: the execute button must be disabled BEFORE the confirm dialog / network
// call so a double-click can't fire a second POST (duplicate pending execution).
describe('handleExecutePurchase — double-submit guard (#644)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (recs.getFanOutBuckets as jest.Mock).mockReturnValue(null);
    (recs.getPurchaseModalRecommendations as jest.Mock).mockReturnValue([buildMinimalRec()]);
    (plans.closePurchaseModal as jest.Mock).mockImplementation(() => undefined);
  });

  afterEach(() => {
    document.body.textContent = '';
  });

  test('button is disabled while the confirm dialog is pending (before any POST)', async () => {
    let resolveConfirm: (v: boolean) => void = () => undefined;
    (confirmDialog as jest.Mock).mockReturnValueOnce(
      new Promise<boolean>((resolve) => {
        resolveConfirm = resolve;
      }),
    );
    (api.executePurchase as jest.Mock).mockResolvedValue({
      execution_id: 'exec-1',
      status: 'pending',
      email_sent: true,
    });

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    // Confirm dialog is still open: button disabled, no POST yet.
    expect(btn.disabled).toBe(true);
    expect(api.executePurchase).not.toHaveBeenCalled();

    resolveConfirm(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(api.executePurchase).toHaveBeenCalledTimes(1);
  });

  test('button is re-enabled when the user cancels the confirm dialog', async () => {
    (confirmDialog as jest.Mock).mockResolvedValueOnce(false);

    const btn = setup();
    btn.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(api.executePurchase).not.toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
    expect(btn.textContent).toBe('Send for approval');
  });
});
