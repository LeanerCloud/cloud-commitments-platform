// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// sourceCloud returns the cloud where CUDly is currently running, read from
// the CUDLY_SOURCE_CLOUD env var (set by Terraform). Falls back to "aws".
func sourceCloud() string {
	if v := os.Getenv("CUDLY_SOURCE_CLOUD"); v != "" {
		return v
	}
	return "aws"
}

// Configuration handlers.
func (h *Handler) getConfig(ctx context.Context, req *events.LambdaFunctionURLRequest) (*ConfigResponse, error) {
	// Require view:config permission. Every other read handler in the package
	// pairs the route-level AuthUser gate with this explicit permission check;
	// config GET must be consistent (02-M4).
	if _, err := h.requirePermission(ctx, req, "view", "config"); err != nil {
		return nil, err
	}

	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, err
	}

	services, err := h.config.ListServiceConfigs(ctx)
	if err != nil {
		return nil, err
	}

	resp := &ConfigResponse{
		Global:      globalCfg,
		Services:    services,
		SourceCloud: sourceCloud(),
	}

	// SourceIdentity contains the host cloud account ID (AWS account number,
	// Azure tenant ID, etc.). Only expose it to admin sessions so that
	// non-admin users cannot extract the cloud identity of the CUDly host
	// account (issue #407).
	if _, adminErr := h.requireAdmin(ctx, req); adminErr == nil {
		resp.SourceIdentity = h.resolveSourceIdentity(ctx)
	}

	return resp, nil
}

func (h *Handler) updateConfig(ctx context.Context, req *events.LambdaFunctionURLRequest) (*StatusResponse, error) {
	// Require update:config permission
	session, permErr := h.requirePermission(ctx, req, "update", "config")
	if permErr != nil {
		return nil, permErr
	}

	present, parseErr := globalConfigWriteFields(req.Body)
	if parseErr != nil {
		return nil, parseErr
	}
	gracePresent := bodyHasKey(present, "grace_period_days")
	modeSent := riExchangeModeSent(present)
	providersSent := bodyHasKey(present, "enabled_providers")
	if err := h.requireGlobalConfigScope(ctx, session); err != nil {
		return nil, err
	}
	// Resolved before the transaction: the closure below runs while it holds a
	// pooled connection and must not need a second one (issue #1517).
	grants := h.resolveArmingGrants(ctx, req)

	// Serialized read-modify-write: the store loads the stored config and
	// applies this closure under an advisory-locked transaction, then upserts
	// the result in the same tx. json.Unmarshal only assigns keys present in the
	// body, so any field ABSENT is preserved (present -> updated, absent ->
	// preserved) for EVERY field, with no per-field enumeration. Doing the read
	// and write in one locked tx prevents two concurrent partial PUTs (e.g. the
	// laddering kill-switch toggle and a Settings save) from reading the same
	// stale base and losing each other's update. The closure returns a
	// ClientError(400) on a bad body or validation failure, which the store
	// propagates unchanged; DB/transport errors surface as 500. A changed
	// default reaches the service configs inside the same transaction.
	var switchBefore, ladderBefore bool
	merged, err := h.config.UpdateGlobalConfigAtomic(ctx, func(existing *config.GlobalConfig) error {
		// Snapshot before the wholesale unmarshal: this body writes onto the
		// entire GlobalConfig, so ri_exchange_mode / ri_exchange_enabled reach
		// the scheduler through this endpoint exactly as they do through the
		// dedicated PUT /api/ri-exchange/config (issue #1765). Taken inside the
		// closure so the comparison reads the same advisory-locked row the
		// write lands on.
		before := riExchangeArmStateOf(existing)
		switchBefore = existing.AutoPurchaseEnabled
		ladderBefore = existing.LadderExecutionEnabled
		delayBefore := existing.PurchaseDelayHours
		thresholdBefore := existing.RIExchangeUtilizationThreshold

		// grace_period_days is a map: json.Unmarshal into a non-nil map MERGES
		// keys (an omitted key can never be deleted). When the caller sends the
		// key, nil the stored map first so the body's map REPLACES it wholesale
		// (present -> replace); when absent, leave it to preserve.
		if gracePresent {
			existing.GracePeriodDays = nil
		}
		if uErr := json.Unmarshal([]byte(req.Body), existing); uErr != nil {
			return NewClientError(400, "invalid request body")
		}
		thresholdWritten := riThresholdWritten(present, thresholdBefore, existing.RIExchangeUtilizationThreshold)
		if vErr := validateGlobalConfigWrite(existing, modeSent || existing.RIExchangeMode != before.mode, providersSent, thresholdWritten, h.refusesDelay(present, delayBefore, existing.PurchaseDelayHours)); vErr != nil {
			return vErr
		}
		if gErr := requireRIExchangeAutoModeGrant(before, riExchangeArmStateOf(existing), grants.riExchange); gErr != nil {
			return gErr
		}
		return requireSwitchGrants(ctx, session, grants.purchases, switchBefore, ladderBefore, existing)
	})
	if err != nil {
		return nil, err
	}
	logAutoPurchaseSwitchChange(ctx, session, switchBefore, merged)
	logLadderExecutionSwitchChange(ctx, session, ladderBefore, merged)

	return &StatusResponse{Status: "updated"}, nil
}

