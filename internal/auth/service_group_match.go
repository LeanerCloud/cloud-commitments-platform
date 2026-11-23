package auth

import (
	"strings"
)

func checkPermissionMatch(perm Permission, action, resource string) bool {
	if perm.Action != action {
		return false
	}
	if perm.Resource != resource && perm.Resource != ResourceAll {
		return false
	}
	return true
}

func checkPermissionConstraints(perm Permission, constraints *PermissionConstraints) bool {
	if constraints != nil && constraints.Global {
		return !perm.Constraints.restricts()
	}
	if constraints != nil && perm.Constraints != nil {
		return matchConstraints(perm.Constraints, constraints)
	}
	return true
}

// matchConstraints checks if permission constraints match request constraints.
func matchConstraints(permConstraints, reqConstraints *PermissionConstraints) bool {
	if reqConstraints.StrictScope && len(permConstraints.Regions) > 0 && len(reqConstraints.Regions) == 0 {
		return false
	}
	matchList := matchStringListConstraints
	if reqConstraints.StrictScope {
		matchList = listCovers
	}
	return matchList(permConstraints.AccountIDs, reqConstraints.AccountIDs) &&
		matchList(permConstraints.Providers, reqConstraints.Providers) &&
		matchList(permConstraints.Services, reqConstraints.Services) &&
		matchAllRegionsConstraint(permConstraints.Regions, reqConstraints.Regions) &&
		matchPurchaseAmountConstraint(permConstraints.MaxPurchaseAmount, reqConstraints.MaxPurchaseAmount)
}

// matchStringListConstraints is the matcher for the AccountIDs, Providers and
// Services dimensions: EVERY requested value must be permitted, not merely
// one of them (issue #138).
//
// It used to be any-match (containsAny), which was safe only because every
// caller happens to pass a single-value request list; nothing enforced that.
// The first caller to submit, say, AccountIDs [A, B] would have been allowed
// by a permission scoped to A alone -- the same bypass the Regions dimension
// had to close with matchAllRegionsConstraint. All-match is identical to
// any-match for a single-value request, so no current caller changes
// behavior.
//
// Semantics (03-L6): if either list is empty the constraint is treated as
// satisfied ("no constraint specified = no restriction"). Concretely:
//   - empty permList: the permission has no constraint on this dimension
//   - empty reqList: the request does not specify this dimension
//
// Both cases return true (match). Only when both lists are non-empty is
// containsAll used, and it requires every requested value to be in permList.
// Comparison is exact (case-sensitive); listCovers relies on that.
//
// Callers that need "a constrained permission must only match an explicit
// request value" should verify reqList is non-empty before calling, or set
// StrictScope so matchConstraints uses listCovers instead.
func matchStringListConstraints(permList, reqList []string) bool {
	if len(permList) > 0 && len(reqList) > 0 {
		return containsAll(permList, reqList)
	}
	return true
}

// matchAllRegionsConstraint is the Regions dimension's matcher: EVERY
// requested region must be permitted, not merely one of them.
//
// Regions is the one dimension where a single request legitimately spans
// several values: an Azure RI exchange takes a list of targets, and
// handler_ri_exchange_azure_regions.go's targetLocations feeds all of their locations in
// at once. Under the any-match rule the dimensions once shared, that made the
// region constraint bypassable -- a caller permitted only in eastus could
// attach a westus target, any-match would find eastus in the permitted set,
// return true, and the exchange would execute for BOTH regions. "Regions
// limits to specific regions" (types.go) cannot mean "limits to requests that
// mention at least one permitted region".
//
// The other list dimensions are all-match too (matchStringListConstraints,
// issue #138); Regions keeps its own matcher only for the case-insensitive,
// trimmed comparison below.
//
// Comparison is case-insensitive so a permission stored as "EastUS" still
// matches the canonical lower-case form callers are normalized to; the
// empty-list semantics are unchanged from matchStringListConstraints (an
// unconstrained permission, or a request that does not name a region, still
// matches).
func matchAllRegionsConstraint(permRegions, reqRegions []string) bool {
	if len(permRegions) == 0 || len(reqRegions) == 0 {
		return true
	}
	permitted := make(map[string]bool, len(permRegions))
	for _, r := range permRegions {
		permitted[strings.ToLower(strings.TrimSpace(r))] = true
	}
	for _, r := range reqRegions {
		if !permitted[strings.ToLower(strings.TrimSpace(r))] {
			return false
		}
	}
	return true
}
