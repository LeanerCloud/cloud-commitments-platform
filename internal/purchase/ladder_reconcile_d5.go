package purchase

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// ladderD5Exempt reports whether a failed tranche must not disable its config
// because the operator's own switches most likely caused the refusal. Only a
// no_submission verdict can be exempt: a revoke, cancel or expiry always
// disables. The execution keeps no typed refusal reason, so this guesses from
// the current global config; the name returned is for the log.
func ladderD5Exempt(g *config.GlobalConfig, layer ladder.LayerType, reason string) (string, bool) {
	if reason != ladderReasonNoSubmission {
		return "", false
	}
	switch {
	case !g.LadderingEnabled:
		return "laddering_enabled", true
	case !g.LadderExecutionEnabled:
		return "ladder_execution_enabled", true
	case g.RequireDifferentApprover:
		return "require_different_approver", true
	case layer == ladder.LayerConvertibleRI && g.OfferingClass != "" && g.OfferingClass != "convertible":
		return "offering_class", true
	}
	return "", false
}

// applyLadderD5Tx disables the failed tranche's ladder config (decision D5) in
// the caller's transaction and reports whether it flipped the row. The global
// config is read here, inside the transaction, and only for a no_submission
// verdict. An error rolls the tranche move back so the next sweep retries; it
// is never swallowed, because that would skip the disable for good.
// config.ErrLadderConfigBusy is returned unwrapped by DisableLadderConfigTx and
// must reach the caller as an error: a nil return would commit an aborted tx.
func applyLadderD5Tx(ctx context.Context, store LadderReconcileStore, tx pgx.Tx, tr config.LadderTrancheDB, reason string) (bool, error) {
	if tr.ConfigID == nil {
		logging.Warnf("ladder tranche %s failed (%s) but its config is gone: nothing to disable", tr.ID, reason)
		return false, nil
	}
	if reason == ladderReasonNoSubmission {
		g, err := store.GetGlobalConfigTx(ctx, tx)
		if err != nil {
			return false, fmt.Errorf("read global config for ladder tranche %s: %w", tr.ID, err)
		}
		if g == nil {
			return false, fmt.Errorf("global config unavailable for ladder tranche %s", tr.ID)
		}
		if sw, exempt := ladderD5Exempt(g, tr.LayerType, reason); exempt {
			logging.Infof("ladder tranche %s failed (%s) while %s forbids purchases: config %s stays enabled", tr.ID, reason, sw, *tr.ConfigID)
			return false, nil
		}
	}
	disabled, err := store.DisableLadderConfigTx(ctx, tx, *tr.ConfigID)
	if err != nil {
		return false, err
	}
	if disabled {
		logging.Warnf("ladder config %s disabled: tranche %s failed (%s)", *tr.ConfigID, tr.ID, reason)
	}
	return disabled, nil
}
