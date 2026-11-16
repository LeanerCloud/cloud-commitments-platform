import * as state from '../state';
import { canAccess } from '../permissions';

// canCancelScheduledExecution reports whether the current session may cancel a
// scheduled purchase created by createdByUserId. UX gate only: it mirrors the
// backend authorizePlannedPurchaseCancel (internal/api/handler_purchases_cancel_planned.go)
// and must never be more permissive than it; Constraints are enforced there.
//
// Full-scope (any row): admin, update-any:purchases or cancel-any:purchases.
// Creator-scope (own row only): delete:purchases or cancel-own:purchases plus a
// non-null creator id equal to the current user. Legacy rows with no creator
// are out of reach without a full-scope verb.
//
// Plan-bearing rows (non-empty planId): the backend also requires update:plans
// (requirePlanUpdateIfPlanBearing) because cancelling disables the plan, so
// without it the request is always refused and the button stays hidden.
export function canCancelScheduledExecution(
  createdByUserId: string | null | undefined,
  planId?: string | null,
): boolean {
  if (planId && !canAccess('update', 'plans')) return false;
  if (
    canAccess('admin', '*') ||
    canAccess('cancel-any', 'purchases') ||
    canAccess('update-any', 'purchases')
  ) {
    return true;
  }
  const user = state.getCurrentUser();
  if (!user) return false;
  if (!createdByUserId) return false;
  if (createdByUserId !== user.id) return false;
  return canAccess('delete', 'purchases') || canAccess('cancel-own', 'purchases');
}
