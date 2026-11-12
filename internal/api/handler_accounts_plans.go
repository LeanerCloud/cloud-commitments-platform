package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// validatePlanAccountProviders enforces the issue-#209 / spec E-4 rule
// that every account assigned to a plan must have its provider match
// one of the plan's derived providers. Returns:
//   - 404 ClientError when the plan does not exist
//   - 404 ClientError when an account_id does not exist (referencing
//     the offending ID)
//   - 400 ClientError listing every provider mismatch in one message
//     (so clients fix everything in one round-trip rather than
//     resubmitting to discover the next)
//   - 500 ClientError when a stored service key names no provider (fail
//     closed: skipping the key could leave the provider set empty, which
//     would accept an account of any provider, issue #316)
//   - nil when all accounts match (or when the plan has no services at
//     all — production plans always carry at least one service, frontend
//     enforces this)
//
// Pulled out of setPlanAccounts to keep that function under the gocyclo
// budget (limit 10). No business logic lives here that isn't otherwise
// described in setPlanAccounts' doc comment.
func (h *Handler) validatePlanAccountProviders(ctx context.Context, planID string, accountIDs []string) error {
	plan, err := h.getPlanForAccountProviderValidation(ctx, planID)
	if err != nil {
		return err
	}

	expected, err := config.DerivePlanProviders(plan)
	if err != nil {
		// The key is stored data, not the caller's input; log it and keep
		// the plan UUID out of the response, as the 500s above do.
		logging.Errorf("validatePlanAccountProviders: %v", err)
		return NewClientError(500, "failed to validate plan accounts")
	}
	if len(expected) == 0 {
		return nil
	}

	type mismatch struct {
		ID       string
		Name     string
		Provider string
	}
	var mismatches []mismatch
	for _, aid := range accountIDs {
		acct, getErr := h.config.GetCloudAccount(ctx, aid)
		if getErr != nil {
			// Do NOT wrap getErr with the raw account UUID: a non-ClientError
			// here propagates to the router's `logging.Errorf("API error: %v")`,
			// which would leak the account UUID and the raw DB error string
			// into logs (issue #944(b): log account counts, never raw IDs).
			// Map to a 404 if the store reports not-found, else a structured
			// 500 that logs only the derived providers + account count.
			return mapCreatePlanStorageError(getErr,
				"account not found", "failed to validate plan accounts",
				"validatePlanAccountProviders: GetCloudAccount failed (providers=%v accounts=%d)",
				expected, len(accountIDs))
		}
		if acct == nil {
			return NewClientError(404, fmt.Sprintf("account not found: %s", aid))
		}
		if !slices.Contains(expected, acct.Provider) {
			mismatches = append(mismatches, mismatch{ID: aid, Name: acct.Name, Provider: acct.Provider})
		}
	}
	if len(mismatches) == 0 {
		return nil
	}

	parts := make([]string, len(mismatches))
	for i, m := range mismatches {
		parts[i] = fmt.Sprintf("account %q has provider=%q, expected one of %v",
			m.Name, m.Provider, expected)
	}
	return NewClientError(400, "plan provider mismatch: "+strings.Join(parts, "; "))
}

func (h *Handler) getPlanForAccountProviderValidation(ctx context.Context, planID string) (*config.PurchasePlan, error) {
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err != nil {
		// Do NOT wrap err with the raw plan UUID: a non-ClientError propagates
		// to the router's logging.Errorf("API error: %v"), leaking the raw DB
		// error string (issue #965). Map to 404 on ErrNotFound; else log a
		// structured 500 without the DB error or the plan UUID.
		return nil, mapCreatePlanStorageError(err,
			"plan not found", "failed to validate plan",
			"getPlanForAccountProviderValidation: GetPurchasePlan failed")
	}
	if plan == nil {
		return nil, NewClientError(404, fmt.Sprintf("plan not found: %s", planID))
	}
	return plan, nil
}

