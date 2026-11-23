package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// riExchangeArmState is the subset of GlobalConfig the auto-mode gate compares.
//
// An explicit value snapshot of exactly the compared scalars, rather than a
// copy of the whole struct: GlobalConfig carries slices and maps, so a shallow
// `before := *existing` would alias them and could not be trusted to describe
// the pre-write state of anything but its scalar fields. Narrowing to the four
// fields the decision actually reads makes that a non-question.
type riExchangeArmState struct {
	mode        string
	perExchange float64
	daily       float64
	enabled     bool
}

func riExchangeArmStateOf(cfg *config.GlobalConfig) riExchangeArmState {
	return riExchangeArmState{
		enabled:     cfg.RIExchangeEnabled,
		mode:        cfg.RIExchangeMode,
		perExchange: cfg.RIExchangeMaxPerExchangeUSD,
		daily:       cfg.RIExchangeMaxDailyUSD,
	}
}

// armed reports whether this state will make the scheduled TaskRIExchangeReshape
// execute exchanges against the provider without a human approval step.
//
// The predicate is deliberately wider than the consumer. pkg/exchange's
// RunAutoExchange executes without approval only for the literal "auto" and
// refuses any other unknown mode (including "") with an error, so a typo
// fails the scheduled run loudly instead of executing. PUT /api/config
// validates newly assigned modes, but preserves legacy unknown modes during
// unrelated partial writes. Treating
// every such value as armed keeps the grant check closed if the consumer's
// mode handling ever changes.
func (s riExchangeArmState) armed() bool {
	return s.enabled && s.mode != "manual"
}

// requireRIExchangeAutoModeGrant gates the config write that arms unattended
// RI exchange behind the same execute:ri-exchange verb the direct execute
// handlers require.
//
// execute:ri-exchange is deliberately carved out of admin:* (issue #1644,
// PR #1758) so a compromised admin account alone cannot drain commitments.
// update:config is NOT carved out, by design. But arming auto-mode is
// functionally equivalent to pre-authorizing every future exchange: the
// scheduled task then executes against the provider with no approval step and
// no execute:ri-exchange check anywhere on its path, which reopened the exact
// threat the carve-out closed (issue #1765).
//
// Authorization goes through requirePermission, taking the same request the
// caller was authenticated from (resolved ahead of the transaction by
// resolveGrant and passed in as grant), so the verb is evaluated against
// THE CREDENTIAL PRESENTED rather than the owning user's group permissions.
// That distinction is load-bearing: for a user API key, requirePermission
// routes to authorizeAPIKey and checks the KEY's effective permissions.
// Checking the session's user id instead let a CI key that is correctly
// denied execute:ri-exchange arm the scheduler to execute every exchange --
// the same inheritance bug
// requirePermissionConstraints already guards against one function above, and
// the reason this does not call requireSessionPermission directly. It also
// gives the stateless admin API key the same answer here as on the direct
// execute handlers, instead of failing its user lookup and returning a 500.
//
// Only the escalation is gated, never the whole ri-exchange config surface. An
// update:config-only operator keeps view:config, mode "manual", the utilization
// and lookback tunables, and even RIExchangeEnabled while mode stays manual --
// that merely has the scheduler raise Pending approvals, which move no money.
// Two transitions require the verb:
//
//   - arming: the write leaves the config armed when it was not armed before;
//   - raising a spend ceiling while already armed, since the caps are plain
//     fields on the same struct and the same actor would otherwise set both the
//     switch and its bound.
//
// Lowering a cap, or an idempotent re-write of an already-armed config, is a
// de-escalation and stays available to update:config alone.
//
// Called from BOTH config write paths. PUT /api/config (updateConfig) reaches
// these fields exactly as effectively as the dedicated PUT /api/ri-exchange/config
// (updateRIExchangeConfig), so a gate on one alone would look complete and
// would not be.
//
// It runs inside the UpdateGlobalConfigAtomic closure, so it must not touch
// the pool (issue #1517): grant is the outcome of that requirePermission call,
// resolved before the transaction opened, and is returned only when the write
// escalates.
func requireRIExchangeAutoModeGrant(before, after riExchangeArmState, grant error) error {
	if !after.armed() {
		return nil
	}
	raisesCap := after.perExchange > before.perExchange || after.daily > before.daily
	if before.armed() && !raisesCap {
		return nil
	}
	return grant
}

// getAccountScope returns the auth.AccountScope describing which cloud
// accounts the session may reach.
//
// The scope is a value, not a list, and its ZERO FORM DENIES: an
// AccountScope{} grants no account at all. That is the inverse of the
// []string it replaced, where an empty slice meant ALL access -- the
// representation confusion behind issue #1748. Unrestricted access is carried
// as an explicit flag and is only ever set positively, here for the stateless
// admin API key (an infrastructure credential with no user row) and by
// auth.ScopeFromLegacyList for a resolved list that is empty or carries "*".
//
// Fails closed: every path that cannot establish the scope returns an error
// alongside a zero AccountScope, so a caller that ignores the error still
// denies.
//
// A restricted scope matches its entries by account ID only. A caller that
// knows an account only by display name is resolved to the one registered
// account carrying that name (withRegisteredAccountNames, issues #433 and
// #1034). Every scoped handler gets its scope here, so this is the one place
// that has to load the account list.
func (h *Handler) getAccountScope(ctx context.Context, session *Session) (auth.AccountScope, error) {
	if session.UserID == apiKeyAdminUserID {
		// Positively unrestricted: the stateless admin API key is an
		// infrastructure credential with no user row. Expressed as a set flag,
		// never as an empty list (issue #1748).
		return auth.UnrestrictedScope(), nil
	}
	if h.auth == nil {
		// Fail closed. Auth components must fail closed when nil, never fall
		// through. The zero AccountScope returned alongside this error denies
		// everything even if a caller ignores the error.
		return auth.AccountScope{}, fmt.Errorf("authentication service not configured: cannot establish account scope")
	}
	resolved, err := h.auth.GetAllowedAccountsAPI(ctx, session.UserID)
	if err != nil {
		return auth.AccountScope{}, err
	}
	// Safe here and only here: GetAllowedAccountsAPI has already failed closed
	// on an unestablishable scope, so an empty list at this point genuinely
	// means "no restriction configured".
	return h.withRegisteredAccountNames(ctx, auth.ScopeFromLegacyList(resolved)), nil
}

// withRegisteredAccountNames lets a restricted scope identify an account by
// its display name, limited to names that exactly one cloud account carries.
// The scope must still hold that account's ID: a name never matches a scope
// entry, because a stored name is not bound to an account and would grant
// whichever account takes the name later (issue #1034). Display names are not
// unique either, so a shared name identifies no account (issue #433). When the
// account list cannot be loaded the scope cannot resolve names at all: that
// only narrows access, and callers that pass the account ID are unaffected.
func (h *Handler) withRegisteredAccountNames(ctx context.Context, scope auth.AccountScope) auth.AccountScope {
	if scope.AllowsAll() || len(scope.Accounts) == 0 || h.config == nil {
		return scope
	}
	registered, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		logging.Warnf("account scope: cannot load cloud accounts, matching scope entries by ID only: %v", err)
		return scope
	}
	known := make([]auth.RegisteredAccount, len(registered))
	for i := range registered {
		known[i] = auth.RegisteredAccount{ID: registered[i].ID, Name: registered[i].Name, ExternalID: registered[i].ExternalID}
	}
	return scope.WithRegisteredAccounts(known)
}
