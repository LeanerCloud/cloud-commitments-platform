package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) getServiceConfig(ctx context.Context, req *events.LambdaFunctionURLRequest, service string) (any, error) {
	// Require view:config permission. Consistent with getConfig (02-M4).
	if _, err := h.requirePermission(ctx, req, "view", "config"); err != nil {
		return nil, err
	}

	// Validate for path traversal attacks
	if err := validateServicePath(service); err != nil {
		return nil, err
	}

	parts := strings.SplitN(service, "/", 2)
	if len(parts) != 2 {
		return nil, NewClientError(400, "invalid service format, expected: provider/service")
	}

	// Validate provider
	if err := validateProvider(parts[0]); err != nil {
		return nil, err
	}

	cfg, err := h.config.GetServiceConfig(ctx, parts[0], parts[1])
	if err != nil {
		return nil, err
	}

	if cfg == nil {
		return &EmptyServiceConfigResponse{}, nil
	}

	return cfg, nil
}

func (h *Handler) updateServiceConfig(ctx context.Context, req *events.LambdaFunctionURLRequest, service string) (*StatusResponse, error) {
	// Require update:config permission
	session, permErr := h.requirePermission(ctx, req, "update", "config")
	if permErr != nil {
		return nil, permErr
	}
	// Service configs are deployment-wide (provider+service, no account), so
	// they need the same unrestricted-account gate as the global config.
	if err := h.requireGlobalConfigScope(ctx, session); err != nil {
		return nil, err
	}

	// Validate for path traversal attacks
	if err := validateServicePath(service); err != nil {
		return nil, err
	}

	var cfg config.ServiceConfig
	if err := json.Unmarshal([]byte(req.Body), &cfg); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	// created_at and updated_at are read-only row stamps (issue #8). The
	// GET response carries them, so a client that echoes it back must not
	// get them into the merged row; the store takes both from the database.
	cfg.CreatedAt, cfg.UpdatedAt = nil, nil
	// The plan-group fields (#1746) belong only to purchase plan entries; a
	// global service setting never carries them, even on a first save.
	cfg.GroupID, cfg.BoundIdentities, cfg.MaxStepCommitment, cfg.BoundCount = "", nil, nil, 0

	// The URL path names the setting, so a provider or service in the body
	// is replaced by it.
	parts := strings.SplitN(service, "/", 2)
	if len(parts) == 2 {
		cfg.Provider = parts[0]
		cfg.Service = parts[1]
	}

	// Fetch the commitment options before the transaction opens: the merge
	// callback below runs while the transaction holds a pooled connection,
	// so nothing in it may take another one (see commitmentOptsSnapshot).
	combos := h.prefetchCommitmentOptions(ctx, cfg.Provider)

	// Serialized read-merge-write (issue #1488): the store reads the stored
	// row and writes the merged result in one locked transaction, so two
	// admins saving different fields of this row at once cannot both merge
	// onto the same stale base and lose one change. ClientErrors from the
	// merge or validation propagate unchanged.
	_, err := h.config.UpdateServiceConfigAtomic(ctx, cfg.Provider, cfg.Service, func(existing *config.ServiceConfig) (*config.ServiceConfig, error) {
		return mergeAndValidateServiceConfig(existing, cfg, req.Body, combos)
	})
	if err != nil {
		return nil, err
	}

	return &StatusResponse{Status: "updated"}, nil
}

// mergeAndValidateServiceConfig is the read-merge step of a service-config
// PUT, run inside UpdateServiceConfigAtomic's transaction. It overlays each
// field (the scalars and the recommendation filters) only when the body
// carried its key, so a partial PUT never reverts or zeroes a field
// (ramp_schedule, a filter set out-of-band) it didn't mean to touch, then
// validates the merged row, not the partial body, including its commitment
// combo against the prefetched combos. It touches neither the database nor
// the network: the transaction already holds a pooled connection.
func mergeAndValidateServiceConfig(existing *config.ServiceConfig, cfg config.ServiceConfig, body string, combos commitmentOptsSnapshot) (*config.ServiceConfig, error) {
	merged, err := mergeServiceConfig(existing, cfg, body)
	if err != nil {
		return nil, err
	}
	if err := merged.Validate(); err != nil {
		return nil, NewClientError(400, fmt.Sprintf("validation error: %s", err))
	}
	if err := combos.check(merged); err != nil {
		return nil, err
	}
	return &merged, nil
}
