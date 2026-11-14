package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

func (r *Router) listInventoryCommitmentsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listActiveCommitments(ctx, req, req.QueryStringParameters)
}

func (r *Router) getCoverageBreakdownHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCoverageBreakdown(ctx, req, req.QueryStringParameters)
}

func (r *Router) listExchangeableAzureRIsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listExchangeableAzureRIs(ctx, req)
}

func (r *Router) getAzureCompatibleOfferingsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getAzureCompatibleOfferings(ctx, req)
}

func (r *Router) executeAzureExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.executeAzureExchange(ctx, req)
}

func (r *Router) listConvertibleRIsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listConvertibleRIs(ctx, req)
}

func (r *Router) listTargetOfferingsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listTargetOfferings(ctx, req)
}

func (r *Router) getRIUtilizationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIUtilization(ctx, req)
}

func (r *Router) getReshapeRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getReshapeRecommendations(ctx, req)
}

func (r *Router) getExchangeQuoteHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getExchangeQuote(ctx, req)
}

func (r *Router) executeExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.executeExchange(ctx, req)
}

func (r *Router) getRIExchangeConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIExchangeConfig(ctx, req)
}

func (r *Router) updateRIExchangeConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateRIExchangeConfig(ctx, req)
}

func (r *Router) getRIExchangeHistoryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIExchangeHistory(ctx, req)
}

func (r *Router) approveRIExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimitStrict(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	return r.h.approveRIExchange(ctx, req, params["id"], req.QueryStringParameters["token"])
}

func (r *Router) rejectRIExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimitStrict(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	return r.h.rejectRIExchange(ctx, params["id"], req.QueryStringParameters["token"])
}

func (r *Router) listRIExchangeDirectExecutionsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listRIExchangeDirectExecutions(ctx, req)
}

func (r *Router) resolveRIExchangeDirectExecutionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resolveRIExchangeDirectExecution(ctx, req, params["id"])
}

func (r *Router) resolveRIExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resolveRIExchange(ctx, req, params["id"])
}
