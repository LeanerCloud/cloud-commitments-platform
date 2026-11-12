package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

// Commitment Laddering route wrappers (issue #1336).

func (r *Router) getLadderConfigsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getLadderConfigs(ctx, req)
}

func (r *Router) upsertLadderConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.upsertLadderConfig(ctx, req)
}

func (r *Router) getLadderTimelineHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getLadderTimeline(ctx, req)
}

func (r *Router) getLadderTimelineRunsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getLadderTimelineRuns(ctx, req)
}

func (r *Router) amendLadderTrancheHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.amendLadderTranche(ctx, req, params["id"])
}

func (r *Router) resolveLadderTrancheHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resolveLadderTranche(ctx, req, params["id"])
}
