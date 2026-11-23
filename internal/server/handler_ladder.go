package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Cadence thresholds for the per-config self-gate (Q6).
// Each constant is a minimum elapsed time before the same config may run again.
// The 4-hour slack on each bound prevents skipped firings from drifting into
// the next cadence window: a daily at 23:55 still qualifies at 19:55 the next
// day even if the cron fires a few minutes early.
const (
	cadenceDailyMin  = 20 * time.Hour                // daily: 20h (24h - 4h slack)
	cadenceWeeklyMin = 6*24*time.Hour + 20*time.Hour // weekly: 6d 20h (7d - 4h slack)
)

// dataSourceAWSCostExplorer is the provenance tag stamped on every planned
// action in the PR-2 plan-only phase. The baseline is derived from AWS Cost
// Explorer once the CE adapter lands (PR-4); until then the tag documents the
// intended source. Named constant instead of a bare string literal so the
// provenance value has a single definition.
const dataSourceAWSCostExplorer = "aws-ce"

// ladderConfigOutcome is the result of processing a single ladder_config entry
// in the handleLadderRun loop. Using a typed constant avoids bare strings on
// the outcome path.
type ladderConfigOutcome int

const (
	outcomeSkippedDisabled     ladderConfigOutcome = iota
	outcomeSkippedMultiAccount                     // iota = 1
	outcomeSkippedCadence                          // iota = 2
	outcomeErrored                                 // iota = 3
	outcomePlanned                                 // iota = 4
)

// LadderRunResult is the aggregate outcome of one ladder_run task invocation.
// Each counter increments once per ladder_config entry processed.
type LadderRunResult struct {
	Planned             int `json:"planned"`
	SkippedCadence      int `json:"skipped_cadence"`
	SkippedDisabled     int `json:"skipped_disabled"`
	SkippedMultiAccount int `json:"skipped_multi_account"`
	Errored             int `json:"errored"`
}

// record increments the counter that corresponds to the given outcome.
func (r *LadderRunResult) record(o ladderConfigOutcome) {
	switch o {
	case outcomePlanned:
		r.Planned++
	case outcomeSkippedDisabled:
		r.SkippedDisabled++
	case outcomeSkippedMultiAccount:
		r.SkippedMultiAccount++
	case outcomeSkippedCadence:
		r.SkippedCadence++
	case outcomeErrored:
		r.Errored++
	}
}

// handleLadderRun is the top-level orchestrator for the ladder_run scheduled task.
// It iterates over all ladder_configs and runs the planning engine for each
// eligible config. The result struct carries per-outcome counts so the caller
// can surface them in the Lambda log and in future status pages.
//
// plan-only: no PurchaseLayer, ReshapeBuffer, email, or approval tokens.
func (app *Application) handleLadderRun(ctx context.Context) (*LadderRunResult, error) {
	// Global kill-switch check.
	globalCfg, err := app.Config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: failed to load global config: %w", err)
	}
	if !globalCfg.LadderingEnabled {
		logging.Warnf("ladder_run: laddering_enabled=false in global config, skipping all configs")
		return &LadderRunResult{}, nil
	}

	// Parse global-default term and payment option. Fail loud: money-path
	// decisions must never proceed with an invalid/unknown option.
	global, err := ladderGlobalTerms(globalCfg)
	if err != nil {
		return nil, err
	}

	// Load the ladder_config rows. GetLadderConfigs returns at most
	// config.MaxListLimit rows, newest first, disabled ones included; we
	// filter Enabled=false here so the counter is visible (SkippedDisabled).
	allConfigs, err := app.Config.GetLadderConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: failed to load ladder configs: %w", err)
	}
	if len(allConfigs) >= config.MaxListLimit {
		// Rows past the cap were not read, so their configs are not run.
		logging.Warnf("ladder_run: GetLadderConfigs returned %d rows, its %d-row cap; ladder configs may be truncated",
			len(allConfigs), config.MaxListLimit)
	}
	if len(allConfigs) == 0 {
		logging.Infof("ladder_run: no ladder_config rows found, nothing to do")
		return &LadderRunResult{}, nil
	}

	// Q1: single-account only. Resolve the Lambda's own AWS account ID STRICTLY:
	// this ID scopes which configs run, so an unresolved account must abort the
	// whole task (fail loud) rather than fall through and skip every config as
	// multi-account, which would report a false success on a money path.
	ownAccountID, region, err := app.resolveLadderAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_run: could not resolve caller AWS account for the single-account gate: %w", err)
	}

	now := time.Now().UTC()
	result := app.runLadderConfigs(ctx, allConfigs, ownAccountID, region, global, now)

	logging.Infof("ladder_run done: planned=%d skipped_cadence=%d skipped_disabled=%d skipped_multi_account=%d errored=%d",
		result.Planned, result.SkippedCadence, result.SkippedDisabled, result.SkippedMultiAccount, result.Errored)
	return result, nil
}

