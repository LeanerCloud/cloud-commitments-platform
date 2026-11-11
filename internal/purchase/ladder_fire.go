package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// LadderFireResult counts what one sweep did. Skipped counts configs a gate
// refused, Errored configs whose reads or claim failed; Due, Unresolved,
// Claimed and Trimmed sum over the configs that reached the resolve step.
type LadderFireResult struct {
	Configs, Skipped, Due, Unresolved, Claimed, Trimmed, Errored int
	// Announced counts executions stamped after a send; the rest of a claim is
	// NoticeFailed (send error), NoticeDeferred (no time left) or StampFailed.
	Announced, NoticeFailed, NoticeDeferred, StampFailed, SettleFailed int
}

// ladderFirePlan is what the gates let one config sweep with.
type ladderFirePlan struct {
	global       *config.GlobalConfig
	acct         *config.CloudAccount
	layers       []ladder.LayerType
	ceilingMicro int64
}

// RunLadderFire claims every due, resolvable ladder tranche into a scheduled
// execution, config by config. It is not registered as a scheduled task and
// buys nothing by itself: a claimed execution is only fireable once the notice
// step stamps it, and the fire sweep's recheck refuses it until then. The
// caller must treat a claimed but unannounced execution as a lost notice: it
// nets against the ceiling for a day, then the recheck refuses it and the
// reconciler disables the config (a re-enable, never a spend).
//
// Every refusal creates nothing and logs its reason. The global switches are
// re-read per config, so a switch flipped mid-sweep stops the remaining
// configs. A config that errors is counted and the sweep goes on; the sweep
// returns an error when any config errored, so the scheduler records a failed
// run. Due tranches that cannot be resolved are never claimed, and if none of
// a config's due tranches resolves the claim is skipped with an ERROR, because
// the overdue sweep would otherwise cancel them as expired a day later. The
// due read is capped, so rows behind unresolvable heads can starve; the WARN
// on a full read says so.
func RunLadderFire(ctx context.Context, deps LadderFireDeps) (LadderFireResult, error) {
	var res LadderFireResult
	if deps.Store == nil || deps.Registry == nil || deps.Now == nil {
		return res, errors.New("ladder_fire: store, registry and clock are required")
	}
	configs, err := loadLadderFireConfigs(ctx, deps.Store)
	if err != nil {
		return res, err
	}
	for i := range configs {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("ladder_fire: %w", err)
		}
		res.Configs++
		if err := fireLadderConfig(ctx, deps, &configs[i], &res); err != nil {
			res.Errored++
			logging.Errorf("ladder_fire: config %s: %v", configs[i].ID, err)
		}
	}
	if res.Errored > 0 {
		return res, fmt.Errorf("ladder_fire: %d config(s) errored", res.Errored)
	}
	return res, nil
}

func loadLadderFireConfigs(ctx context.Context, store LadderFireStore) ([]config.LadderConfigDB, error) {
	configs, err := store.GetLadderConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ladder_fire: load ladder configs: %w", err)
	}
	if len(configs) >= config.MaxListLimit {
		logging.Warnf("ladder_fire: GetLadderConfigs returned %d rows, its %d-row cap; configs may be truncated", len(configs), config.MaxListLimit)
	}
	return configs, nil
}

// ladderCeilingRefusal turns the ceiling read into a skip reason; a read error
// other than the missing column is returned as an error.
func ladderCeilingRefusal(ceiling *int64, err error) (string, error) {
	switch {
	case errors.Is(err, config.ErrLadderCeilingUnavailable):
		return "the sweep ceiling is not available until the database migration is applied", nil
	case err != nil:
		return "", fmt.Errorf("read sweep ceiling: %w", err)
	case ceiling == nil || *ceiling <= 0:
		return "the sweep ceiling is not set", nil
	}
	return "", nil
}

