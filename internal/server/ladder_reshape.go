package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderReshapeRefusal names why a ladder reshape must not run. The zero
// value means eligible.
type ladderReshapeRefusal string

const (
	ladderReshapeOK           ladderReshapeRefusal = ""
	reshapeRefuseNoGlobal     ladderReshapeRefusal = "global config unavailable"
	reshapeRefuseLadderingOff ladderReshapeRefusal = "laddering_enabled is off"
	reshapeRefuseExecutionOff ladderReshapeRefusal = "ladder_execution_enabled is off"
	reshapeRefuseExchangeOff  ladderReshapeRefusal = "ri_exchange_enabled is off"
	// Stricter than the plan's four-eyes downgrade to manual: the fire gate
	// refuses while require_different_approver is on too, so a reshape does
	// too. Deliberate.
	reshapeRefuseFourEyes          ladderReshapeRefusal = "require_different_approver is on (ladder reshapes have no approver)"
	reshapeRefuseNoConfig          ladderReshapeRefusal = "ladder config unavailable"
	reshapeRefuseConfigDisabled    ladderReshapeRefusal = "config is disabled"
	reshapeRefuseProvider          ladderReshapeRefusal = "provider is not aws"
	reshapeRefuseMode              ladderReshapeRefusal = "mode is not auto_approve"
	reshapeRefuseNoAccount         ladderReshapeRefusal = "cloud account not found"
	reshapeRefuseOwnAccountUnknown ladderReshapeRefusal = "the account this process runs in is unknown"
	reshapeRefuseForeignAccount    ladderReshapeRefusal = "cloud account is not the account this process runs in"
	reshapeRefuseNoRun             ladderReshapeRefusal = "no ladder run to reshape for"
	reshapeRefuseRunStatus         ladderReshapeRefusal = "latest ladder run status does not allow a reshape"
	reshapeRefuseNoReshapeAction   ladderReshapeRefusal = "latest ladder run has no reshape action"
)

// ladderReshapeEligible returns why the config's latest run must not trigger
// an RI exchange reshape, or ladderReshapeOK. It is pure: the caller supplies
// the account, the process's own account id and the latest run. Fail closed:
// anything missing or unreadable refuses.
func ladderReshapeEligible(g *config.GlobalConfig, cfg *config.LadderConfigDB, acct *config.CloudAccount, ownAccountID string, run *config.LadderRunDB) ladderReshapeRefusal {
	if r := ladderReshapeSwitchRefusal(g); r != ladderReshapeOK {
		return r
	}
	if r := ladderReshapeConfigRefusal(cfg, acct, ownAccountID); r != ladderReshapeOK {
		return r
	}
	return ladderReshapeRunRefusal(run)
}

func ladderReshapeSwitchRefusal(g *config.GlobalConfig) ladderReshapeRefusal {
	switch {
	case g == nil:
		return reshapeRefuseNoGlobal
	case !g.LadderingEnabled:
		return reshapeRefuseLadderingOff
	case !g.LadderExecutionEnabled:
		return reshapeRefuseExecutionOff
	case !g.RIExchangeEnabled:
		return reshapeRefuseExchangeOff
	case g.RequireDifferentApprover:
		return reshapeRefuseFourEyes
	}
	return ladderReshapeOK
}

func ladderReshapeConfigRefusal(cfg *config.LadderConfigDB, acct *config.CloudAccount, ownAccountID string) ladderReshapeRefusal {
	switch {
	case cfg == nil:
		return reshapeRefuseNoConfig
	case !cfg.Enabled:
		return reshapeRefuseConfigDisabled
	case cfg.Provider != string(common.ProviderAWS):
		return reshapeRefuseProvider
	case cfg.Mode != string(pkgladder.ModeAutoApprove):
		return reshapeRefuseMode
	case acct == nil:
		return reshapeRefuseNoAccount
	case ownAccountID == "":
		return reshapeRefuseOwnAccountUnknown
	case acct.ExternalID != ownAccountID:
		return reshapeRefuseForeignAccount
	}
	return ladderReshapeOK
}