// setPlanAccounts handles PUT /api/plans/:id/accounts.
//
// Per issue #209 / spec acceptance criterion E-4, every account assigned
// to a plan must have its provider match one of the plan's derived
// providers (extracted from plan.Services keys). Mismatches return 400
// listing every offender; the assignment is rejected atomically (no
// partial writes).
//
// update:plans is a default Standard User grant, so the verb gate alone does
// not bound WHICH plan or WHICH accounts the caller may write (issue #1769).
// requirePlanAccountsAccess supplies that bound on both axes and runs before
// the provider validation below, so a scoped caller cannot use the
// mismatch/not-found messages to learn about accounts outside their scope.
func (h *Handler) setPlanAccounts(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "plans")
	if err != nil {
		return nil, err
	}

	var body struct {
		AccountIDs []string `json:"account_ids"`
	}
	if err := json.Unmarshal([]byte(httpReq.Body), &body); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	if err := validatePlanAccountIDs(body.AccountIDs); err != nil {
		return nil, err
	}

	if err := h.requirePlanAccountsAccess(ctx, session, id, body.AccountIDs); err != nil {
		return nil, err
	}

	// Provider-match validation (issue #209), before the arming gate so a
	// malformed request is a 400 whatever the plan's armed state. Extracted to
	// keep setPlanAccounts under the gocyclo budget (10).
	if err := h.validatePlanAccountProviders(ctx, id, body.AccountIDs); err != nil {
		return nil, err
	}

	return nil, h.storePlanAccounts(ctx, httpReq, session, id, body.AccountIDs)
}

// storePlanAccounts writes the plan's accounts. The retarget grant is decided
// first, outside any transaction, against the plan as read then; the gate under
// the plan's row lock only confirms the plan has not changed since (an arm via
// updatePlan / patchPlan takes the same lock), and the decision is redone if it
// has.
func (h *Handler) storePlanAccounts(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, session *Session, id string, accountIDs []string) error {
	for attempt := 0; attempt < planGateAttempts; attempt++ {
		seen, err := h.requireRetargetOfArmedPlan(ctx, httpReq, session, id, accountIDs)
		if err != nil {
			return err
		}
		err = h.config.SetPlanAccountsGated(ctx, id, accountIDs, func(locked *config.PurchasePlan, _ []string) error {
			if !locked.UpdatedAt.Equal(seen.UpdatedAt) {
				return errPlanMovedUnderGate
			}
			return nil
		})
		if errors.Is(err, errPlanMovedUnderGate) {
			continue
		}
		return mapSetPlanAccountsError(err)
	}
	return errPlanGateContended
}

func mapSetPlanAccountsError(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := IsClientError(err); ok {
		return ce
	}
	if errors.Is(err, config.ErrNotFound) {
		return NewClientError(404, err.Error())
	}
	if errors.Is(err, config.ErrEditorReconciliationRequired) {
		return editorReconciliationError()
	}
	return fmt.Errorf("accounts: %w", err)
}

// validatePlanAccountIDs rejects a missing/empty account_ids payload and
// entries that are not valid UUIDs.
//
// Empty is refused because a plan must remain tied to at least one
// cloud_account row: letting the PUT clear all rows would recreate the
// universal-plan bug class (a purchase_plans row with no matching
// plan_accounts row) that createPlan refuses at insert time. It is also what
// keeps the account-axis scope check in requirePlanAccountsAccess meaningful,
// since an empty list has nothing to check.
//
// Same contract as validateTargetAccounts (handler_plans.go) with this
// endpoint's field names in the messages, so a payload that gets past
// createPlan also gets past here.
func validatePlanAccountIDs(ids []string) error {
	if len(ids) == 0 {
		return NewClientError(400, "account_ids is required: a plan must be tied to at least one account")
	}
	for _, aid := range ids {
		if err := validateUUID(aid); err != nil {
			return NewClientError(400, fmt.Sprintf("invalid account_id %q: must be a valid UUID", aid))
		}
	}
	return nil
}

// listPlanAccounts handles GET /api/plans/:id/accounts.
//
// The read sibling of setPlanAccounts, and it carries the mirror image of the
// same gap (issue #1769): view:plans is a default Standard User grant, so
// without requirePlanAccess a scoped caller could enumerate the accounts of
// any plan by id. requirePlanAccess bounds WHICH plans are visible; the
// per-account filter then bounds WHAT of a visible plan is disclosed, since a
// plan the caller legitimately reaches through one of their accounts may also
// carry accounts they have no entitlement to see.
func (h *Handler) listPlanAccounts(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "plans")
	if err != nil {
		return nil, err
	}

	err = h.requirePlanAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	accts, err := h.config.GetPlanAccounts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}

	// Allows() is true for every account of an unrestricted scope, so this is
	// one loop rather than a branch on AllowsAll. make() also gives the
	// endpoint its no-nil-slice contract for free.
	scoped := make([]config.CloudAccount, 0, len(accts))
	for i := range accts {
		if allowed.Allows(accts[i].ID, accts[i].Name) {
			scoped = append(scoped, accts[i])
		}
	}

	return scoped, nil
}