// prepareLadderFire runs the gates for one config. A non-empty refusal means
// skip; an error means the config errored.
func prepareLadderFire(ctx context.Context, deps LadderFireDeps, cfg *config.LadderConfigDB) (*ladderFirePlan, string, error) {
	g, err := deps.Store.GetGlobalConfig(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read global config: %w", err)
	}
	if r := ladderGlobalRefusal(g, deps); r != "" {
		return nil, r, nil
	}
	acct, err := deps.Store.GetCloudAccount(ctx, cfg.CloudAccountID)
	if err != nil {
		return nil, "", fmt.Errorf("read cloud account %s: %w", cfg.CloudAccountID, err)
	}
	if r := ladderConfigRefusal(cfg, acct, deps); r != "" {
		return nil, r, nil
	}
	ceiling, err := deps.Store.GetLadderSweepCeiling(ctx, cfg.ID)
	if r, rErr := ladderCeilingRefusal(ceiling, err); r != "" || rErr != nil {
		return nil, r, rErr
	}
	layers := ladderSweepLayers(deps.Registry.Layers(), g)
	if len(layers) == 0 {
		return nil, "no buildable layer is allowed", nil
	}
	return &ladderFirePlan{global: g, acct: acct, layers: layers, ceilingMicro: *ceiling}, "", nil
}

// fireLadderConfig sweeps one config: gates, ceiling, list, resolve, claim.
func fireLadderConfig(ctx context.Context, deps LadderFireDeps, cfg *config.LadderConfigDB, res *LadderFireResult) error {
	plan, refusal, err := prepareLadderFire(ctx, deps, cfg)
	if err != nil {
		return err
	}
	if refusal != "" {
		res.Skipped++
		logging.Warnf("ladder_fire: config %s skipped: %s", cfg.ID, refusal)
		return nil
	}
	now := deps.Now()
	rows, err := deps.Store.ListDueLadderTranches(ctx, cfg.ID, now, ladderDueListCap, plan.layers)
	if err != nil {
		return fmt.Errorf("list due tranches: %w", err)
	}
	rv := resolveLadderTranches(rows, cfg, deps.Registry, deps.Region)
	logLadderResolution(cfg.ID, rv)
	res.Due += rv.Due
	res.Unresolved += len(rv.Unresolved)
	if len(rv.IDs) == 0 {
		return nil
	}
	execs := map[string]*config.PurchaseExecution{}
	create := ladderClaimCreate(deps.Store, deps.Registry, cfg, deps.Region, plan.global.GetPurchaseDelay(), now, execs)
	claimed, err := deps.Store.ClaimDueLadderTranches(ctx, cfg.ID, now, ladderFireClaimLimit,
		config.LadderClaimFilter{Layers: plan.layers, TrancheIDs: rv.IDs, CeilingMicros: plan.ceilingMicro}, create)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	res.Claimed += len(claimed)
	logLadderTrim(cfg.ID, plan.ceilingMicro, rv, len(claimed), res)
	announceClaimed(ctx, deps, plan, claimed, execs, res)
	return nil
}

// logLadderTrim reports tranches the ceiling kept back. A short claim is never
// an error: the trimmed rows stay scheduled for the next sweep. The claim trims
// against the ceiling minus what the last day already fired, so an empty claim
// is an ERROR only when the head tranche alone exceeds the ceiling; otherwise
// the day's budget is simply used up.
func logLadderTrim(configID string, ceilingMicro int64, rv ladderResolution, claimed int, res *LadderFireResult) {
	trimmed := len(rv.IDs) - claimed
	if trimmed <= 0 {
		return
	}
	res.Trimmed += trimmed
	switch {
	case claimed > 0:
		logging.Warnf("ladder_fire: config %s claimed %d, %d held back by the sweep ceiling (%d micro-dollars/hr)", configID, claimed, trimmed, ceilingMicro)
	case rv.HeadUSDHr*1e6 > float64(ceilingMicro):
		logging.Errorf("ladder_fire: config %s claimed 0 of %d resolved: the head tranche of %.6f USD/hr exceeds the ceiling (%d micro-dollars/hr); an oversized head blocks later rows and expires after %s",
			configID, len(rv.IDs), rv.HeadUSDHr, ceilingMicro, config.LadderFireGrace)
	default:
		logging.Warnf("ladder_fire: config %s claimed 0 of %d resolved: the ceiling budget is used by executions of the last %s", configID, len(rv.IDs), config.LadderCeilingWindow)
	}
}
