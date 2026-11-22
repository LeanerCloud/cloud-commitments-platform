package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
)

// getInsuranceRecommendation returns the Archera quote for one stored
// recommendation. Same gate as the plan-wide comparison (view:recommendations,
// unrestricted account scope: the Archera plan is deployment-global, so a
// scoped caller gets 404 before any lookup or vendor call). A recommendation
// with no exact vendor line item is a 200 with status "unavailable" and no
// figures; an ambiguous match is a 409 rather than a guess.
func (h *Handler) getInsuranceRecommendation(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (*archera.RecommendationComparisonDTO, error) {
	if err := h.requireInsuranceAccess(ctx, req); err != nil {
		return nil, err
	}
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	stored, _, err := h.scheduler.GetRecommendationByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch recommendation: %w", err)
	}
	if stored == nil {
		return nil, errNotFound
	}
	identity, err := archera.ProjectRecommendation(stored)
	if err != nil {
		return nil, NewClientError(422, "recommendation is not eligible for an Archera quote")
	}
	cmp, items, err := h.FetchRecommendationComparison(ctx, identity)
	if err != nil {
		return nil, err
	}
	dto, err := archera.BuildRecommendationComparison(stored.ID, h.insurance.PlanID(), identity, cmp, items)
	if err != nil {
		if errors.Is(err, archera.ErrUnrepresentable) {
			return nil, NewClientError(502, "Archera returned a value that cannot be shown exactly")
		}
		return nil, NewClientError(502, "Archera returned data that does not match its line items")
	}
	if dto.Reason == archera.ReasonAmbiguousLineItems {
		return nil, NewClientError(409, "more than one Archera line item matches this recommendation")
	}
	return dto, nil
}
