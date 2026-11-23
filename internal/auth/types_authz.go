package auth

// AuthContext represents the complete authorization context for a user
// It combines group memberships and the permissions computed from them.
type AuthContext struct { //nolint:revive // exported: doc comment style intentional
	User            *User
	Groups          []*Group
	AllowedAccounts []string     // Computed from all groups (union)
	Permissions     []Permission // Computed from group memberships

	// SkippedGroups counts memberships that could not be resolved (deleted or
	// unreadable), counted where the skip happens.
	//
	// len(User.GroupIDs) - len(Groups) would give the same answer, since Groups
	// is appended once per ID with no dedup and duplicate IDs therefore produce
	// duplicate entries. Counting explicitly states the intent rather than
	// relying on two lengths staying aligned (issue #1748).
	SkippedGroups int
}

// adminCarvedOuts is the set of (action, resource) pairs that the admin:*
// wildcard does NOT cover. Each pair requires explicit membership in a group
// that holds the matching permission (e.g. the Purchaser group). This
// implements separation-of-duties for money-spending operations (issue #923):
// a compromised admin account alone cannot drain commitments.
var adminCarvedOuts = map[[2]string]bool{
	{ActionExecute, ResourcePurchases}:    true,
	{ActionApproveAny, ResourcePurchases}: true,
	{ActionRetryAny, ResourcePurchases}:   true,
	// execute:ri-exchange (issue #1644). An RI exchange consumes existing
	// commitments and buys replacements, and the provider APIs have no
	// rollback once submitted, so it belongs under the same separation of
	// duties as execute:purchases. Membership in the seeded RI Exchanger
	// group (migration 000096) is what grants it.
	{ActionExecute, ResourceRIExchange}: true,
}

// coversCarvedOut reports whether perm, as stated, would satisfy any
// carved-out pair under enforcement's own matching rules
// (checkPermissionMatch). A stored resource of "*" matches every resource
// there, so {execute, *} covers execute:purchases and execute:ri-exchange and
// is carved out exactly as those pairs are (issue #1901). Every carve-out
// test goes through here so the grant-time guards and the enforcement
// matchers cannot disagree about what the set covers.
func coversCarvedOut(perm Permission) bool {
	for pair := range adminCarvedOuts {
		if checkPermissionMatch(perm, pair[0], pair[1]) {
			return true
		}
	}
	return false
}

// HasPermission checks if the auth context has a specific permission.
// Authorization is derived purely from group-granted permissions: a user
// who is a member of the Administrators group holds {ActionAdmin, ResourceAll}
// and therefore passes any check; a user with no groups holds no permissions
// and is denied everything (fail closed).
//
// The admin:* wildcard is intentionally narrow for the three carved-out
// money-spending verbs (execute:purchases, approve-any:purchases,
// retry-any:purchases). Those require explicit membership in a group that
// grants them directly (e.g. the Purchaser group seeded by migration 000054).
func (ctx *AuthContext) HasPermission(action, resource string) bool {
	for _, perm := range ctx.Permissions {
		// Admin permission grants all access EXCEPT the carved-out
		// money-spending verbs (separation of duties, issue #923).
		if perm.Action == ActionAdmin && perm.Resource == ResourceAll {
			if coversCarvedOut(Permission{Action: action, Resource: resource}) {
				// Fall through to explicit-permission check below.
				continue
			}
			return true
		}

		// Check action and resource match
		if perm.Action != action {
			continue
		}
		if perm.Resource != resource && perm.Resource != ResourceAll {
			continue
		}

		return true
	}

	return false
}

// IsUnrestrictedAccess returns true if the allowed list grants access to all
// accounts — either because it's empty (backward-compat default) or contains
// a "*" wildcard entry. Handlers can use this to short-circuit their filter
// loops without iterating accounts when access is unrestricted.
//
// WARNING — fail-open default (03-L5): an empty allowed list means "all
// accounts", not "no accounts". This is a deliberate backward-compatibility
// default so existing groups without an AllowedAccounts configuration grant
// full access. New callers that intend to express "no access" must represent
// that with an explicit sentinel (e.g. a list containing only a non-existent
// account ID) and must NOT rely on an empty list for the "deny all" case.
func IsUnrestrictedAccess(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == "*" {
			return true
		}
	}
	return false
}
