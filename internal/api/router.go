package api

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/events"
)

// RouteHandler is a function that handles a matched route.
type RouteHandler func(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error)

// AuthLevel controls how Router.Route() enforces authentication.
// The Auth field on Route is MANDATORY — NewRouter panics at startup
// if any registered route leaves it at the zero value. See the const
// block below for the AuthAdmin / AuthUser / AuthPublic options.
//
// Router.Route enforces these levels itself as a defense-in-depth check,
// in addition to the authentication middleware that runs
// earlier in the request pipeline. If middleware ordering ever changes or
// a new route bypasses validateSecurityContext, the router-level enforcement
// still rejects unauthorized requests.
type AuthLevel int

const (
	// authUnset is the zero value. NewRouter panics at startup if any
	// route is registered with this value, forcing every route author
	// to make an explicit choice between AuthAdmin / AuthUser / AuthPublic.
	// Prior versions of this file used AuthAdmin as the iota zero with a
	// "secure by default" rationale, but that pattern silently locked
	// every non-admin user out of any new read-only endpoint a developer
	// added without thinking about it (issue surfaced in 2026-05-14
	// readonly-role lockout). Making the field mandatory at the route
	// declaration site removes the failure mode entirely.
	authUnset AuthLevel = iota
	// AuthAdmin requires admin role (admin API key or admin bearer-token
	// session). Enforced by Router.Route via h.requireAdmin.
	AuthAdmin
	// AuthUser requires any authenticated user (admin API key, user API
	// key, or any valid bearer-token session). Use for read-only views
	// and self-service endpoints (logout, profile, API key management).
	// Enforced by Router.Route via h.requireAuth.
	AuthUser
	// AuthPublic requires no authentication. Must also be listed in
	// isPublicEndpoint() so the middleware skips its auth/CSRF checks.
	AuthPublic
)

// Route defines a routing rule.
type Route struct {
	Handler    RouteHandler
	ExactPath  string
	PathPrefix string
	PathSuffix string
	Method     string
	Auth       AuthLevel
}

// Router manages request routing.
type Router struct {
	h      *Handler
	routes []Route
}

// NewRouter creates a new router with all routes configured.
// Panics on startup if any route was registered with an unset Auth
// field — see the AuthLevel doc for the rationale (forces every route
// author to declare a level explicitly so a missed field can't silently
// inherit an over- or under-permissive default).
func NewRouter(h *Handler) *Router {
	r := &Router{h: h}
	r.registerRoutes()
	validateRoutes(r.routes)
	return r
}

// validateRoutes panics if any route in the slice has Auth == authUnset.
// Extracted from NewRouter so tests can exercise the live validator
// directly rather than replaying the check inline — without this split,
// a regression that removes the call from NewRouter wouldn't be caught
// by a unit test (the inline replay would keep passing in isolation).
// See CR feedback on PR #364.
func validateRoutes(routes []Route) {
	for i, route := range routes {
		if route.Auth == authUnset {
			panic(fmt.Sprintf(
				"router: route %d (%s%s%s %q) has unset Auth field — every route must declare AuthAdmin, AuthUser, or AuthPublic explicitly",
				i, route.PathPrefix, route.ExactPath, route.PathSuffix, route.Method,
			))
		}
	}
}

