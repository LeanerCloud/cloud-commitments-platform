package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

// Handler wrappers that adapt the old handlers to the new RouteHandler signature

func (r *Router) dashboardSummaryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getDashboardSummary(ctx, req, req.QueryStringParameters)
}

func (r *Router) upcomingPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getUpcomingPurchases(ctx, req)
}

func (r *Router) getConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getConfig(ctx, req)
}

func (r *Router) updateConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateConfig(ctx, req)
}

func (r *Router) getServiceConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getServiceConfig(ctx, req, params["id"])
}

func (r *Router) updateServiceConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateServiceConfig(ctx, req, params["id"])
}

func (r *Router) commitmentOptionsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCommitmentOptions(ctx)
}

func (r *Router) getRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendations(ctx, req, req.QueryStringParameters)
}

func (r *Router) refreshRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.postRefreshRecommendations(ctx, req)
}

func (r *Router) getInsuranceStatusHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getInsuranceStatus(ctx, req)
}

func (r *Router) getInsuranceComparisonHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getInsuranceComparison(ctx, req)
}

func (r *Router) getRecommendationsFreshnessHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendationsFreshness(ctx, req)
}

func (r *Router) getRecommendationDetailHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendationDetail(ctx, req, params["id"])
}

func (r *Router) getHistoryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistory(ctx, req, req.QueryStringParameters)
}

func (r *Router) getHistoryAnalyticsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistoryAnalytics(ctx, req, req.QueryStringParameters)
}

func (r *Router) getHistoryBreakdownHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistoryBreakdown(ctx, req, req.QueryStringParameters)
}

func (r *Router) getAnalyticsTrendsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getAnalyticsTrends(ctx, req, req.QueryStringParameters)
}

func (r *Router) triggerAnalyticsCollectionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.triggerAnalyticsCollection(ctx, req, req.QueryStringParameters)
}