func ladderReshapeRunRefusal(run *config.LadderRunDB) ladderReshapeRefusal {
	switch {
	case run == nil || run.ID == "":
		return reshapeRefuseNoRun
	case !ladderReshapeRunStatusAllowed(run.Status):
		return reshapeRefuseRunStatus
	case !ladderPlanHasReshape(run.Plan):
		return reshapeRefuseNoReshapeAction
	}
	return ladderReshapeOK
}

// ladderReshapeRunStatusAllowed is an allow-list over the latest run's status:
// the pre-#554 legacy canceled spelling and unknown values are in no case, so
// they are refused without any normalization. planned (auto_approve runs are
// born live), approved, executing and completed keep the plan's reshape action
// live. awaiting_approval is not yet approved; canceled, expired and failed
// mean an operator stopped the run or it never took effect.
func ladderReshapeRunStatusAllowed(status pkgladder.RunStatus) bool {
	switch status {
	case pkgladder.RunStatusPlanned, pkgladder.RunStatusApproved,
		pkgladder.RunStatusExecuting, pkgladder.RunStatusCompleted:
		return true
	}
	return false
}

// ladderPlanHasReshape reports whether the stored plan JSON (written by
// marshalLadderPlan) lists a reshape action. Undecodable or empty plans have
// none.
func ladderPlanHasReshape(plan json.RawMessage) bool {
	var dto struct {
		Actions []struct {
			Action string `json:"action"`
		} `json:"actions"`
	}
	if len(plan) == 0 || json.Unmarshal(plan, &dto) != nil {
		return false
	}
	for _, a := range dto.Actions {
		if a.Action == string(pkgladder.ActionReshape) {
			return true
		}
	}
	return false
}

// ladderReshapeConfig builds the library reshape config from the standalone
// exchange caps in the global settings and the config row's threshold and
// lookback. It refuses a missing, non-finite or non-positive cap, an empty run
// id (a nil LadderRunID would cancel the standalone task's pendings) and an
// unusable threshold or lookback. DryRun is always false.
func ladderReshapeConfig(g *config.GlobalConfig, cfg *config.LadderConfigDB, runID string) (pkgladder.BufferReshapeConfig, error) {
	if g == nil || cfg == nil {
		return pkgladder.BufferReshapeConfig{}, errors.New("ladder reshape config: global and ladder config are required")
	}
	if runID == "" {
		return pkgladder.BufferReshapeConfig{}, errors.New("ladder reshape config: ladder run id is required")
	}
	perExchange, daily := g.RIExchangeMaxPerExchangeUSD, g.RIExchangeMaxDailyUSD
	if _, err := pkgladder.RequireCap("ri_exchange_max_per_exchange_usd", &perExchange); err != nil {
		return pkgladder.BufferReshapeConfig{}, fmt.Errorf("ladder reshape config: %w", err)
	}
	if _, err := pkgladder.RequireCap("ri_exchange_max_daily_usd", &daily); err != nil {
		return pkgladder.BufferReshapeConfig{}, fmt.Errorf("ladder reshape config: %w", err)
	}
	if t := cfg.BufferUtilizationThreshold; !(t > 0 && t <= 100) {
		return pkgladder.BufferReshapeConfig{}, fmt.Errorf("ladder reshape config: buffer_utilization_threshold must be in (0, 100], got %g", t)
	}
	if cfg.LookbackDays <= 0 {
		return pkgladder.BufferReshapeConfig{}, fmt.Errorf("ladder reshape config: lookback_days must be > 0, got %d", cfg.LookbackDays)
	}
	return pkgladder.BufferReshapeConfig{
		MaxPaymentPerExchangeUSD: &perExchange,
		MaxPaymentDailyUSD:       &daily,
		LadderRunID:              &runID,
		UtilizationThresholdPct:  cfg.BufferUtilizationThreshold,
		LookbackDays:             cfg.LookbackDays,
		DryRun:                   false,
	}, nil
}
