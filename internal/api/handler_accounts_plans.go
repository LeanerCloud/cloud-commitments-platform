package api

import (
	"context"
	"encoding/json"
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
