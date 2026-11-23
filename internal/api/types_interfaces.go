package api

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/scheduler"
)

// PurchaseManagerInterface defines purchase manager methods used by handler.
// `actor` is the session-authenticated user's email for per-user
// attribution via the auth-gated deep-link flow; pass "" for token-only
// paths (message workers, legacy callers) where attribution falls back to
// the notification email at render time.
//
// `transitionedBy` (ApproveAndExecute) is the session user's UUID stamped
// onto purchase_executions.transitioned_by for human-initiated approvals;
// pass nil for token/system flows so transitioned_by = NULL (issue #1009).
//
// ApproveExecution, ApproveAndExecute and RunPlannedPurchaseNow return the
// raw revocation token minted on a successful execute (issue #103: only its
// hash is stored, so callers can no longer recover a raw, emailable token by
// re-reading the execution from the store). Empty on failure. All three
// also return the row with its final status, also on an execution error, and
// a nil row when the run never started (issue #532).
//
// `check` (every execute entry point) re-runs the caller's permission
// Constraints against the row the claim CAS returned, the row that is
// actually bought, before any provider call (issue #415). The handler's own
// pre-CAS check reads an earlier snapshot, which a repricing or a
// recommendation attach can change before the CAS.
type PurchaseManagerInterface interface {
	ApproveExecution(ctx context.Context, execID, token, actor string, check purchase.ClaimedRowCheck) (*config.PurchaseExecution, string, error)
	ApproveAndExecute(ctx context.Context, execID, actor string, transitionedBy *string, check purchase.ClaimedRowCheck) (*config.PurchaseExecution, string, error)
	// DirectExecute is ApproveAndExecute for the direct-execute permission; it
	// stamps the executed_* audit fields only after the claim is won.
	DirectExecute(ctx context.Context, execID, actor string, transitionedBy *string, check purchase.ClaimedRowCheck) (*config.PurchaseExecution, string, error)
	// RunPlannedPurchaseNow forces a pending or paused scheduled purchase to
	// execute immediately (the "Run now" button), sharing ApproveAndExecute's
	// 4-eyes-gated, CAS-guarded funnel instead of a bare status flip that
	// nothing else consumes (issue #218).
	RunPlannedPurchaseNow(ctx context.Context, execID, actor string, transitionedBy *string, check purchase.ClaimedRowCheck) (*config.PurchaseExecution, string, error)
	CancelExecution(ctx context.Context, execID, token, actor string) error
}

// SchedulerInterface defines scheduler methods used by handler.
type SchedulerInterface interface {
	CollectRecommendations(ctx context.Context, ownerToken string) (*scheduler.CollectResult, error)
	ListRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error)
	// ListRecommendationsStrict is ListRecommendations for the money path:
	// suppressions and overrides applied, read errors returned (planstep.Source).
	ListRecommendationsStrict(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error)
	// ScanRecommendations walks the same rows as ListRecommendations in
	// byte-wise ID order, chunkSize store rows at a time, calling fn with
	// each chunk's survivors (issue #981).
	ScanRecommendations(ctx context.Context, filter config.RecommendationFilter, chunkSize int, fn func([]config.RecommendationRecord) error) error
	// PageRecommendations returns one page of the list and the summary of
	// the whole filtered set with every filter evaluated in SQL (issue
	// #981). An error wrapping scheduler.ErrRecommendationsPageNeedsScan
	// means the request must be served by ScanRecommendations instead.
	PageRecommendations(ctx context.Context, scope config.RecommendationListScope, afterID string, limit int) (*config.RecommendationPage, error)
	// GetRecommendationByID fetches a single rec by its application-level id,
	// bypassing account-override filtering so deep-linked URLs to override-
	// hidden recs resolve. hiddenBy is non-nil when the rec would be dropped by
	// the override filter; callers render a "hidden" banner. Returns nil, nil,
	// nil when the rec is absent or fully suppressed.
	GetRecommendationByID(ctx context.Context, id string) (rec *config.RecommendationRecord, hiddenBy []string, err error)
}

