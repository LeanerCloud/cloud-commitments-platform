package auth

import (
	"encoding/json"
)

// API adapter types - these match the types in internal/api/handler.go

// APIUser is the user type for API responses.
//
// Groups deliberately has NO omitempty: the frontend's TS type
// (frontend/src/api/types.ts) declares groups as a required string[],
// and renderers read user.groups.length / iterate user.groups without
// a guard. Omitting the field on empty slices breaks that contract
// and crashes the admin users page with "TypeError: Cannot read
// properties of undefined (reading 'length')" — see issue #350.
type APIUser struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	LastLogin  string   `json:"last_login,omitempty"`
	Groups     []string `json:"groups"`
	MFAEnabled bool     `json:"mfa_enabled"`
	// No omitempty: false means deactivated, not absent.
	Active bool `json:"active"`
}

// APIGroup is the group type for API responses.
//
// AllowedAccounts has NO omitempty for the same reason Groups doesn't on
// APIUser — the frontend treats it as always present. See issue #350.
type APIGroup struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	CreatedAt       string          `json:"created_at,omitempty"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
	Permissions     []APIPermission `json:"permissions"`
	AllowedAccounts []string        `json:"allowed_accounts"`
	// SystemManaged marks a group seeded and maintained by migrations. The
	// API refuses to update or delete it (ErrSystemManagedGroup), and the
	// dashboard reads this flag to hide Edit and Delete (issue #919).
	SystemManaged bool `json:"system_managed,omitempty"`
}

// APIPermission is the permission type for API responses.
type APIPermission struct {
	Constraints *APIPermissionConstraint `json:"constraints,omitempty"`
	Action      string                   `json:"action"`
	Resource    string                   `json:"resource"`
}

// APIPermissionConstraint is the permission constraint type for API responses.
type APIPermissionConstraint struct {
	Accounts  []string `json:"accounts,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Services  []string `json:"services,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	MaxAmount float64  `json:"max_amount,omitempty"`

	// maxAmountSet records that the decoded JSON carried a non-null
	// max_amount, so an explicit 0 can be told from an absent field. Only
	// UnmarshalJSON sets it; a Go-built literal is never "explicit".
	maxAmountSet bool
}

// UnmarshalJSON decodes the constraint and records whether max_amount was
// present and non-null (null counts as absent).
func (c *APIPermissionConstraint) UnmarshalJSON(b []byte) error {
	type plain APIPermissionConstraint // no methods, so no recursion
	var probe struct {
		MaxAmount *float64 `json:"max_amount"`
	}
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	c.maxAmountSet = probe.MaxAmount != nil
	return nil
}

// APICreateUserRequest is the request type for creating users via API.
// Groups must be non-empty: authorization is group-membership-only (issue #907).
type APICreateUserRequest struct {
	Email    string   `json:"email"`
	Password string   `json:"password"` //nolint:gosec // G117: intentional credential field -- password supplied by caller for user creation; not re-stored downstream
	Groups   []string `json:"groups,omitempty"`
}

// APICreateUserResponse is the response type for POST /api/users. It
// embeds APIUser so existing consumers keep reading the flat
// {id, email, role, ...} fields and only callers that need the new
// invite-status information have to look at the extra optional fields.
//
// InviteEmailSent is non-nil only when the request created an invited
// (passwordless) user. true means the invite email was handed to the
// configured sender; false means the user row exists but the recipient
// hasn't been told how to activate it and the admin should re-mail the
// setup link via Forgot Password.
type APICreateUserResponse struct {
	*APIUser
	InviteEmailSent  *bool  `json:"invite_email_sent,omitempty"`
	InviteEmailError string `json:"invite_email_error,omitempty"`
}

// APIUpdateUserRequest is the request type for updating users via API.
//
// Groups is decoded from JSON, so the handler cannot use a nil slice to mean
// "not sent". A non-empty Groups replaces the user's membership; an empty/nil
// Groups means "leave membership unchanged" (callers that intend to change
// groups always send at least one, since zero-group users are forbidden).
//
// Active is a pointer so "not sent" differs from false; otherwise every PUT
// that omits it would reactivate the user.
type APIUpdateUserRequest struct {
	Active *bool    `json:"active,omitempty"`
	Email  string   `json:"email,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// APICreateGroupRequest is the request type for creating groups via API.
type APICreateGroupRequest struct {
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	Permissions     []APIPermission `json:"permissions"`
	AllowedAccounts []string        `json:"allowed_accounts,omitempty"`
}

// APIUpdateGroupRequest is the request type for updating groups via API.
// AllowedAccounts has no omitempty: clients must be able to send an explicit
// empty slice to clear account restrictions. Nil means "not sent".
//
// Permissions has the same "nil means not sent" contract, but encoding/json
// already gives UpdateGroupAPI what it needs without a pointer: decoding an
// omitted field leaves the slice nil, while decoding an explicit `[]` leaves
// it non-nil with length 0 (json.Unmarshal allocates the slice before
// leaving it empty). UpdateGroupAPI checks req.Permissions != nil &&
// len(req.Permissions) == 0 to refuse that explicit-empty case -- this
// endpoint has no way to express "clear all permissions" (a group must
// always grant at least one) -- rather than silently keeping the group's
// old permissions while reporting success (issue #237).
type APIUpdateGroupRequest struct {
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	Permissions     []APIPermission `json:"permissions,omitempty"`
	AllowedAccounts []string        `json:"allowed_accounts"`
}
