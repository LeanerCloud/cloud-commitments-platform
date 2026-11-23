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

// serviceConfigOverlayKeys are the JSON fields the Settings UI edits: the four
// scalars (enabled, term, payment, coverage) and the recommendation filters.
// They are overlaid onto the existing row only when the
// request body actually contains the key, so a partial PUT from a non-UI
// client (or an older UI build) never zeroes a filter the operator set
// elsewhere. RampSchedule is intentionally absent: it is still set out-of-band
// and the UI does not own it.
var serviceConfigOverlayKeys = []string{
	"enabled", "term", "payment", "coverage",
	"include_engines", "exclude_engines",
	"include_regions", "exclude_regions",
	"include_types", "exclude_types",
	"min_count",
}

// mergeServiceConfig overlays the UI-editable fields from cfg onto the stored
// row existing. Every field (Enabled, Term, Payment, Coverage and the
// include/exclude engines/regions/types and min_count filters) is overlaid
// only when the request body actually carried the corresponding key (per
// presentKeys), so a partial PUT cannot silently revert or wipe a field it
// did not mean to touch. body is the raw request JSON used to detect which
// keys are present.
//
// A nil existing means no stored row: cfg is returned unchanged. existing is
// not modified.
func mergeServiceConfig(existing *config.ServiceConfig, cfg config.ServiceConfig, body string) (config.ServiceConfig, error) {
	if existing == nil {
		return cfg, nil // new record — no existing fields to preserve
	}

	present, perr := presentKeys(body, serviceConfigOverlayKeys)
	if perr != nil {
		return cfg, perr
	}
	merged := *existing
	overlayPresentScalarFields(&merged, &cfg, present)
	overlayPresentFilterFields(&merged, &cfg, present)
	return merged, nil
}

// presentKeys returns the subset of keys that appear as top-level fields in the
// JSON body, matched case-insensitively like encoding/json binds them. A malformed body is reported as a client error so the caller
// returns 400 rather than silently overlaying nothing.
func presentKeys(body string, keys []string) (map[string]bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	present := make(map[string]bool, len(keys))
	for _, k := range keys {
		if bodyHasKey(raw, k) {
			present[k] = true
		}
	}
	return present, nil
}

// overlayPresentScalarFields copies enabled, term, payment and coverage from src
// onto dst only when the body carried the key, so a client that sends just the
// field the user edited (the Settings page, #539) cannot revert the others. A
// present zero value (enabled:false, coverage:0) still applies.
func overlayPresentScalarFields(dst, src *config.ServiceConfig, present map[string]bool) {
	if present["enabled"] {
		dst.Enabled = src.Enabled
	}
	if present["term"] {
		dst.Term = src.Term
	}
	if present["payment"] {
		dst.Payment = src.Payment
	}
	if present["coverage"] {
		dst.Coverage = src.Coverage
	}
}

// overlayPresentFilterFields copies each filter field from src onto dst only
// when its JSON key was present in the request body. Split out of
// mergeServiceConfig to keep that function under the cyclomatic-complexity
// gate.
func overlayPresentFilterFields(dst, src *config.ServiceConfig, present map[string]bool) {
	if present["include_engines"] {
		dst.IncludeEngines = src.IncludeEngines
	}
	if present["exclude_engines"] {
		dst.ExcludeEngines = src.ExcludeEngines
	}
	if present["include_regions"] {
		dst.IncludeRegions = src.IncludeRegions
	}
	if present["exclude_regions"] {
		dst.ExcludeRegions = src.ExcludeRegions
	}
	if present["include_types"] {
		dst.IncludeTypes = src.IncludeTypes
	}
	if present["exclude_types"] {
		dst.ExcludeTypes = src.ExcludeTypes
	}
	if present["min_count"] {
		dst.MinCount = src.MinCount
	}
}