// resolveLadderAccount resolves the caller AWS account ID and region for the
// single-account gate. It delegates to the injected LadderAccountResolver (used
// by tests) or the default STS-backed resolver. Any error is returned to the
// caller so handleLadderRun can fail loud.
func (app *Application) resolveLadderAccount(ctx context.Context) (accountID, region string, err error) {
	if app.LadderAccountResolver != nil {
		return app.LadderAccountResolver(ctx)
	}
	return defaultLadderAccountResolver(ctx)
}

// defaultLadderAccountResolver resolves the Lambda's own AWS account ID via STS
// STRICTLY, like resolveAccountID in the RI-exchange path: this returns an
// error when the account cannot be determined. The ladder path uses the account ID to SCOPE
// which configs run, so a fabricated/"unknown" value is unsafe: it would skip
// every config as multi-account and report a false success.
func defaultLadderAccountResolver(ctx context.Context) (accountID, region string, err error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", "", fmt.Errorf("load AWS config: %w", err)
	}
	return resolveLadderIdentity(ctx, awsCfg)
}

// resolveLadderIdentity validates the region and resolves the caller account ID
// from an already-loaded aws.Config. Split from defaultLadderAccountResolver so
// the region short-circuit is unit-testable without loading real credentials or
// calling STS.
//
// The region is validated FIRST: an empty region would make NewFromAWSConfig
// reject EVERY config later, turning the whole task into a silent all-Errored
// no-op that still returns success. Failing loud here aborts handleLadderRun
// before any config is processed (same silent-degradation class as the missing
// STS account ID below).
func resolveLadderIdentity(ctx context.Context, awsCfg aws.Config) (accountID, region string, err error) {
	if awsCfg.Region == "" {
		return "", "", fmt.Errorf("AWS region is empty; set AWS_REGION / AWS_DEFAULT_REGION or a region in the shared config")
	}
	stsClient := sts.NewFromConfig(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", "", fmt.Errorf("resolve caller account via STS: %w", err)
	}
	if identity.Account == nil || *identity.Account == "" {
		return "", "", fmt.Errorf("STS GetCallerIdentity returned no account ID")
	}
	return *identity.Account, awsCfg.Region, nil
}

// runLadderConfigs processes every ladder_config entry, isolating each one:
// an error on one config increments the Errored counter and continues to the
// next, so a single broken config never aborts the whole run. Extracted from
// handleLadderRun so the multi-config isolation behavior is unit-testable
// without the AWS SDK account-resolution path in handleLadderRun.
func (app *Application) runLadderConfigs(
	ctx context.Context,
	configs []config.LadderConfigDB,
	ownAccountID, region string,
	global ladderTerms,
	now time.Time,
) *LadderRunResult {
	result := &LadderRunResult{}
	for i := range configs {
		result.record(app.processOneLadderConfig(ctx, &configs[i], ownAccountID, region, global, now))
	}
	return result
}

