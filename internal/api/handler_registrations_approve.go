package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
)

// encryptRegistrationCredential encrypts the credential payload for storage in the
// registration table. Returns empty string if no credential is provided.
func (h *Handler) encryptRegistrationCredential(payload string) (string, error) {
	if payload == "" || h.credStore == nil {
		return "", nil
	}
	return h.credStore.EncryptPayload([]byte(payload))
}

// approveRegistration handles POST /api/registrations/:id/approve.
// The body is a CloudAccountRequest (the same format as POST /api/accounts),
// pre-populated from registration data by the frontend and optionally edited.
func (h *Handler) approveRegistration(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, id string) (any, error) {
	reg, err := h.getPendingRegistration(ctx, id)
	if err != nil {
		return nil, err
	}

	// Before the transition, so a rejected request leaves the registration pending.
	acctReq, regCred, err := h.parseApprovalRequest(ctx, reg, httpReq)
	if err != nil {
		return nil, err
	}

	h.setReviewMetadata(ctx, reg, httpReq)

	now := time.Now()
	account := cloudAccountFromRequest(acctReq)
	account.ID = uuid.New().String()
	// Auto-enable when the operator either embedded a credential in
	// the registration (legacy key-based flow) OR when the account
	// uses an auth mode that doesn't require any stored credential
	// at all — ambient (instance profile, managed identity, ADC) or
	// KMS-backed federated. Without this, every federated-path
	// approval leaves the account disabled and the operator has to
	// follow up with a manual PUT enabled=true.
	account.Enabled = (regCred != nil || accountHasCredentialFreePath(account)) && !approvalDisablesAccount(httpReq.Body)
	account.CreatedAt = now
	account.UpdatedAt = now

	// Approve, create the account and link them in one transaction, so a
	// failed account insert leaves the registration pending rather than
	// approved with no account (issue #1102). The conditional status change
	// inside it also stops a double approval. reviewed_by may be
	// "admin-api-key" (not a UUID FK); validate before passing as actor.
	if err := h.config.ApproveAccountRegistration(ctx, reg, account, validUUIDPtrOrNil(reg.ReviewedBy)); err != nil {
		return nil, classifyApproveRegistrationError(err, acctReq)
	}

	// Auto-store credentials if the registration included them.
	// The registration's copy is dropped only after the save succeeds; until
	// then it is the only copy. The account exists, so a failed clear is logged.
	if h.storeRegistrationCredentials(ctx, reg, account.ID, regCred) {
		if err := h.config.ClearRegistrationCredentialPayload(ctx, reg.ID); err != nil {
			logging.Errorf("registration %s: failed to clear stored credential payload: %v", reg.ID, err)
		}
	}

	h.notifyRegistrant(reg, email.RegistrationDecisionData{
		AccountName: reg.AccountName, Provider: reg.Provider,
		ExternalID: reg.ExternalID, Decision: "approved",
	})

	return account, nil
}

// approvalDisablesAccount reports whether the approval body carries an
// explicit "enabled": false. CloudAccountRequest.Enabled is a plain bool, so
// an omitted field cannot be told apart from false there; only an explicit
// false disables, and the body was already parsed by parseApprovalRequest.
func approvalDisablesAccount(body string) bool {
	var p struct {
		Enabled *bool `json:"enabled"`
	}
	return json.Unmarshal([]byte(body), &p) == nil && p.Enabled != nil && !*p.Enabled
}

// classifyApproveRegistrationError maps an ApproveAccountRegistration failure
// to the response: 409 when another request already decided the registration
// or the account is already registered, 500 otherwise.
func classifyApproveRegistrationError(err error, acctReq CloudAccountRequest) error {
	if errors.Is(err, config.ErrRegistrationConflict) {
		return NewClientError(409, "registration was already processed by another request")
	}
	if isDuplicateKeyError(err) {
		return NewClientError(409, fmt.Sprintf("an account with external ID %q already exists for %s", acctReq.ExternalID, acctReq.Provider))
	}
	return fmt.Errorf("registrations: approve: %w", err)
}

// parseApprovalRequest reads and checks an approval body for reg: the
// CloudAccountRequest itself, the host-account rule for an empty role ARN, and
// the credential stored with the registration (see registrationCredential).
// It returns the request and the checked credential, nil when there is none.
func (h *Handler) parseApprovalRequest(ctx context.Context, reg *config.AccountRegistration, httpReq *events.LambdaFunctionURLRequest) (CloudAccountRequest, []byte, error) {
	var acctReq CloudAccountRequest
	if err := json.Unmarshal([]byte(httpReq.Body), &acctReq); err != nil {
		return acctReq, nil, NewClientError(400, "invalid account request body")
	}
	if err := validateCloudAccountRequest(acctReq); err != nil {
		return acctReq, nil, err
	}
	if err := h.requireHostForEmptyRoleARN(ctx, "(registration "+reg.ID+")", &acctReq); err != nil {
		return acctReq, nil, err
	}
	regCred, err := h.registrationCredential(reg, acctReq.Provider)
	return acctReq, regCred, err
}

// accountHasCredentialFreePath returns true when the account's auth
// mode resolves a credential without any stored secret — either via
// ambient instance credentials (role assumption, managed identity,
// Application Default Credentials) or via CUDly's KMS-backed OIDC
// federated path. These accounts should auto-enable on approval
// because there's no follow-up "upload the JSON" step.
func accountHasCredentialFreePath(acct *config.CloudAccount) bool {
	switch acct.Provider {
	case "aws":
		// role_arn flows via STS AssumeRole with ambient CUDly creds;
		// workload_identity_federation mints its own token file.
		return acct.AWSAuthMode == "role_arn" || acct.AWSAuthMode == "workload_identity_federation"
	case "azure":
		// managed_identity is ambient; workload_identity_federation
		// uses the KMS-backed OIDC signer to mint a client assertion
		// on every call — nothing is stored in CUDly per-account, so
		// auto-enable is always safe for either mode.
		return acct.AzureAuthMode == "managed_identity" || acct.AzureAuthMode == "workload_identity_federation"
	case "gcp":
		// application_default: ambient (Cloud Run / GKE). WIF:
		// federated when gcp_wif_audience is set (the CLI template
		// always sets it for the new flow).
		return acct.GCPAuthMode == "application_default" ||
			(acct.GCPAuthMode == "workload_identity_federation" && acct.GCPWIFAudience != "")
	}
	return false
}