// globalConfigWriteFields validates the body before any DB work and returns
// the supplied top-level fields, including explicit nulls. The write uses this
// presence map to replace grace_period_days and validate ri_exchange_mode.
func globalConfigWriteFields(body string) (map[string]json.RawMessage, error) {
	if !json.Valid([]byte(body)) {
		return nil, NewClientError(400, "invalid request body")
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &present); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	return present, nil
}

// bodyHasKey reports whether the body carries the key under any casing, as
// json.Unmarshal matches keys case-insensitively. An explicit null counts.
func bodyHasKey(present map[string]json.RawMessage, name string) bool {
	for key := range present {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// riExchangeModeSent reports whether the body writes ri_exchange_mode. A JSON
// null does not: json.Unmarshal leaves the stored value in place for it.
func riExchangeModeSent(present map[string]json.RawMessage) bool {
	return bodyWritesKey(present, "ri_exchange_mode")
}

// bodyWritesKey is bodyHasKey minus JSON null, which json.Unmarshal ignores.
func bodyWritesKey(present map[string]json.RawMessage, name string) bool {
	for key, raw := range present {
		if strings.EqualFold(key, name) && string(raw) != "null" {
			return true
		}
	}
	return false
}

// riThresholdKey is the global-config field holding the RI exchange
// utilization threshold.
const riThresholdKey = "ri_exchange_utilization_threshold"

// riThresholdWritten reports whether this write sets the utilization threshold:
// the body carries a non-null key, or decoding changed the stored value (a later
// duplicate key of null hides an earlier write from the presence map).
func riThresholdWritten(present map[string]json.RawMessage, before, after float64) bool {
	return bodyWritesKey(present, riThresholdKey) || after != before
}

// validateGlobalConfigWrite validates the merged config as a 400.
//
// ri_exchange_mode is checked when explicitly set or changed by JSON decoding
// (including duplicate keys followed by null), with
// the same manual|auto rule PUT /api/ri-exchange/config applies. Checking the
// merged value instead would refuse every partial save, including the
// laddering kill-switch toggle, on a row stored before this check with a mode
// such as "Auto". Such a row keeps its value until a caller sends a valid one,
// and armed() keeps treating it as auto. Runs before the arming gates so a
// malformed write never reveals grant state.
func validateGlobalConfigWrite(cfg *config.GlobalConfig, modeSent, providersSent, thresholdWritten, delayRefused bool) error {
	// Presence-based, not in Validate: a fresh install stores [] and partial
	// saves that omit the key must keep working.
	if providersSent && len(cfg.EnabledProviders) == 0 {
		return NewClientError(400, "enabled_providers must list at least one provider")
	}
	if modeSent && !config.IsValidRIExchangeMode(cfg.RIExchangeMode) {
		return NewClientError(400, "ri_exchange_mode must be 'manual' or 'auto'")
	}
	// A threshold the caller writes is held to the stricter rule PUT
	// /api/ri-exchange/config applies (0 would be stored as 95); Validate below
	// stays lenient because it also runs on stored rows.
	if thresholdWritten {
		if err := config.ValidateRIExchangeUtilizationThreshold(riThresholdKey, cfg.RIExchangeUtilizationThreshold); err != nil {
			return NewClientError(400, err.Error())
		}
	}
	if err := cfg.Validate(); err != nil {
		return NewClientError(400, fmt.Sprintf("validation error: %s", err))
	}
	if delayRefused && cfg.PurchaseDelayHours > 0 {
		return NewClientError(400, `this deployment has no fire sweep (FIRE_SWEEP_SCHEDULED=false), so purchase_delay_hours must be 0: send PUT /api/config with {"purchase_delay_hours": 0}`)
	}
	return nil
}

// refusesDelay reports whether this write must keep the delay at 0: the
// deployment has no fire sweep and the body writes purchase_delay_hours, or
// the decode changed the stored value (a later duplicate key of null hides
// an earlier write from the presence map). Presence-based, so a stored delay
// does not block an unrelated partial save.
func (h *Handler) refusesDelay(present map[string]json.RawMessage, before, after int) bool {
	return h.fireSweepOff && (bodyWritesKey(present, "purchase_delay_hours") || after != before)
}

// requireSwitchGrants chains the arming gates of the two money-spending
// switches: auto-purchase, then ladder execution. purchasesGrant is the
// pre-resolved execute:purchases outcome (armingGrants.purchases).
func requireSwitchGrants(ctx context.Context, session *Session, purchasesGrant error, autoBefore, ladderBefore bool, cfg *config.GlobalConfig) error {
	if err := requireAutoPurchaseSwitchGrant(ctx, session, purchasesGrant, autoBefore, cfg); err != nil {
		return err
	}
	return requireLadderExecutionSwitchGrant(ctx, session, purchasesGrant, ladderBefore, cfg)
}

const globalConfigScopeMsg = "permission denied: global configuration requires unrestricted account access"

func (h *Handler) requireGlobalConfigScope(ctx context.Context, session *Session) error {
	return h.requireGlobalScope(ctx, session, auth.ActionUpdate, auth.ResourceConfig, globalConfigScopeMsg)
}

// requireGlobalScope gates a deployment-wide action for a session that already
// holds action:resource (callers keep their own validation order by calling
// requirePermission first). It needs an unrestricted account scope and a grant
// with no constraints on any dimension, checked on the key and the owner for
// user API keys. msg is the 403 text for a restricted account scope.
func (h *Handler) requireGlobalScope(ctx context.Context, session *Session, action, resource, msg string) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return err
	}
	if !scope.AllowsAll() {
		return NewClientError(403, msg)
	}
	return h.requirePermissionConstraints(ctx, session, action, resource,
		[]auth.PermissionConstraints{auth.GlobalConstraints()})
}

