package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

func (r *Router) healthCheckHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.GetHealth(ctx)
}

func (r *Router) getPublicInfoHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPublicInfo(ctx, req)
}

func (r *Router) getVersionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getVersion(ctx, req), nil
}

func (r *Router) getDeploymentInfoHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getDeploymentInfo(ctx, req)
}

func (r *Router) docsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.docsHandler(ctx, req, params)
}

func (r *Router) unsubscribeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.unsubscribeHandler(ctx, req, params)
}
