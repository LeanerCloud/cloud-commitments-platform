package api

import (
	"context"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// Cloud Account route wrappers.

func (r *Router) listAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listAccounts(ctx, req)
}

func (r *Router) listAccountsMinimalHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listAccountsMinimal(ctx, req)
}

func (r *Router) createAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.createAccount(ctx, req)
}

func (r *Router) createSelfAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.createSelfAccount(ctx, req)
}

func (r *Router) discoverOrgAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.discoverOrgAccounts(ctx, req)
}

func (r *Router) getAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getAccount(ctx, req, params["id"])
}

// exportAccountServiceOverridesHandler handles GET /api/accounts/service-overrides/export.
func (r *Router) exportAccountServiceOverridesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.exportAccountServiceOverrides(ctx, req)
}

// importAccountServiceOverridesHandler handles POST /api/accounts/service-overrides/import.
func (r *Router) importAccountServiceOverridesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.importAccountServiceOverrides(ctx, req)
}

// updateAccountOrServiceOverrideHandler handles PUT /api/accounts/:id and
// PUT /api/accounts/:id/service-overrides/:provider/:service.
// The router puts everything after /api/accounts/ into params["id"].
func (r *Router) updateAccountOrServiceOverrideHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if strings.Contains(params["id"], "/service-overrides/") {
		return r.h.saveAccountServiceOverride(ctx, req, params["id"])
	}
	return r.h.updateAccount(ctx, req, params["id"])
}

// deleteAccountOrServiceOverrideHandler handles DELETE /api/accounts/:id and
// DELETE /api/accounts/:id/service-overrides/:provider/:service.
func (r *Router) deleteAccountOrServiceOverrideHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if strings.Contains(params["id"], "/service-overrides/") {
		return r.h.deleteAccountServiceOverride(ctx, req, params["id"])
	}
	return r.h.deleteAccount(ctx, req, params["id"])
}

func (r *Router) saveAccountCredentialsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.saveAccountCredentials(ctx, req, params["id"])
}

func (r *Router) testAccountCredentialsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.testAccountCredentials(ctx, req, params["id"])
}

func (r *Router) listAccountServiceOverridesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listAccountServiceOverrides(ctx, req, params["id"])
}

// Plan ↔ Account association wrappers.

func (r *Router) getFederationIaCHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getFederationIaC(ctx, req)
}

// Account self-registration wrappers.

func (r *Router) submitRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.submitRegistration(ctx, req)
}

func (r *Router) getRegistrationStatusHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRegistrationStatus(ctx, params["id"])
}

func (r *Router) listRegistrationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listRegistrations(ctx, req)
}

func (r *Router) getRegistrationHandler(ctx context.Context, _ *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRegistration(ctx, params["id"])
}

func (r *Router) approveRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.approveRegistration(ctx, req, params["id"])
}

func (r *Router) rejectRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.rejectRegistration(ctx, req, params["id"])
}

func (r *Router) deleteRegistrationHandler(ctx context.Context, _ *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteRegistration(ctx, params["id"])
}

func (r *Router) listPlanAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listPlanAccounts(ctx, req, params["id"])
}

func (r *Router) setPlanAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.setPlanAccounts(ctx, req, params["id"])
}
