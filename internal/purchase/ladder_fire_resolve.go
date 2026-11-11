package purchase

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	// ladderFireClaimLimit is how many tranches one config may claim per sweep.
	// It is also the resolve cap, so claimed <= resolved holds and the trimmed
	// count (resolved - claimed) is exact.
	ladderFireClaimLimit = 50
	// ladderDueListCap is how many due rows the pre-claim read returns.
	ladderDueListCap = 200
)

// LadderUnresolved is a due tranche the sweep will not claim, with the reason.
type LadderUnresolved struct {
	TrancheID string
	Layer     ladder.LayerType
	Reason    string
}

// ladderResolution is what the resolve step decided for one config.
type ladderResolution struct {
	IDs        []string // tranches to hand to the claim, in due order.
	Unresolved []LadderUnresolved
	Due        int // rows the pre-claim read returned.
	Capped     bool
}

// resolveLadderTranches decides, before the claim, which due rows the sweep may
// claim. A row is refused when the claim's own predicate would not take it,
// when it exceeds the config's per-run cap (a cap lowered after planning), or
// when its instrument cannot be built (including a corrupt stored rate). A bad
// row never fails the config: it is returned as unresolved so the good rows
// still go through. Amounts come from the tranche only.
func resolveLadderTranches(rows []config.LadderDueRow, cfg *config.LadderConfigDB, registry *LadderInstrumentRegistry, region string) ladderResolution {
	res := ladderResolution{Due: len(rows), IDs: make([]string, 0, len(rows))}
	for i := range rows {
		tr := rows[i].Tranche
		if len(res.IDs) >= ladderFireClaimLimit {
			res.Capped = true
			break
		}
		if reason := ladderTrancheRefusal(&rows[i], cfg, registry, region); reason != "" {
			res.Unresolved = append(res.Unresolved, LadderUnresolved{TrancheID: tr.ID, Layer: tr.LayerType, Reason: reason})
			continue
		}
		res.IDs = append(res.IDs, tr.ID)
	}
	return res
}

// ladderTrancheRefusal returns why one due row cannot be claimed, or "".
func ladderTrancheRefusal(row *config.LadderDueRow, cfg *config.LadderConfigDB, registry *LadderInstrumentRegistry, region string) string {
	tr := row.Tranche
	basisErr := config.RequireLadderCommitmentBasis(&tr)
	switch {
	case !row.SQLClaimable:
		return "not claimable: layer, offering, basis, run window, run mode or canceled run"
	case basisErr != nil:
		// The SQL predicate cannot see an unparsable rate; the claim's scan
		// would fail on it and roll back the whole config's claim.
		return fmt.Sprintf("corrupt tranche: %v", basisErr)
	case cfg.MaxHourlyCommitPerRun == nil:
		return "config has no per-run cap"
	case tr.AmountUSDHr > *cfg.MaxHourlyCommitPerRun:
		return "above the per-run cap"
	}
	if _, err := registry.Build(LadderBuildInput{Tranche: tr, CloudAccountID: cfg.CloudAccountID, Region: region}); err != nil {
		return fmt.Sprintf("unbuildable tranche: %v", err)
	}
	return ""
}

// logLadderResolution writes the per-config drain-guard line and one line per
// unresolved tranche. due > 0 with nothing resolved is an ERROR: a resolver
// bug or outage would otherwise let the overdue sweep cancel every tranche as
// expired a day later with no error anywhere.
func logLadderResolution(configID string, res ladderResolution) {
	logging.Infof("ladder_fire: config %s due=%d resolved=%d unresolved=%d", configID, res.Due, len(res.IDs), len(res.Unresolved))
	for _, u := range res.Unresolved {
		logging.Warnf("ladder_fire: config %s tranche %s (%s) unresolved: %s", configID, u.TrancheID, u.Layer, u.Reason)
	}
	if res.Due > 0 && len(res.IDs) == 0 {
		logging.Errorf("ladder_fire: config %s due=%d resolved=0: no due tranche can be claimed; they expire after %s", configID, res.Due, config.LadderFireGrace)
	}
	if res.Due >= ladderDueListCap || res.Capped {
		logging.Warnf("ladder_fire: config %s has more due rows than one sweep handles (listed %d, claim limit %d)", configID, res.Due, ladderFireClaimLimit)
	}
}
