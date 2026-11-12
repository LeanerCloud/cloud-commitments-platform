package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// AccountServiceOverrideRequest is the request body for service override endpoints.
type AccountServiceOverrideRequest struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	Term           *int     `json:"term,omitempty"`
	Payment        *string  `json:"payment,omitempty"`
	Coverage       *float64 `json:"coverage,omitempty"`
	RampSchedule   *string  `json:"ramp_schedule,omitempty"`
	IncludeEngines []string `json:"include_engines,omitempty"`
	ExcludeEngines []string `json:"exclude_engines,omitempty"`
	IncludeRegions []string `json:"include_regions,omitempty"`
	ExcludeRegions []string `json:"exclude_regions,omitempty"`
	IncludeTypes   []string `json:"include_types,omitempty"`
	ExcludeTypes   []string `json:"exclude_types,omitempty"`
}

// serviceOverrideOverlayKeys are the JSON keys of AccountServiceOverrideRequest.
// A key present in the PUT body replaces the stored value (null clears it); an
// absent key keeps it.
var serviceOverrideOverlayKeys = []string{
	"enabled", "term", "payment", "coverage", "ramp_schedule",
	"include_engines", "exclude_engines",
	"include_regions", "exclude_regions",
	"include_types", "exclude_types",
}

// listAccountServiceOverrides handles GET /api/accounts/:id/service-overrides.
func (h *Handler) listAccountServiceOverrides(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	_, err = h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	overrides, err := h.config.ListAccountServiceOverrides(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	if overrides == nil {
		overrides = []config.AccountServiceOverride{}
	}

	return overrides, nil
}

// saveAccountServiceOverride handles PUT /api/accounts/:id/service-overrides/:provider/:service.
// providerServicePath contains the remaining path segment: "uuid/service-overrides/provider/service".
func (h *Handler) saveAccountServiceOverride(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, providerServicePath string) (any, error) {
	accountID, provider, service, err := parseServiceOverridePath(providerServicePath)
	if err != nil {
		return nil, NewClientError(400, err.Error())
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "accounts")
	if err != nil {
		return nil, err
	}

	account, err := h.requireAccountAccess(ctx, session, accountID)
	if err != nil {
		return nil, err
	}
	// The store's lock key is built from the id, so every spelling of one
	// account must reach it as the stored (canonical) one.
	accountID = account.ID

	var req AccountServiceOverrideRequest
	err = json.Unmarshal([]byte(httpReq.Body), &req)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	present, err := presentKeys(httpReq.Body, serviceOverrideOverlayKeys)
	if err != nil {
		return nil, err
	}

	// Fetch the commitment options before the transaction opens: the merge
	// callback runs while it holds a pooled connection, so it must not take
	// another (see commitmentOptsSnapshot).
	combos := h.prefetchCommitmentOptions(ctx, provider)

	// The read, merge and write run in one locked transaction (issue #1775),
	// so two single-field PUTs cannot merge onto the same stale row and lose
	// one change. The audit event commits with the write (issue #1493).
	audit := config.AuditContext{Actor: configAuditActor(session)}
	override, err := h.config.UpdateAccountServiceOverrideAtomic(ctx, accountID, provider, service, audit,
		func(existing *config.AccountServiceOverride) (*config.AccountServiceOverride, error) {
			return mergeAndValidateServiceOverride(accountID, provider, service, req, existing, present, combos)
		})
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return override, nil
}