// processOneLadderConfig applies all eligibility gates for a single
// ladder_config entry and runs the planning engine when eligible. It is
// extracted from handleLadderRun to keep both functions within the project's
// cyclomatic-complexity limit (10).
func (app *Application) processOneLadderConfig(
	ctx context.Context,
	dbCfg *config.LadderConfigDB,
	ownAccountID, region string,
	global ladderTerms,
	now time.Time,
) ladderConfigOutcome {
	if !dbCfg.Enabled {
		logging.Warnf("ladder_run: config %s: enabled=false, skipping", dbCfg.ID)
		return outcomeSkippedDisabled
	}

	// Resolve the cloud account to get the 12-digit AWS account number (Q2).
	cloudAcct, err := app.Config.GetCloudAccount(ctx, dbCfg.CloudAccountID)
	if err != nil {
		logging.Errorf("ladder_run: config %s: failed to get cloud account %s: %v", dbCfg.ID, dbCfg.CloudAccountID, err)
		return outcomeErrored
	}
	if cloudAcct == nil {
		logging.Errorf("ladder_run: config %s: cloud account %s not found in DB", dbCfg.ID, dbCfg.CloudAccountID)
		return outcomeErrored
	}

	// Q1: skip configs that belong to a different AWS account.
	if cloudAcct.ExternalID != ownAccountID {
		logging.Warnf("ladder_run: config %s: cloud account external_id=%q != lambda account=%q: skipped (multi_account_unsupported)", dbCfg.ID, cloudAcct.ExternalID, ownAccountID)
		return outcomeSkippedMultiAccount
	}

	// Cadence self-gate: skip if a run already started within the window. Fail
	// CLOSED on a lookup error: if we cannot tell whether a recent run exists,
	// running anyway risks a duplicate money action, so count the config Errored
	// (visible in LadderRunResult.Errored) instead of proceeding silently.
	within, reason, err := ladderWithinCadenceWindow(ctx, app.Config, dbCfg, now)
	if err != nil {
		logging.Errorf("ladder_run: config %s: cadence lookup failed, skipping to avoid a possible double-run: %v", dbCfg.ID, err)
		return outcomeErrored
	}
	if within {
		logging.Infof("ladder_run: config %s: %s", dbCfg.ID, reason)
		return outcomeSkippedCadence
	}

	// Build the planning capability for this account: its write side is always disabled.
	capability, err := app.planningCapability(ctx, region, cloudAcct.ExternalID)
	if err != nil {
		logging.Errorf("ladder_run: config %s: %v", dbCfg.ID, err)
		return outcomeErrored
	}

	// Run the plan engine and persist the result.
	if err := app.executeLadderRun(ctx, dbCfg, capability, cloudAcct.ExternalID, global, now); err != nil {
		logging.Errorf("ladder_run: config %s: planning failed: %v", dbCfg.ID, err)
		return outcomeErrored
	}
	return outcomePlanned
}