// registerRoutes sets up all application routes.
func (r *Router) registerRoutes() {
	r.routes = []Route{
		// Dashboard endpoints — read-only views available to any signed-in
		// user (admin / user / readonly). The page itself is the landing
		// screen post-login and was locking out non-admin roles when these
		// routes defaulted to AuthAdmin.
		{ExactPath: "/api/dashboard/summary", Method: "GET", Handler: r.dashboardSummaryHandler, Auth: AuthUser},
		{ExactPath: "/api/dashboard/upcoming", Method: "GET", Handler: r.upcomingPurchasesHandler, Auth: AuthUser},

		// Configuration endpoints — GET is AuthUser (settings are app config,
		// not secrets — credentials live elsewhere), PUT remains AuthAdmin.
		{ExactPath: "/api/config", Method: "GET", Handler: r.getConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/config", Method: "PUT", Handler: r.updateConfigHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/config/service/", Method: "GET", Handler: r.getServiceConfigHandler, Auth: AuthUser},
		{PathPrefix: "/api/config/service/", Method: "PUT", Handler: r.updateServiceConfigHandler, Auth: AuthAdmin},

		// Dynamically-probed AWS commitment-option combos. Non-admin reads
		// (AuthUser) — data is public-ish (hardcoded in the frontend today)
		// but we don't expose it unauthenticated.
		{ExactPath: "/api/commitment-options", Method: "GET", Handler: r.commitmentOptionsHandler, Auth: AuthUser},

		// Recommendations endpoints. The /:id/detail suffix route uses
		// the same prefix+suffix pattern as /api/plans/{id}/purchases
		// below — extractParams strips both ends to recover the id.
		// AuthUser for the GETs so readonly/user roles can browse the
		// recommendation feed (handlers still scope rows by account
		// permission grants).
		{ExactPath: "/api/recommendations", Method: "GET", Handler: r.getRecommendationsHandler, Auth: AuthUser},
		{ExactPath: "/api/insurance/comparison", Method: "GET", Handler: r.getInsuranceComparisonHandler, Auth: AuthUser},
		{ExactPath: "/api/insurance/status", Method: "GET", Handler: r.getInsuranceStatusHandler, Auth: AuthUser},
		{ExactPath: "/api/recommendations/freshness", Method: "GET", Handler: r.getRecommendationsFreshnessHandler, Auth: AuthUser},
		// AuthUser: any signed-in user can trigger refresh; the handler
		// then enforces requirePermission(view, recommendations) so
		// users without that permission are rejected at the handler
		// (admin-only would block view-only roles that legitimately
		// need to refresh the data they're allowed to see).
		{ExactPath: "/api/recommendations/refresh", Method: "POST", Handler: r.refreshRecommendationsHandler, Auth: AuthUser},
		{PathPrefix: "/api/recommendations/", PathSuffix: "/detail", Method: "GET", Handler: r.getRecommendationDetailHandler, Auth: AuthUser},

		// Purchase plans endpoints — GETs are AuthUser (anyone signed in
		// can see plans they're entitled to). Mutating routes are also
		// AuthUser now (PR-A of #660): the router-level gate drops to
		// "must be signed in" and each handler calls requirePermission with
		// the specific verb+resource so the per-handler check is the real
		// gate. Without the flip the requirePermission call inside the
		// handler is never reached for non-admins.
		{ExactPath: "/api/plans", Method: "GET", Handler: r.listPlansHandler, Auth: AuthUser},
		{ExactPath: "/api/plans", Method: "POST", Handler: r.createPlanHandler, Auth: AuthUser},
		// Suffix routes must precede generic prefix routes so they are matched first.
		{PathPrefix: "/api/plans/", PathSuffix: "/purchases", Method: "POST", Handler: r.createPlannedPurchasesHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", PathSuffix: "/accounts", Method: "GET", Handler: r.listPlanAccountsHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", PathSuffix: "/accounts", Method: "PUT", Handler: r.setPlanAccountsHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "GET", Handler: r.getPlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "PUT", Handler: r.updatePlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "PATCH", Handler: r.patchPlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "DELETE", Handler: r.deletePlanHandler, Auth: AuthUser},

		// Purchase actions. AuthUser so requirePermission inside each
		// handler is the real gate (PR-A of #660 — same rationale as plans
		// above). Approve + Cancel stay AuthPublic (token-based paths that
		// are deliberately unauthenticated).
		{ExactPath: "/api/purchases/execute", Method: "POST", Handler: r.executePurchaseHandler, Auth: AuthUser},
		// Approve and Cancel are POST only: a GET must never change purchase
		// state, or a mail scanner prefetching the link would approve or
		// cancel it. The emails link to the SPA deep links
		// /purchases/{approve,cancel}/{id}?token=..., which POST the token
		// after the user confirms (issues #549, #578).
		// Read-only details for the approval deep link's confirm modal
		// (issue #529). Must precede the approve route, which shares its
		// prefix. POST so the token travels in the body (issue #398).
		{PathPrefix: "/api/purchases/approve/", PathSuffix: "/details", Method: "POST", Handler: r.approvalDetailsHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/approve/", Method: "POST", Handler: r.approvePurchaseHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/cancel/", Method: "POST", Handler: r.cancelPurchaseHandler, Auth: AuthPublic},
		// Revoke a completed purchase via email one-click link (issue #291).
		// AuthPublic + token-based auth pattern: the token is embedded in the
		// post-execution notification email's one-click link. GET renders a
		// confirmation form; POST executes the revocation.
		{PathPrefix: "/api/purchases/revoke/", Method: "GET", Handler: r.revokeViaEmailTokenHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/revoke/", Method: "POST", Handler: r.revokeViaEmailTokenHandler, Auth: AuthPublic},
		// Retry a failed purchase execution (issue #47). Session-authed
		// only — the original failed row's email-token has already been
		// consumed/expired, so there is no token-mode dispatch here.
		// AuthUser gates "must be signed in"; the handler then enforces
		// the retry-any/retry-own RBAC matrix.
		{PathPrefix: "/api/purchases/retry/", Method: "POST", Handler: r.retryPurchaseHandler, Auth: AuthUser},
		// Revoke a completed purchase within the provider's free-cancel window
		// (issue #290). Session-authed only; the handler enforces the
		// revoke-any/revoke-own RBAC matrix and the per-provider window check.
		// AuthUser: router-level gate; per-handler requirePermission is the
		// real security boundary.
		{PathPrefix: "/api/purchases/", PathSuffix: "/revoke", Method: "POST", Handler: r.revokePurchaseHandler, Auth: AuthUser},
		// Settle a failed plan execution whose outcome is unknown (issue #999),
		// so it stops blocking the delete of its editor-managed plan. The
		// handler enforces execute:purchases, account access and constraints.
		{PathPrefix: "/api/purchases/", PathSuffix: "/settle", Method: "POST", Handler: r.settleExecutionHandler, Auth: AuthUser},
		// GET /api/purchases/{id}/revoke/calculate: returns the Azure refund quote
		// (amount + currency) for the two-step quote-then-confirm revoke UX
		// (issue #290 Finding #4). No state mutation; result used to populate
		// expected_refund_amount + expected_refund_currency in the POST /revoke body.
		{PathPrefix: "/api/purchases/", PathSuffix: "/revoke/calculate", Method: "GET", Handler: r.calculateRevokeHandler, Auth: AuthUser},

		// RI Marketplace listing (issue #292). Session-authed only; the
		// handler enforces the sell-any/sell-own RBAC matrix and checks
		// that the purchase_history row has offering_class='standard'.
		{PathPrefix: "/api/purchases/", PathSuffix: "/marketplace-list", Method: "POST", Handler: r.marketplaceListHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/", PathSuffix: "/marketplace-cancel", Method: "POST", Handler: r.marketplaceCancelHandler, Auth: AuthUser},

		// Planned purchases endpoints (must come before generic /api/purchases/{id}).
		// All now AuthUser (PR-A of #660): handler-level requirePermission
		// is the actual gate for pause/resume/run/delete.
		{ExactPath: "/api/purchases/planned", Method: "GET", Handler: r.getPlannedPurchasesHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/pause", Method: "POST", Handler: r.pausePlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/resume", Method: "POST", Handler: r.resumePlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/run", Method: "POST", Handler: r.runPlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", Method: "DELETE", Handler: r.deletePlannedPurchaseHandler, Auth: AuthUser},

		// Generic purchase details (must come after more specific routes)
		// — read-only; AuthUser so the history detail view works for everyone.
		{PathPrefix: "/api/purchases/", Method: "GET", Handler: r.getPurchaseDetailsHandler, Auth: AuthUser},

		// History endpoints — read-only views available to any signed-in user.
		{ExactPath: "/api/history", Method: "GET", Handler: r.getHistoryHandler, Auth: AuthUser},
		{ExactPath: "/api/history/analytics", Method: "GET", Handler: r.getHistoryAnalyticsHandler, Auth: AuthUser},
		{ExactPath: "/api/history/breakdown", Method: "GET", Handler: r.getHistoryBreakdownHandler, Auth: AuthUser},

		// Savings-snapshot time-series (Trends view) — scoped to allowed_accounts.
		{ExactPath: "/api/analytics/trends", Method: "GET", Handler: r.getAnalyticsTrendsHandler, Auth: AuthUser},

		// Analytics collection endpoint
		{ExactPath: "/api/analytics/collect", Method: "POST", Handler: r.triggerAnalyticsCollectionHandler, Auth: AuthAdmin},

		// Auth endpoints
		{ExactPath: "/api/auth/login", Method: "POST", Handler: r.loginHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/logout", Method: "POST", Handler: r.logoutHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/me", Method: "GET", Handler: r.getCurrentUserHandler, Auth: AuthUser},
		// Effective permission set for the authenticated user (issue #917).
		// Must be listed before /api/auth/me so an exact-path match fires
		// rather than a future prefix match. Auth level mirrors /api/auth/me.
		{ExactPath: "/api/auth/me/permissions", Method: "GET", Handler: r.getCurrentUserPermissionsHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/check-admin", Method: "GET", Handler: r.checkAdminExistsHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/setup-admin", Method: "POST", Handler: r.setupAdminHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/forgot-password", Method: "POST", Handler: r.forgotPasswordHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/reset-password", Method: "POST", Handler: r.resetPasswordHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/reset-password/status", Method: "GET", Handler: r.resetPasswordStatusHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/profile", Method: "PUT", Handler: r.updateProfileHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/change-password", Method: "POST", Handler: r.changePasswordHandler, Auth: AuthUser},
		// MFA enrollment / lifecycle (issue #497). All require an
		// authenticated session; setup + disable additionally require
		// a fresh password re-verify in the body.
		{ExactPath: "/api/auth/mfa/setup", Method: "POST", Handler: r.mfaSetupHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/enable", Method: "POST", Handler: r.mfaEnableHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/disable", Method: "POST", Handler: r.mfaDisableHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/regenerate-recovery-codes", Method: "POST", Handler: r.mfaRegenerateRecoveryCodesHandler, Auth: AuthUser},

		// API Key endpoints (self-service — any authenticated user).
		// Usage-stats route is declared before the generic prefix
		// matches so the more-specific path wins.
		{ExactPath: "/api/api-keys/usage-stats", Method: "GET", Handler: r.listAPIKeysUsageStatsHandler, Auth: AuthUser},
		{ExactPath: "/api/api-keys", Method: "GET", Handler: r.listAPIKeysHandler, Auth: AuthUser},
		{ExactPath: "/api/api-keys", Method: "POST", Handler: r.createAPIKeyHandler, Auth: AuthUser},
		{PathPrefix: "/api/api-keys/", PathSuffix: "/revoke", Method: "POST", Handler: r.revokeAPIKeyHandler, Auth: AuthUser},
		{PathPrefix: "/api/api-keys/", Method: "DELETE", Handler: r.deleteAPIKeyHandler, Auth: AuthUser},

		// User management endpoints
		{ExactPath: "/api/users", Method: "GET", Handler: r.listUsersHandler, Auth: AuthAdmin},
		{ExactPath: "/api/users", Method: "POST", Handler: r.createUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "GET", Handler: r.getUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "PUT", Handler: r.updateUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "DELETE", Handler: r.deleteUserHandler, Auth: AuthAdmin},

		// Cloud Account endpoints (more-specific suffix routes must precede generic prefix routes).
		// GETs are AuthUser — account metadata is needed by every dashboard
		// page; sensitive credential bodies are redacted in the handler's
		// response shape, not gated at the route layer.
		{ExactPath: "/api/accounts/discover-org", Method: "POST", Handler: r.discoverOrgAccountsHandler, Auth: AuthAdmin},
		{ExactPath: "/api/accounts/self", Method: "POST", Handler: r.createSelfAccountHandler, Auth: AuthAdmin},
		// Minimal-disclosure accounts list for the global filter + plan-target
		// prefill (issues #949/#951). Gated in-handler on view:recommendations so
		// Standard / Read-Only users can populate the dropdown; the full account
		// object stays behind view:accounts on GET /api/accounts below. Must
		// precede the generic "/api/accounts/" GET prefix route.
		{ExactPath: "/api/accounts/list", Method: "GET", Handler: r.listAccountsMinimalHandler, Auth: AuthUser},
		// Override export/import (issue #9). Exact paths, so they must precede
		// the generic "/api/accounts/" prefix routes. Gated in-handler on
		// view:accounts (export) and update:accounts (import), scoped to the
		// caller's allowed accounts.
		{ExactPath: "/api/accounts/service-overrides/export", Method: "GET", Handler: r.exportAccountServiceOverridesHandler, Auth: AuthUser},
		{ExactPath: "/api/accounts/service-overrides/import", Method: "POST", Handler: r.importAccountServiceOverridesHandler, Auth: AuthAdmin},
		{ExactPath: "/api/accounts", Method: "GET", Handler: r.listAccountsHandler, Auth: AuthUser},
		{ExactPath: "/api/accounts", Method: "POST", Handler: r.createAccountHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/credentials", Method: "POST", Handler: r.saveAccountCredentialsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/test", Method: "POST", Handler: r.testAccountCredentialsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/service-overrides", Method: "GET", Handler: r.listAccountServiceOverridesHandler, Auth: AuthUser},
		{PathPrefix: "/api/accounts/", Method: "PUT", Handler: r.updateAccountOrServiceOverrideHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", Method: "DELETE", Handler: r.deleteAccountOrServiceOverrideHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", Method: "GET", Handler: r.getAccountHandler, Auth: AuthUser},

		// Group management endpoints — list/get readable by any signed-in
		// user (group display name is needed when rendering user lists);
		// CUD stays AuthAdmin.
		{ExactPath: "/api/groups", Method: "GET", Handler: r.listGroupsHandler, Auth: AuthUser},
		{ExactPath: "/api/groups", Method: "POST", Handler: r.createGroupHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/groups/", Method: "GET", Handler: r.getGroupHandler, Auth: AuthUser},
		{PathPrefix: "/api/groups/", Method: "PUT", Handler: r.updateGroupHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/groups/", Method: "DELETE", Handler: r.deleteGroupHandler, Auth: AuthAdmin},

		// Inventory & Coverage endpoints. Per-commitment list view for
		// the Inventory & Coverage → Active commitments sub-tab (issue
		// #340 deferred sub-task). AuthUser gates the read; the
		// handler also filters by the session's allowed_accounts list
		// so a restricted-access user sees only their entitled rows.
		{ExactPath: "/api/inventory/commitments", Method: "GET", Handler: r.listInventoryCommitmentsHandler, Auth: AuthUser},
		// Per-provider, per-service coverage breakdown for the Coverage
		// sub-tab (issue #754). AuthUser + allowed_accounts filter applied
		// inside the handler, matching commitments endpoint precedent.
		{ExactPath: "/api/inventory/coverage", Method: "GET", Handler: r.getCoverageBreakdownHandler, Auth: AuthUser},

		// RI Exchange endpoints — GETs are AuthUser (Convertible RIs,
		// Reshape Recommendations, Exchange History pages all need this).
		// quote / execute / config writes are now AuthUser (PR-A of #660):
		// each handler calls requirePermission so the per-handler check is
		// the real gate. approve/reject stay AuthPublic (token-based).
		{ExactPath: "/api/ri-exchange/azure-instances", Method: "GET", Handler: r.listExchangeableAzureRIsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/azure-instances/compatible-offerings", Method: "POST", Handler: r.getAzureCompatibleOfferingsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/azure-instances/exchange", Method: "POST", Handler: r.executeAzureExchangeHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/instances", Method: "GET", Handler: r.listConvertibleRIsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/target-offerings", Method: "GET", Handler: r.listTargetOfferingsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/utilization", Method: "GET", Handler: r.getRIUtilizationHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/reshape-recommendations", Method: "GET", Handler: r.getReshapeRecommendationsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/quote", Method: "POST", Handler: r.getExchangeQuoteHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/execute", Method: "POST", Handler: r.executeExchangeHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/config", Method: "GET", Handler: r.getRIExchangeConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/config", Method: "PUT", Handler: r.updateRIExchangeConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/history", Method: "GET", Handler: r.getRIExchangeHistoryHandler, Auth: AuthUser},
		{PathPrefix: "/api/ri-exchange/approve/", Method: "POST", Handler: r.approveRIExchangeHandler, Auth: AuthPublic},
		{PathPrefix: "/api/ri-exchange/reject/", Method: "POST", Handler: r.rejectRIExchangeHandler, Auth: AuthPublic},
		// Operator resolve of a stale processing exchange (#692). AuthUser, so the
		// outer middleware enforces the session and CSRF.
		{PathPrefix: "/api/ri-exchange/resolve/", Method: "POST", Handler: r.resolveRIExchangeHandler, Auth: AuthUser},
		// Operator list of AWS direct-execute rows that still hold their source RIs (#1836).
		{ExactPath: "/api/ri-exchange/direct-executions", Method: "GET", Handler: r.listRIExchangeDirectExecutionsHandler, Auth: AuthUser},
		// Operator resolve of one such row. AuthUser, so the outer middleware
		// enforces the session and CSRF.
		{PathPrefix: "/api/ri-exchange/direct-executions/", PathSuffix: "/resolve", Method: "POST", Handler: r.resolveRIExchangeDirectExecutionHandler, Auth: AuthUser},

		// Commitment Laddering endpoints (flag-gated default-off, issue #1336).
		// GET returns all per-account ladder configs; PUT inserts or updates one.
		// Both routes require update:config / view:config (checked inside the
		// handler), consistent with the RI Exchange config precedent.
		{ExactPath: "/api/ladder/configs", Method: "GET", Handler: r.getLadderConfigsHandler, Auth: AuthUser},
		{ExactPath: "/api/ladder/configs", Method: "PUT", Handler: r.upsertLadderConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/ladder/runs", Method: "GET", Handler: r.getLadderTimelineRunsHandler, Auth: AuthUser},
		{ExactPath: "/api/ladder/tranches", Method: "GET", Handler: r.getLadderTimelineHandler, Auth: AuthUser},
		{PathPrefix: "/api/ladder/tranches/", PathSuffix: "/resolve", Method: "POST", Handler: r.resolveLadderTrancheHandler, Auth: AuthUser},
		{PathPrefix: "/api/ladder/tranches/", Method: "PATCH", Handler: r.amendLadderTrancheHandler, Auth: AuthUser},

		// Notification one-click unsubscribe (RFC 8058). AuthPublic: the signed
		// token in the query string is the credential (mirrors approve/cancel).
		{ExactPath: "/api/notifications/unsubscribe", Method: "GET", Handler: r.unsubscribeHandler, Auth: AuthPublic},
		{ExactPath: "/api/notifications/unsubscribe", Method: "POST", Handler: r.unsubscribeHandler, Auth: AuthPublic},

		// Account self-registration (public, called by Terraform during federation IaC apply)
		{ExactPath: "/api/register", Method: "POST", Handler: r.submitRegistrationHandler, Auth: AuthPublic},
		{PathPrefix: "/api/register/", Method: "GET", Handler: r.getRegistrationStatusHandler, Auth: AuthPublic},

		// Admin registration management (suffix routes before generic prefix)
		{ExactPath: "/api/registrations", Method: "GET", Handler: r.listRegistrationsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", PathSuffix: "/approve", Method: "POST", Handler: r.approveRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", PathSuffix: "/reject", Method: "POST", Handler: r.rejectRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", Method: "DELETE", Handler: r.deleteRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", Method: "GET", Handler: r.getRegistrationHandler, Auth: AuthAdmin},

		// Federation IaC download endpoint (requires auth — templates embed the
		// host AWS account ID via STS, which we don't want to leak to unauth'd
		// reconnaissance. Only admins have access; view:accounts is the gate.)
		{ExactPath: "/api/federation/iac", Method: "GET", Handler: r.getFederationIaCHandler, Auth: AuthUser},

		// Note: /.well-known/openid-configuration and /.well-known/jwks.json
		// are served by the transport layer (lambda.go / http.go) which
		// calls api.Handler.HandleOIDC before dispatching to this router.
		// That keeps the OIDC endpoints out of the API surface entirely —
		// no auth middleware, no CSRF, no CORS layer — and avoids special
		// cases in isPublicEndpoint / isStaticPath.

		// Health check (both root and /api paths)
		{ExactPath: "/health", Handler: r.healthCheckHandler, Auth: AuthPublic},
		{ExactPath: "/api/health", Handler: r.healthCheckHandler, Auth: AuthPublic},

		// Public info endpoint (unauthenticated — version + admin_exists only)
		{ExactPath: "/api/info", Method: "GET", Handler: r.getPublicInfoHandler, Auth: AuthPublic},
		// Deployment info endpoint (authenticated — API key secret URL + AWS account ID, #633)
		{ExactPath: "/api/info/deployment", Method: "GET", Handler: r.getDeploymentInfoHandler, Auth: AuthUser},

		// Build-version endpoint — public, returns only version / git SHA /
		// build time (no account IDs, ARNs, or secrets) so an operator can
		// curl a running environment and compare the SHA to the branch HEAD
		// to detect deploy-lag.
		{ExactPath: "/version", Method: "GET", Handler: r.getVersionHandler, Auth: AuthPublic},

		// API documentation (Swagger UI + raw spec)
		{PathPrefix: "/api/docs", Method: "GET", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/api/docs", Method: "HEAD", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/docs", Method: "GET", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/docs", Method: "HEAD", Handler: r.docsHandler, Auth: AuthPublic},
	}
}
