package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

func (r *Router) listPlansHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listPlans(ctx, req, req.QueryStringParameters)
}

func (r *Router) createPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createPlan(ctx, req)
}

func (r *Router) getPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPlan(ctx, req, params["id"])
}

func (r *Router) updatePlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updatePlan(ctx, req, params["id"])
}

func (r *Router) patchPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.patchPlan(ctx, req, params["id"])
}

func (r *Router) deletePlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deletePlan(ctx, req, params["id"])
}

func (r *Router) getPlannedPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPlannedPurchases(ctx, req)
}

func (r *Router) pausePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.pausePlannedPurchase(ctx, req, params["id"])
}

func (r *Router) resumePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resumePlannedPurchase(ctx, req, params["id"])
}

func (r *Router) runPlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.runPlannedPurchase(ctx, req, params["id"])
}

func (r *Router) deletePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deletePlannedPurchase(ctx, req, params["id"])
}