// executeLadderRun runs the planning engine for a single ladder_config and
// persists the result as a ladder_runs row (+ ladder_tranches audit rows).
//
// It is split from handleLadderRun so tests can inject a hermetic
// LadderCapability (fake) and a mock store without touching AWS or PostgreSQL.
//
// L5 netting: before calling Allocate, the handler fetches the config's in-flight
// commitment (scheduled tranches still fireable, plus fired tranches not yet
// resolved). The Allocate engine subtracts this from the raw gap so each daily
// run plans only what is still needed, preventing pile-up when prior tranches
// have not yet been bought. Tranches completed after `now` are counted too (see
// below): a safe under-buy, and a deliberate deviation from go's InFlight doc.
//
// Units: the provider's existing and expiring coverage arrive in commitment
// dollars and are first converted to on-demand-equivalent dollars at the
// observed rate of the commitments held when the provider reports one, else at
// the lower of the constant-pair and global-pair new-purchase rates
// (ladderRates, ladderConversionRates, ladderLayerStatesOnDemand).
// New tranches are always sized at the constant-pair rate. Coverage on a layer with no rate
// cannot be converted, so the gap cannot be sized: the run is persisted as a
// Hold-only run with reason_code missing_discount_rate that buys nothing
// (planMissingRateHold), not an Errored run. Allocate sizes the gap in
// on-demand-equivalent dollars and emits tranches in commitment dollars with
// the rate they were sized with. Rows are stored as basis=commitment plus that
// rate; the store nets each row back to on-demand-equivalent dollars by its
// own rate.
//
// Persistence is APPEND-ONLY (see persistLadderRun): every run inserts its new
// scheduled tranches and NEVER supersedes existing ones. Netting already
// subtracted the config's existing scheduled tranches from the gap, so each run
// appends exactly the delta needed to reach target-E: a Hold appends nothing
// and preserves the live ramp with its original fire dates; a drift-up run
// appends the top-up so total scheduled commitment converges to target.
func (app *Application) executeLadderRun(
	ctx context.Context,
	dbCfg *config.LadderConfigDB,
	capability pkgladder.LadderCapability,
	accountID string,
	global ladderTerms,
	now time.Time,
) error {
	// Convert the DB config row to the engine's typed LadderConfig.
	engineCfg, err := ladderConfigToEngine(dbCfg, accountID)
	if err != nil {
		return fmt.Errorf("config conversion: %w", err)
	}
	scope := engineCfg.Scope

	layers := capability.SupportedLayers()
	if refusal := ladderComputeCapRefusal(dbCfg, layers); refusal != nil {
		return refusal
	}

	// Collect read-side data. All three calls are independent; errors are
	// fail-loud (not silently defaulted to empty).
	baseline, err := readLadderBaseline(ctx, capability, engineCfg)
	if err != nil {
		return err
	}

	layerStates, err := capability.GetLayerStates(ctx, scope)
	if err != nil {
		return fmt.Errorf("GetLayerStates: %w", err)
	}

	// Rates are provider data, fetched outside the transaction. No capability or
	// a fetch error fails the run: the allocator never gets a guessed rate. An
	// empty map is valid and yields a Hold-only run. Observed rates are
	// optional: a capability without them converts at the marginal rates. One
	// local plan pair feeds both the rates and the planner, so the sizing rate
	// and the stored term and payment cannot diverge.
	plan := ladderPlanTerms
	rates, base, err := ladderRates(ctx, capability, scope, plan, global, layerStates)
	if err != nil {
		return err
	}

	// Providers report existing coverage in commitment dollars; Allocate and
	// the plan snapshot compare it with an on-demand-equivalent target (#946).
	// Existing coverage converts at observed rates where reported, else at the
	// lower of the constant-pair and global-pair rates; the planner below keeps
	// the constant-pair rates for new tranches. Layers holding coverage with no rate at all come back in
	// missing; the run is then a Hold that buys nothing and says why.
	conv, observed, err := ladderConversionRates(ctx, capability, scope, base)
	if err != nil {
		return fmt.Errorf("observed commitment rates: %w", err)
	}
	logObservedOverrides(dbCfg.ID, observed, base)
	layerStates, missing, err := ladderLayerStatesOnDemand(layerStates, conv)
	if err != nil {
		return fmt.Errorf("existing coverage: %w", err)
	}

	// L5 netting runs inside the persist transaction (PlanAndSaveLadderRun):
	// the store reads the config's in-flight commitment (scheduled tranches
	// still within config.LadderFireGrace plus fired, unresolved ones) under
	// the per-config lock, plus tranches completed after `now` (settled term).
	// `now` was captured before the GetLayerStates read above, so a tranche that
	// completed after that read is in neither E nor in-flight without the
	// settled term; one already in E is counted twice (a safe under-buy). The
	// provider reads above stay outside the transaction: no lock is held across
	// network I/O.
	in := &ladderRunPlanner{
		dbCfg: dbCfg, engineCfg: engineCfg, baseline: baseline, layerStates: layerStates, rates: rates,
		layers: layers, term: plan.term, paymentOpt: plan.payment, global: global, now: now, missing: missing,
	}
	savedRun, err := app.Config.PlanAndSaveLadderRun(ctx, dbCfg.ID, now, in.build)
	if err != nil {
		return fmt.Errorf("persist ladder run: %w", err)
	}
	in.logPersisted(savedRun)
	return nil
}

