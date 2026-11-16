package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	pkgcommon "github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderReshapeResult counts what one runLadderReshape sweep did per config.
type ladderReshapeResult struct {
	Configs, Refused, Reshaped, Errored int
}

type ladderReshapeOutcome int

const (
	reshapeOutcomeRefused ladderReshapeOutcome = iota
	reshapeOutcomeReshaped
	reshapeOutcomeErrored
)

// reshapeSkipDuplicateAccount refuses a second eligible config for the same
// account and region in one sweep. RunAutoExchange cancels every pending
// ladder-origin exchange before it saves new ones, so a second reshape would
// cancel the first one's pending records and the approval email just sent.
const reshapeSkipDuplicateAccount ladderReshapeRefusal = "another config already reshaped this account and region in this sweep"

var accountIDInText = regexp.MustCompile(`\b\d{12}\b`)

// redactAccountIDs keeps 12-digit AWS account ids out of log lines built from
// provider error text and exchange details.
func redactAccountIDs(s string) string {
	return accountIDInText.ReplaceAllString(s, "<account-id>")
}

// runLadderReshape runs the ladder RI-exchange reshape for the config whose
// latest run passes ladderReshapeEligible. It is deliberately not registered
// as a scheduled task (S15 does that): nothing in production can call it.
//
// Caller contract (S15): ownAccountID is the account the process runs in,
// resolved via STS (resolveLadderAccount), and region the process's AWS
// region; the exchange adapter works on the process's default credentials and
// region, and the eligibility gate refuses any config whose account is not
// ownAccountID. Exactly one sweep may run at a time: there is no lock here.
// At most one reshape runs per (account, region) per sweep, the first eligible
// config by ID, because every reshape cancels the earlier ones' pending
// exchanges (reshapeSkipDuplicateAccount); the others count as Refused.
//
// This is the only place that wires the real write side (ladderCapability with
// true). It calls ReshapeBuffer once per reshaped config and never
// PurchaseLayer: an exchange moves real commitments, so it runs in the exchange
// adapter's mode policy (manual unless the global mode is auto, ladderExchangeMode)
// and under the exchange ledger's caps. One config's error never stops the
// next; a canceled ctx stops the sweep.
func (app *Application) runLadderReshape(ctx context.Context, ownAccountID, region string) (ladderReshapeResult, error) {
	var res ladderReshapeResult
	global, err := app.Config.GetGlobalConfig(ctx)
	if err != nil {
		return res, fmt.Errorf("ladder_reshape: failed to load global config: %w", err)
	}
	if r := ladderReshapeSwitchRefusal(global); r != ladderReshapeOK {
		logging.Infof("ladder_reshape: not running: %s", r)
		return res, nil
	}
	configs, err := app.Config.GetLadderConfigs(ctx)
	if err != nil {
		return res, fmt.Errorf("ladder_reshape: failed to load ladder configs: %w", err)
	}
	configs = slices.Clone(configs)
	slices.SortFunc(configs, func(a, b config.LadderConfigDB) int { return strings.Compare(a.ID, b.ID) })
	claimed := map[string]string{}
	for i := range configs {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("ladder_reshape: stopped before config %s: %w", configs[i].ID, err)
		}
		res.Configs++
		switch app.reshapeOneLadderConfig(ctx, global, &configs[i], ownAccountID, region, claimed) {
		case reshapeOutcomeRefused:
			res.Refused++
		case reshapeOutcomeReshaped:
			res.Reshaped++
		case reshapeOutcomeErrored:
			res.Errored++
		}
	}
	logging.Infof("ladder_reshape done: configs=%d refused=%d reshaped=%d errored=%d", res.Configs, res.Refused, res.Reshaped, res.Errored)
	return res, nil
}

func (app *Application) reshapeOneLadderConfig(ctx context.Context, global *config.GlobalConfig, dbCfg *config.LadderConfigDB, ownAccountID, region string, claimed map[string]string) ladderReshapeOutcome {
	acct, err := app.Config.GetCloudAccount(ctx, dbCfg.CloudAccountID)
	if err != nil {
		logging.Errorf("ladder_reshape: config %s: failed to get cloud account: %s", dbCfg.ID, redactAccountIDs(err.Error()))
		return reshapeOutcomeErrored
	}
	run, err := app.latestLadderRun(ctx, dbCfg.ID)
	if err != nil {
		logging.Errorf("ladder_reshape: config %s: failed to get latest run: %v", dbCfg.ID, err)
		return reshapeOutcomeErrored
	}
	if r := ladderReshapeEligible(global, dbCfg, acct, ownAccountID, run); r != ladderReshapeOK {
		logging.Infof("ladder_reshape: config %s: refused: %s", dbCfg.ID, r)
		return reshapeOutcomeRefused
	}
	rc, err := ladderReshapeConfig(global, dbCfg, run.ID)
	if err != nil {
		logging.Errorf("ladder_reshape: config %s: %v", dbCfg.ID, err)
		return reshapeOutcomeErrored
	}
	key := acct.ExternalID + "/" + region
	if first, dup := claimed[key]; dup {
		logging.Infof("ladder_reshape: config %s: refused: %s (config %s)", dbCfg.ID, reshapeSkipDuplicateAccount, first)
		return reshapeOutcomeRefused
	}
	claimed[key] = dbCfg.ID
	return app.reshapeLadderRun(ctx, dbCfg, run, acct, region, rc)
}

func (app *Application) reshapeLadderRun(ctx context.Context, dbCfg *config.LadderConfigDB, run *config.LadderRunDB, acct *config.CloudAccount, region string, rc pkgladder.BufferReshapeConfig) ladderReshapeOutcome {
	capability, err := app.ladderCapability(ctx, true, region, acct.ExternalID)
	if err != nil {
		logging.Errorf("ladder_reshape: config %s: %s", dbCfg.ID, redactAccountIDs(err.Error()))
		return reshapeOutcomeErrored
	}
	scope := pkgladder.Scope{Provider: pkgcommon.ProviderAWS, AccountID: acct.ExternalID}
	summary, err := capability.ReshapeBuffer(ctx, scope, rc)
	if err != nil {
		// A non-nil error with a populated summary means some exchanges were
		// attempted or may have gone through: money-path, never swallowed.
		logging.Errorf("ladder_reshape: config %s run %s: ReshapeBuffer failed: %s; analyzed=%d reshaped=%d skipped=%d details=[%s]",
			dbCfg.ID, run.ID, redactAccountIDs(err.Error()), summary.Analyzed, summary.Reshaped, summary.Skipped,
			redactAccountIDs(strings.Join(summary.Details, "; ")))
		return reshapeOutcomeErrored
	}
	logging.Infof("ladder_reshape: config %s run %s: analyzed=%d reshaped=%d skipped=%d details=[%s]",
		dbCfg.ID, run.ID, summary.Analyzed, summary.Reshaped, summary.Skipped, redactAccountIDs(strings.Join(summary.Details, "; ")))
	return reshapeOutcomeReshaped
}

// latestLadderRun returns the config's newest run, or nil when it has none.
func (app *Application) latestLadderRun(ctx context.Context, configID string) (*config.LadderRunDB, error) {
	id, err := app.Config.LatestLadderRunID(ctx, configID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return app.Config.GetLadderRun(ctx, id)
}
