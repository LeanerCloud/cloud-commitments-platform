package api

// Auth request/response types (to avoid import cycle with auth package).
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	MFACode  string `json:"mfa_code,omitempty"`
	// MFAToken is the continuation token from an mfa_required answer
	// (issue #134), sent with Email and MFACode in place of Password.
	MFAToken string `json:"mfa_token,omitempty"`
}

// LoginResponse carries the JWT token and user summary returned after a successful login.
type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt string    `json:"expires_at"`
	User      *UserInfo `json:"user"`
	CSRFToken string    `json:"csrf_token,omitempty"`
}

// UserInfo is a lightweight projection of the authenticated user included in login
// and session responses; it carries only the fields needed for client-side routing
// and display, without exposing credential hashes or internal audit fields.
type UserInfo struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// SetupAdminRequest is the one-time bootstrap payload used to create the first
// administrator account when the system has no users yet.
type SetupAdminRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
}

// PasswordResetRequest initiates a password-reset flow by sending a reset link
// to the provided email address if it matches an existing account.
type PasswordResetRequest struct {
	Email string `json:"email"`
}

// PasswordResetConfirm completes a password-reset flow by pairing the one-time
// token (delivered by email) with the user's chosen new password.
type PasswordResetConfirm struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// Session holds the minimal identity claims extracted from a validated JWT and
// attached to the request context by the authentication middleware.
type Session struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	// UserAPIKeyID is the database key ID when the request was authenticated via
	// a user API key (not the admin infra key). Empty for bearer-token and
	// admin-API-key sessions. Used by requirePermissionConstraints to evaluate
	// constraints against the key's effective permissions rather than the owning
	// user's full group permissions (adversarial-review F2).
	UserAPIKeyID string `json:"-"`
}

// User is the full user record returned by the admin user-management endpoints.
// It mirrors UserInfo's identity fields and adds CreatedAt/UpdatedAt audit
// timestamps; it is never embedded in the public-facing login or session
// responses, which use the slimmer UserInfo / CurrentUserResponse types instead.
type User struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// Group represents a user group with permissions.
type Group struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Description     string       `json:"description,omitempty"`
	CreatedAt       string       `json:"created_at,omitempty"`
	UpdatedAt       string       `json:"updated_at,omitempty"`
	Permissions     []Permission `json:"permissions"`
	AllowedAccounts []string     `json:"allowed_accounts,omitempty"`
}

// Permission represents an action that can be performed on a resource.
type Permission struct {
	Constraints *PermissionConstraint `json:"constraints,omitempty"`
	Action      string                `json:"action"`
	Resource    string                `json:"resource"`
}

// PermissionConstraint limits where a permission applies.
type PermissionConstraint struct {
	Accounts  []string `json:"accounts,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Services  []string `json:"services,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	MaxAmount float64  `json:"max_amount,omitempty"`
}

// ChangePasswordRequest represents a request to change password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ProfileUpdateRequest represents a profile update request.
type ProfileUpdateRequest struct {
	Email           string `json:"email"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password,omitempty"`
}

// MFASetupRequest begins an MFA enrollment. Current password is
// required as defense-in-depth — a stolen session alone shouldn't
// be enough to swap a user's MFA secret.
type MFASetupRequest struct {
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
}

// MFASetupResponse returns the freshly-generated secret + the
// otpauth:// URI the frontend renders as a QR code. The secret is
// already persisted server-side as the pending secret; clients do
// not need to round-trip it back on enable.
type MFASetupResponse struct {
	Secret          string `json:"secret"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	ProvisioningURI string `json:"provisioning_uri"`
}

// MFAEnableRequest finalizes an enrollment by proving the user
// loaded the secret into their authenticator (the supplied code is
// validated against the pending secret).
type MFAEnableRequest struct {
	Code string `json:"code"`
}

// MFAEnableResponse returns the plaintext recovery codes exactly
// once. Backend stores only bcrypt hashes.
type MFAEnableResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// MFADisableRequest turns off MFA. Requires the current password AND
// a fresh proof-of-possession (TOTP code or unused recovery code).
type MFADisableRequest struct {
	Password string `json:"password"` //nolint:gosec // G117: intentional credential field in request/response struct -- value is supplied by the authenticated caller or returned once at creation; not re-stored downstream
	Code     string `json:"code"`
}

// MFARegenerateRequest replaces all stored recovery codes. Requires
// a fresh TOTP code (NOT a recovery code — see service for the
// rationale).
type MFARegenerateRequest struct {
	Code string `json:"code"`
}

// MFARegenerateResponse mirrors MFAEnableResponse — plaintext codes
// returned exactly once.
type MFARegenerateResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}