// ladderPlanJSONAction is a JSON-serializable form of pkgladder.PlannedAction.
// PlannedAction.AmountUSDPerHour is *big.Rat (not JSON-friendly); this DTO
// converts it to *float64 at the boundary.
type ladderPlanJSONAction struct {
	Action        string   `json:"action"`
	Layer         string   `json:"layer"`
	Family        string   `json:"family,omitempty"`
	AmountUSDHr   *float64 `json:"amount_usd_hr,omitempty"` // nil for Hold/Reshape
	Term          string   `json:"term,omitempty"`
	PaymentOption string   `json:"payment_option,omitempty"`
	Rationale     string   `json:"rationale"`
	DataSources   []string `json:"data_sources,omitempty"`
}

// ladderPlanJSONDTO is a JSON-serializable form of pkgladder.LadderPlan.
// The *big.Rat monetary fields are converted to *float64 so they round-trip
// cleanly through JSONB without binary encoding.
type ladderPlanJSONDTO struct {
	// ReasonCode is set only when the run could not size its gap (see
	// ladderRunPlanner.reasonCode); ordinary runs omit it.
	ReasonCode    string                  `json:"reason_code,omitempty"`
	Scope         pkgladder.Scope         `json:"scope"`
	GeneratedAt   time.Time               `json:"generated_at"`
	TargetUSDHr   *float64                `json:"target_usd_hr,omitempty"`
	ExistingUSDHr *float64                `json:"existing_usd_hr,omitempty"`
	GapUSDHr      *float64                `json:"gap_usd_hr,omitempty"`
	Actions       []ladderPlanJSONAction  `json:"actions"`
	Baseline      pkgladder.UsageBaseline `json:"baseline"`
}

// marshalLadderPlan serializes a LadderPlan to JSON for storage in the
// plan JSONB column. *big.Rat fields are converted to *float64 at this
// boundary; nil stays nil. reasonCode is stored as reason_code when non-empty.
func marshalLadderPlan(plan *pkgladder.LadderPlan, reasonCode string) (json.RawMessage, error) {
	actions := make([]ladderPlanJSONAction, 0, len(plan.Actions))
	for i := range plan.Actions {
		a := &plan.Actions[i]
		action := ladderPlanJSONAction{
			Action:        string(a.Action),
			Layer:         string(a.Layer),
			Family:        a.Family,
			AmountUSDHr:   ratToFloat64Ptr(a.AmountUSDPerHour),
			Term:          string(a.Term),
			PaymentOption: string(a.PaymentOption),
			Rationale:     a.Rationale,
			DataSources:   a.DataSources,
		}
		actions = append(actions, action)
	}
	dto := ladderPlanJSONDTO{
		ReasonCode:    reasonCode,
		Scope:         plan.Scope,
		GeneratedAt:   plan.GeneratedAt,
		TargetUSDHr:   ratToFloat64Ptr(plan.TargetUSDPerHour),
		ExistingUSDHr: ratToFloat64Ptr(plan.ExistingUSDPerHour),
		GapUSDHr:      ratToFloat64Ptr(plan.GapUSDPerHour),
		Actions:       actions,
		Baseline:      plan.Baseline,
	}
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// ladderTermFromYears converts a GlobalConfig.DefaultTerm (integer years) to
// a typed pkgladder.Term. Fails loud on any unrecognized value so money-path
// decisions never proceed with an unknown commitment term.
func ladderTermFromYears(years int) (pkgladder.Term, error) {
	switch years {
	case 1:
		return pkgladder.Term1Year, nil
	case 3:
		return pkgladder.Term3Year, nil
	default:
		return "", fmt.Errorf("unsupported default_term=%d years (allowed: 1, 3)", years)
	}
}