// requireGlobalPermission is requirePermission followed by requireGlobalScope.
func (h *Handler) requireGlobalPermission(ctx context.Context, req *events.LambdaFunctionURLRequest, action, resource, msg string) error {
	session, err := h.requirePermission(ctx, req, action, resource)
	if err != nil {
		return err
	}
	return h.requireGlobalScope(ctx, session, action, resource, msg)
}

// checkCommitmentOptionCombo rejects saves that carry a (term, payment)
// combination we've dynamically confirmed the cloud doesn't sell. Returns
// nil when: no probe service is wired, the service hasn't persisted data
// yet (absent data → fall through to the frontend's hardcoded rules),
// the save is neither AWS nor an Azure Savings Plan, or the combo is valid.
//
// Any other Validate error (a store read failing, a timeout) fails the save
// with a 503 instead of letting it through: the check exists for API
// callers that bypass the frontend's rules, so waving the save through on an
// error would persist an unsupported combo that fails only at purchase time
// (issue #243, audit finding A02-022). The caller can retry.
func (h *Handler) checkCommitmentOptionCombo(ctx context.Context, cfg config.ServiceConfig) error {
	if h.commitmentOpts == nil || !commitmentGated(cfg) {
		return nil
	}
	if cfg.Provider == "azure" {
		return h.prefetchCommitmentOptions(ctx, cfg.Provider).check(cfg)
	}
	ok, err := h.commitmentOpts.Validate(ctx, cfg.Provider, cfg.Service, cfg.Term, cfg.Payment)
	if errors.Is(err, commitmentopts.ErrNoData) {
		return nil
	}
	if err != nil {
		return commitmentComboUnverifiable(err)
	}
	if !ok {
		return commitmentComboUnsupported(cfg)
	}
	return nil
}

