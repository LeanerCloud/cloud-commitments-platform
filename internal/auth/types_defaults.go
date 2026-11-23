package auth

// DefaultAdminPermissions returns full admin permissions.
func DefaultAdminPermissions() []Permission {
	return []Permission{
		{Action: ActionAdmin, Resource: ResourceAll},
	}
}

// DefaultUserPermissions returns standard user permissions.
func DefaultUserPermissions() []Permission {
	return []Permission{
		{Action: ActionView, Resource: ResourceRecommendations},
		{Action: ActionView, Resource: ResourcePlans},
		{Action: ActionView, Resource: ResourcePurchases},
		{Action: ActionView, Resource: ResourceHistory},
		{Action: ActionCreate, Resource: ResourcePlans},
		{Action: ActionUpdate, Resource: ResourcePlans},
		// delete:plans — every authenticated user can delete plans they
		// have access to (PR-A of #660). The handler still requires
		// requirePermission("delete", "plans") and the plan-access scope
		// check, so only plans in the user's allowed accounts are
		// reachable.
		{Action: ActionDelete, Resource: ResourcePlans},
		// update:purchases — every authenticated user can pause, resume,
		// and update planned purchase executions (PR-A of #660). The
		// handler still requires requirePermission("update", "purchases")
		// and the execution-access scope check.
		{Action: ActionUpdate, Resource: ResourcePurchases},
		// cancel-own:purchases — every authenticated user can cancel
		// pending purchase executions they created themselves (issue #46).
		// The handler still requires the execution to be in a cancellable
		// state (pending/notified) and the creator UUID to match the
		// session UserID before honoring the request.
		{Action: ActionCancelOwn, Resource: ResourcePurchases},
		// retry-own:purchases — every authenticated user can retry
		// failed purchase executions they created themselves (issue #47).
		// The handler still requires the execution to be in `failed`
		// state, the creator UUID to match the session UserID, the
		// failure reason not to match the persistent-misconfig list,
		// and the retry-attempt counter on the chain to be below the
		// soft-block threshold (overridable with ?force=true).
		{Action: ActionRetryOwn, Resource: ResourcePurchases},
		// approve-own:purchases is intentionally NOT granted here (issue #1407).
		// Four-eyes principle: the same user who submits a purchase must NOT
		// be able to approve it by default. The approve-own verb must be
		// explicitly granted to a custom group when self-approval is a
		// deliberate policy choice for that group. Roles without an explicit
		// approve-own or approve-any grant cannot approve any purchase,
		// including their own.
		// revoke-own:purchases — every authenticated user can revoke completed
		// purchases they created themselves while still within the provider's
		// free-cancel window (issue #290). The handler verifies the window has
		// not closed, the provider supports a direct revocation API, and the
		// creator UUID matches. Legacy rows with NULL creator are out of reach
		// for non-admins (email-token paths have no revocation escape hatch).
		{Action: ActionRevokeOwn, Resource: ResourcePurchases},
		// sell-own:purchases — every authenticated user can list Standard
		// RIs they purchased themselves on the AWS Marketplace (issue #292).
		// The handler still requires the purchase_history row to carry
		// offering_class = 'standard' and the cloud account to match the
		// row's cloud_account_id before creating the listing.
		{Action: ActionSellOwn, Resource: ResourcePurchases},
		// view:config — every authenticated user can read the global settings
		// (providers, default term/payment/coverage, etc.) as read-only. The
		// GET /api/config and GET /api/ri-exchange/config handlers both require
		// this permission; without it non-admin users got 403 and the Settings
		// page rendered a blank form (issues #1401, #1410, #1413). Writing
		// to config still requires update:config, which only admins hold via
		// admin:*. The handler also withholds the SourceIdentity field from
		// non-admin sessions regardless of this grant (issue #407).
		{Action: ActionView, Resource: ResourceConfig},
	}
}

// DefaultReadOnlyPermissions returns read-only permissions.
func DefaultReadOnlyPermissions() []Permission {
	return []Permission{
		{Action: ActionView, Resource: ResourceRecommendations},
		{Action: ActionView, Resource: ResourcePlans},
		{Action: ActionView, Resource: ResourceHistory},
		// view:config — read-only users can view global settings (issue #1401).
		// They cannot write config (update:config is admin-only via admin:*).
		{Action: ActionView, Resource: ResourceConfig},
	}
}

// DefaultPurchaserPermissions returns the permissions for the system-managed
// Purchaser group (issue #923). The three execute/approve-any/retry-any verbs
// are carved out of the admin:* wildcard; a user must hold them explicitly
// (via this group or a custom group that includes them) to spend money.
func DefaultPurchaserPermissions() []Permission {
	return []Permission{
		// Money-spending verbs (carved out of admin:* wildcard).
		{Action: ActionExecute, Resource: ResourcePurchases},
		{Action: ActionApproveAny, Resource: ResourcePurchases},
		{Action: ActionRetryAny, Resource: ResourcePurchases},
		// Read access so Purchaser members can navigate to the relevant pages.
		{Action: ActionView, Resource: ResourceRecommendations},
		{Action: ActionView, Resource: ResourcePlans},
		{Action: ActionView, Resource: ResourcePurchases},
		{Action: ActionView, Resource: ResourceHistory},
	}
}
