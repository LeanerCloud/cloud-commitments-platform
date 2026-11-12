package api

import (
	"context"

	"github.com/aws/aws-lambda-go/events"
)

func (r *Router) loginHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.login(ctx, req)
}

func (r *Router) logoutHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.logout(ctx, req)
}

func (r *Router) getCurrentUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCurrentUser(ctx, req)
}

func (r *Router) getCurrentUserPermissionsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCurrentUserPermissions(ctx, req)
}

func (r *Router) checkAdminExistsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.checkAdminExists(ctx, req)
}

func (r *Router) setupAdminHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.setupAdmin(ctx, req)
}

func (r *Router) forgotPasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.forgotPassword(ctx, req)
}

func (r *Router) resetPasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resetPassword(ctx, req)
}

func (r *Router) resetPasswordStatusHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resetPasswordStatus(ctx, req)
}

func (r *Router) updateProfileHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateProfile(ctx, req)
}

func (r *Router) changePasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.changePassword(ctx, req)
}

func (r *Router) mfaSetupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaSetup(ctx, req)
}

func (r *Router) mfaEnableHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaEnable(ctx, req)
}

func (r *Router) mfaDisableHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaDisable(ctx, req)
}

func (r *Router) mfaRegenerateRecoveryCodesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaRegenerateRecoveryCodes(ctx, req)
}

func (r *Router) listAPIKeysHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listAPIKeys(ctx, req)
}

// listAPIKeysUsageStatsHandler lives in handler_apikeys_usage.go alongside
// its handler so the usage-stats feature stays in its own bounded-context
// file instead of growing this already-oversized router further.

func (r *Router) createAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createAPIKey(ctx, req)
}

func (r *Router) revokeAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.revokeAPIKey(ctx, req)
}

func (r *Router) deleteAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteAPIKey(ctx, req)
}

func (r *Router) listUsersHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listUsers(ctx, req)
}

func (r *Router) createUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createUser(ctx, req)
}

func (r *Router) getUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getUser(ctx, req, params["id"])
}

func (r *Router) updateUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateUser(ctx, req, params["id"])
}

func (r *Router) deleteUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteUser(ctx, req, params["id"])
}

func (r *Router) listGroupsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listGroups(ctx, req)
}

func (r *Router) createGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createGroup(ctx, req)
}

func (r *Router) getGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getGroup(ctx, req, params["id"])
}

func (r *Router) updateGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateGroup(ctx, req, params["id"])
}

func (r *Router) deleteGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteGroup(ctx, req, params["id"])
}