// commitmentGated reports whether a save of cfg is checked against probe data:
// AWS services, and the Azure Savings Plan. Every other Azure service stays
// ungated. Like the rest of the platform it assumes Azure bills in USD.
func commitmentGated(cfg config.ServiceConfig) bool {
	if cfg.Term <= 0 || cfg.Payment == "" {
		return false
	}
	switch cfg.Provider {
	case "aws":
		return true
	case "azure":
		_, ok := azureGateService(cfg.Service)
		return ok
	}
	return false
}

// azureGateService maps an Azure service name to the key its probe row is
// stored under, routing retired Savings Plans spellings to the same row.
func azureGateService(service string) (string, bool) {
	if _, ok := common.NormalizeSavingsPlansServiceType(common.ServiceType(service)); !ok {
		return "", false
	}
	return string(common.ServiceSavingsPlansAll), true
}

// commitmentComboUnverifiable is the 503 a save gets when the commitment
// options could not be read, so the combo could not be checked.
func commitmentComboUnverifiable(err error) error {
	logging.Errorf("commitment-option validation error (refusing save): %v", err)
	return NewClientError(503, "could not validate the commitment term and payment option; try again")
}

// commitmentComboUnsupported is the 400 a save gets for a (term, payment)
// combination the probe data says the service does not sell.
func commitmentComboUnsupported(cfg config.ServiceConfig) error {
	return NewClientError(400, fmt.Sprintf(
		"%s does not support %dyr %s commitments",
		cfg.Service, cfg.Term, cfg.Payment,
	))
}

// commitmentOptsSnapshot is the commitment-option data a service-config PUT
// checks its merged row against. It is fetched before the save opens its
// transaction (issue #1488): Get reads the store through pooled connections
// and can probe AWS on a cold cache, and doing that while the transaction
// holds a pooled connection deadlocks the pool once as many saves run at
// once as the pool has connections (the Settings page saves its rows in
// parallel). check is pure, so it is safe inside the transaction.
type commitmentOptsSnapshot struct {
	opts commitmentopts.Options
	err  error
	// loaded is false when there is nothing to check against: no service is
	// wired, the save is not AWS, or no probe data exists (ErrNoData).
	loaded bool
}

// prefetchCommitmentOptions fetches the snapshot for a save of provider. A
// read error is kept rather than returned, so it fails only a save whose
// merged row actually needs the check, as checkCommitmentOptionCombo does.
func (h *Handler) prefetchCommitmentOptions(ctx context.Context, provider string) commitmentOptsSnapshot {
	if h.commitmentOpts == nil {
		return commitmentOptsSnapshot{}
	}
	switch provider {
	case "aws":
		opts, err := h.commitmentOpts.Get(ctx)
		if errors.Is(err, commitmentopts.ErrNoData) {
			return commitmentOptsSnapshot{}
		}
		return commitmentOptsSnapshot{opts: opts, err: err, loaded: true}
	case "azure":
		byService, ok, err := h.commitmentOpts.FreshAzureOptions(ctx)
		if err == nil && !ok {
			return commitmentOptsSnapshot{}
		}
		return commitmentOptsSnapshot{opts: commitmentopts.Options{"azure": byService}, err: err, loaded: true}
	}
	return commitmentOptsSnapshot{}
}

// check applies checkCommitmentOptionCombo's rules to cfg against the
// snapshot, without touching the database or the network.
func (s commitmentOptsSnapshot) check(cfg config.ServiceConfig) error {
	if !s.loaded || !commitmentGated(cfg) {
		return nil
	}
	if s.err != nil {
		return commitmentComboUnverifiable(s.err)
	}
	service := cfg.Service
	if cfg.Provider == "azure" {
		service, _ = azureGateService(cfg.Service)
	}
	if !commitmentopts.ValidateCombo(s.opts, cfg.Provider, service, cfg.Term, cfg.Payment) {
		return commitmentComboUnsupported(cfg)
	}
	return nil
}
