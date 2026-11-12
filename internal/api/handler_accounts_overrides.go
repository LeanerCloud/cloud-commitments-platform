package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// mergeAndValidateServiceOverride is the read-merge step of an override PUT,
// run inside the store's locked transaction: it merges the request onto the
// stored row, then rejects an invalid (term, payment) combo on the merged
// result. The check is permissive when no probe data exists, as for the
// service-config PUT. It does no I/O.
func mergeAndValidateServiceOverride(accountID, provider, service string, req AccountServiceOverrideRequest, existing *config.AccountServiceOverride, present map[string]bool, combos commitmentOptsSnapshot) (*config.AccountServiceOverride, error) {
	override := buildServiceOverride(accountID, provider, service, req, existing, present, time.Now())
	if err := combos.check(config.ServiceConfig{
		Provider: override.Provider,
		Service:  override.Service,
		Term:     derefInt(override.Term),
		Payment:  derefString(override.Payment),
	}); err != nil {
		return nil, err
	}
	return override, nil
}

// buildServiceOverride constructs an AccountServiceOverride by overlaying the
// request onto the stored row (or an empty one). present names the request
// keys the body carried: only those replace the stored value, so a one-field
// PUT keeps the rest (#1775). A present key with a null value clears it.
func buildServiceOverride(accountID, provider, service string, req AccountServiceOverrideRequest, existing *config.AccountServiceOverride, present map[string]bool, now time.Time) *config.AccountServiceOverride {
	override := &config.AccountServiceOverride{}
	if existing != nil {
		*override = *existing
	} else {
		override.ID = uuid.New().String()
		override.CreatedAt = now
	}
	override.AccountID, override.Provider, override.Service = accountID, provider, service
	override.UpdatedAt = now

	overlayOverrideScalars(override, req, present)
	overlayOverrideSlices(override, req, present)

	return override
}

func overlayOverrideScalars(o *config.AccountServiceOverride, req AccountServiceOverrideRequest, present map[string]bool) {
	if present["enabled"] {
		o.Enabled = req.Enabled
	}
	if present["term"] {
		o.Term = req.Term
	}
	if present["payment"] {
		o.Payment = req.Payment
	}
	if present["coverage"] {
		o.Coverage = req.Coverage
	}
	if present["ramp_schedule"] {
		o.RampSchedule = req.RampSchedule
	}
}

func overlayOverrideSlices(o *config.AccountServiceOverride, req AccountServiceOverrideRequest, present map[string]bool) {
	if present["include_engines"] {
		o.IncludeEngines = req.IncludeEngines
	}
	if present["exclude_engines"] {
		o.ExcludeEngines = req.ExcludeEngines
	}
	if present["include_regions"] {
		o.IncludeRegions = req.IncludeRegions
	}
	if present["exclude_regions"] {
		o.ExcludeRegions = req.ExcludeRegions
	}
	if present["include_types"] {
		o.IncludeTypes = req.IncludeTypes
	}
	if present["exclude_types"] {
		o.ExcludeTypes = req.ExcludeTypes
	}
}

// derefInt dereferences an *int pointer, returning 0 for nil.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// derefString dereferences a *string pointer, returning "" for nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// deleteAccountServiceOverride handles DELETE /api/accounts/:id/service-overrides/:provider/:service.
func (h *Handler) deleteAccountServiceOverride(ctx context.Context, req *events.LambdaFunctionURLRequest, providerServicePath string) (any, error) {
	accountID, provider, service, err := parseServiceOverridePath(providerServicePath)
	if err != nil {
		return nil, NewClientError(400, err.Error())
	}

	session, err := h.requirePermission(ctx, req, "delete", "accounts")
	if err != nil {
		return nil, err
	}

	if _, err := h.requireAccountAccess(ctx, session, accountID); err != nil {
		return nil, err
	}

	audit := config.AuditContext{Actor: configAuditActor(session)}
	if err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		return h.config.DeleteAccountServiceOverrideAuditedTx(ctx, tx, accountID, provider, service, audit)
	}); err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return nil, nil
}

// parseServiceOverridePath parses "uuid/service-overrides/provider/service"
// and returns accountID, provider, service.
func parseServiceOverridePath(path string) (accountID, provider, service string, err error) {
	// Strip leading slash if present
	path = strings.TrimPrefix(path, "/")

	parts := strings.Split(path, "/")
	// Expect: [accountID, "service-overrides", provider, service]
	if len(parts) != 4 {
		return "", "", "", fmt.Errorf("invalid service override path: expected uuid/service-overrides/provider/service")
	}

	accountID = parts[0]
	if _, parseErr := uuid.Parse(accountID); parseErr != nil {
		return "", "", "", fmt.Errorf("invalid account ID in path")
	}

	if parts[1] != "service-overrides" {
		return "", "", "", fmt.Errorf("invalid service override path: missing service-overrides segment")
	}

	provider = parts[2]
	service = parts[3]

	if !validAccountProviders[provider] {
		return "", "", "", fmt.Errorf("invalid provider %q: must be one of aws, azure, gcp", provider)
	}
	if service == "" || !serviceNameRegex.MatchString(service) {
		return "", "", "", fmt.Errorf("invalid service name %q: must be 1-64 lowercase alphanumeric characters or hyphens", service)
	}

	return accountID, provider, service, nil
}