// AuthServiceInterface defines auth service methods used by handler
// Note: This interface uses API-specific types that are converted from auth package types.
type AuthServiceInterface interface {
	Login(ctx context.Context, req LoginRequest) (*LoginResponse, error)
	Logout(ctx context.Context, token string) error
	ValidateSession(ctx context.Context, token string) (*Session, error)
	ValidateCSRFToken(ctx context.Context, sessionToken, csrfToken string) error
	SetupAdmin(ctx context.Context, req SetupAdminRequest) (*LoginResponse, error)
	CheckAdminExists(ctx context.Context) (bool, error)
	RequestPasswordReset(ctx context.Context, email string) error
	ConfirmPasswordReset(ctx context.Context, req PasswordResetConfirm) error
	// ResetTokenStatus returns the runtime state of a reset token
	// without consuming it. Used by the GET /api/auth/reset-password/
	// status endpoint so the frontend can branch on expired / used
	// tokens before rendering the reset-password form (issues #460,
	// #461). state is one of "valid" | "expired" | "used"; flow is
	// "reset" | "invite".
	ResetTokenStatus(ctx context.Context, token string) (state string, flow string, err error)
	GetUser(ctx context.Context, userID string) (*User, error)
	UpdateUserProfile(ctx context.Context, userID string, email string, currentPassword string, newPassword string) error
	// User management - uses auth.API* types
	CreateUserAPI(ctx context.Context, actorUserID string, req auth.APICreateUserRequest) (*auth.APICreateUserResponse, error)
	UpdateUserAPI(ctx context.Context, actorUserID, userID string, req auth.APIUpdateUserRequest) (*auth.APIUser, error)
	DeleteUser(ctx context.Context, userID string) error
	ListUsersAPI(ctx context.Context) (any, error)
	ChangePasswordAPI(ctx context.Context, userID, currentPassword, newPassword string) error
	// MFA lifecycle (issue #497). All four require the user to be
	// already authenticated; setup + disable additionally require a
	// fresh password re-verify carried in the request body.
	MFASetupAPI(ctx context.Context, userID, password string) (secret, provisioningURI string, err error)
	MFAEnableAPI(ctx context.Context, userID, code string) (recoveryCodes []string, err error)
	MFADisableAPI(ctx context.Context, userID, password, codeOrRecovery string) error
	MFARegenerateRecoveryCodesAPI(ctx context.Context, userID, code string) (recoveryCodes []string, err error)
	// Group management - uses auth.API* types
	// CreateGroupAPI / UpdateGroupAPI take the acting principal so the
	// service can enforce the grant ceiling: a caller may not write a
	// permission onto a group that their own effective permissions do not
	// hold, and the money verbs carved out of admin:* are not grantable at
	// all (issues #1550, #1629).
	CreateGroupAPI(ctx context.Context, actorUserID string, req auth.APICreateGroupRequest) (*auth.APIGroup, error)
	UpdateGroupAPI(ctx context.Context, actorUserID, groupID string, req auth.APIUpdateGroupRequest) (*auth.APIGroup, error)
	DeleteGroup(ctx context.Context, groupID string) error
	GetGroupAPI(ctx context.Context, groupID string) (any, error)
	ListGroupsAPI(ctx context.Context) (any, error)
	// Permission checking
	HasPermissionAPI(ctx context.Context, userID, action, resource string) (bool, error)
	// HasPermissionForConstraintsAPI checks action on resource against
	// request-derived constraint sets so per-permission Constraints
	// (MaxPurchaseAmount, Providers, Services, Regions, AccountIDs) are
	// enforced at execution time. Every constraint set must be granted by
	// at least one of the user's permissions (SEC-01, issue #1141).
	HasPermissionForConstraintsAPI(ctx context.Context, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error)
	// GetUserPermissionsAPI returns the effective permission set for a user
	// (union of all group permissions). Used by GET /api/auth/me/permissions.
	// Returns []auth.APIPermission converted to []PermissionEntry by the handler.
	GetUserPermissionsAPI(ctx context.Context, userID string) (any, error)
	// GetAPIKeyEffectivePermissionsAPI returns a user API key's effective
	// permission set (the key's permissions intersected with its owner's,
	// carrying the key's constraints): the set HasAPIKeyPermissionAPI
	// enforces. Used by GET /api/auth/me/permissions for user-API-key
	// principals so the key, not its owner, is described (issue #62). A
	// non-nil error means the key or its owner could not be confirmed, and
	// callers must refuse rather than fall back to the owner's permissions.
	GetAPIKeyEffectivePermissionsAPI(ctx context.Context, keyID, userID string) ([]auth.APIPermission, error)
	// Account access - returns the union of allowed_accounts from all user groups (empty = all access)
	GetAllowedAccountsAPI(ctx context.Context, userID string) ([]string, error)
	// API Key management
	// CreateAPIKeyAPI takes the auth wire type directly: re-encoding a local
	// copy erased an explicit max_amount of 0 (issue #980).
	CreateAPIKeyAPI(ctx context.Context, userID string, req auth.APICreateAPIKeyRequest) (any, error)
	ListUserAPIKeysAPI(ctx context.Context, userID string) (any, error)
	// GetAPIKeysUsageStatsAPI returns the section-level usage summary
	// (active count, fixed-window/lifetime request totals, top-3 most
	// active keys) for the calling user's own API keys.
	GetAPIKeysUsageStatsAPI(ctx context.Context, userID string) (any, error)
	DeleteAPIKeyAPI(ctx context.Context, userID, keyID string) error
	RevokeAPIKeyAPI(ctx context.Context, userID, keyID string) error
	ValidateUserAPIKeyAPI(ctx context.Context, apiKey string) (any, any, error)
	// RecordAPIKeyUsageAsync books exactly one request against the given
	// API key ID (the last_used_at timestamp plus the lifetime and
	// fixed-window request counters). Fire-and-forget: the store write
	// happens off the request path, so it returns nothing and never delays
	// or fails the caller's request.
	//
	// Must be called once per inbound request and only from
	// Handler.validateSecurityContext. Calling it from a credential
	// validation path instead would count validations, which a single
	// request performs several times over.
	RecordAPIKeyUsageAsync(keyID string)
	// HasAPIKeyPermissionAPI validates a user API key and checks the
	// requested action/resource against the key's effective permissions
	// (the intersection of the key's scoped permissions with the owning
	// user's group-derived permissions). Returns the owning user's ID, the
	// key's database ID, and whether the permission is held; a non-nil error
	// means the key did not validate or the lookup failed, and callers must
	// deny (fail closed). Wired into requirePermission so per-key scoping is
	// enforced at request time (issue #1142).
	HasAPIKeyPermissionAPI(ctx context.Context, apiKey, action, resource string) (userID, keyID string, allowed bool, err error)
	// HasAPIKeyPermissionForConstraintsAPI checks request-derived permission
	// constraint sets against a user API key's effective permissions (the
	// intersection of the key's own constraints and the owning user's
	// group-derived permissions). Must be called after HasAPIKeyPermissionAPI
	// confirms action/resource access. Enforces per-key caps (MaxPurchaseAmount,
	// Providers, Services, Regions, AccountIDs) that the key's owner cannot
	// override via their broader group permissions (adversarial-review F2).
	HasAPIKeyPermissionForConstraintsAPI(ctx context.Context, keyID, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error)
}
