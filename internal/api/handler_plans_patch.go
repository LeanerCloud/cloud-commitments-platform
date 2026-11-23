package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// PatchPlanRequest represents a partial update request for plans.
type PatchPlanRequest struct {
	Name                   *string `json:"name,omitempty"`
	Enabled                *bool   `json:"enabled,omitempty"`
	AutoPurchase           *bool   `json:"auto_purchase,omitempty"`
	NotificationDaysBefore *int    `json:"notification_days_before,omitempty"`
}

// patchDeescalates reports whether a PATCH only stops the plan: it disables or
// disarms it and turns nothing on. Such a write skips the bound-group checks
// (ValidateDeescalation), so a plan can always be stopped; a PATCH that also
// enables or arms the plan runs every check.
func patchDeescalates(req PatchPlanRequest) bool {
	off := func(b *bool) bool { return b != nil && !*b }
	on := func(b *bool) bool { return b != nil && *b }
	return (off(req.Enabled) || off(req.AutoPurchase)) && !on(req.Enabled) && !on(req.AutoPurchase)
}

func validatePatchedPlan(plan *config.PurchasePlan, req PatchPlanRequest) error {
	if patchDeescalates(req) {
		return plan.ValidateDeescalation()
	}
	return plan.Validate()
}

// applyPatchFields applies validated partial-update fields to a plan.
func applyPatchFields(plan *config.PurchasePlan, req PatchPlanRequest) error {
	if req.Name != nil {
		if *req.Name == "" {
			return NewClientError(400, "plan name cannot be empty")
		}
		if len(*req.Name) > 255 {
			return NewClientError(400, "plan name too long (max 255 characters)")
		}
		plan.Name = *req.Name
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AutoPurchase != nil {
		plan.AutoPurchase = *req.AutoPurchase
	}
	if req.NotificationDaysBefore != nil {
		if *req.NotificationDaysBefore < 0 || *req.NotificationDaysBefore > 30 {
			return NewClientError(400, "notification_days_before must be between 0 and 30")
		}
		plan.NotificationDaysBefore = *req.NotificationDaysBefore
	}
	return nil
}

// patchPlan handles partial updates to a plan (PATCH method).
func (h *Handler) patchPlan(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, planID string) (any, error) {
	if err := validateUUID(planID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "plans")
	if err != nil {
		return nil, err
	}

	err = h.requirePlanMutationAccess(ctx, session, planID)
	if err != nil {
		return nil, err
	}

	var req PatchPlanRequest
	err = json.Unmarshal([]byte(httpReq.Body), &req)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// Do NOT wrap err with the raw plan UUID: a non-ClientError propagates to
	// the router's logging.Errorf("API error: %v"), leaking the plan UUID and
	// the raw DB error string (issue #965, same shape as the createPlan-side
	// fix).
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err != nil {
		return nil, mapCreatePlanStorageError(err,
			"plan not found", "failed to load plan",
			"patchPlan: GetPurchasePlan failed")
	}
	if plan == nil {
		return nil, NewClientError(404, fmt.Sprintf("plan not found: %s", planID))
	}

	before := *plan
	if err := applyPatchFields(plan, req); err != nil {
		return nil, err
	}

	if err := validatePatchedPlan(plan, req); err != nil {
		return nil, NewClientError(400, fmt.Sprintf("validation error: %s", err))
	}

	if err := h.updatePlanGated(ctx, httpReq, session, &before, plan, "patchPlan"); err != nil {
		return nil, err
	}

	return planForResponse(plan), nil
}
