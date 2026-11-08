import type { ToastKind } from './toast';

/** Body of a 200 approve response: the backend executes the exchange before replying (#1050). */
export interface RIExchangeApproveResult {
  status: string;
  message?: string;
  reason?: string;
}

export interface ApproveToast {
  kind: ToastKind;
  message: string;
  timeout: number | null;
}

/** Reads the approve body; anything without a string status is kept as an unknown status. */
export function parseRIExchangeApproveResult(body: unknown): RIExchangeApproveResult {
  if (!body || typeof body !== 'object') return { status: '' };
  const { status, message, reason } = body as Record<string, unknown>;
  return {
    status: typeof status === 'string' ? status : '',
    ...(typeof message === 'string' ? { message } : {}),
    ...(typeof reason === 'string' ? { reason } : {}),
  };
}

/**
 * The toast for an approve result. Only "completed" is a success: a 200 can
 * also carry a failed exchange, or one whose outcome AWS never confirmed, which
 * the operator must check before trying again. An unrecognised body is an
 * error, never a success.
 */
export function approveResultToast(result: RIExchangeApproveResult): ApproveToast {
  switch (result.status) {
    case 'completed':
      return { kind: 'success', message: 'RI exchange approved and executed.', timeout: 5_000 };
    case 'outcome_unknown':
      return {
        kind: 'warning',
        message: result.message ?? 'The RI exchange outcome is unknown. Check the source RIs at AWS before trying again.',
        timeout: null,
      };
    case 'failed':
      return { kind: 'error', message: `RI exchange failed: ${result.reason ?? 'no reason given'}`, timeout: null };
    default:
      return {
        kind: 'error',
        message: 'The server returned an unexpected approve response. Check the exchange history before trying again.',
        timeout: null,
      };
  }
}
