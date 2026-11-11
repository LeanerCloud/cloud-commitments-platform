package purchase

import (
	"context"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// ladderMinRevokeWindow is the shortest purchase delay the sweep accepts: the
// delay is the window in which an operator can revoke a claimed execution.
const ladderMinRevokeWindow = 24 * time.Hour

// LadderFireStore is the slice of the Postgres store the ladder fire sweep uses.
type LadderFireStore interface {
	GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error)
	GetLadderConfigs(ctx context.Context) ([]config.LadderConfigDB, error)
	GetCloudAccount(ctx context.Context, id string) (*config.CloudAccount, error)
	GetLadderSweepCeiling(ctx context.Context, configID string) (*int64, error)
	ListDueLadderTranches(ctx context.Context, configID string, asOf time.Time, limit int, layers []ladder.LayerType) ([]config.LadderDueRow, error)
	ClaimDueLadderTranches(ctx context.Context, configID string, asOf time.Time, limit int, filter config.LadderClaimFilter,
		create func(ctx context.Context, tx pgx.Tx, tranche config.LadderTrancheDB) (string, error)) ([]config.LadderTrancheDB, error)
	SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *config.PurchaseExecution) error
	SaveExecutionIfStatus(ctx context.Context, execution *config.PurchaseExecution, expected []string) error
	WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	LockLadderTrancheChainTx(ctx context.Context, tx pgx.Tx, trancheID string) ([]config.LadderChainNode, error)
	CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error)
	TransitionLadderTrancheTx(ctx context.Context, tx pgx.Tx, id string, from, to ladder.TrancheStatus, reason string) (bool, error)
}

// LadderFireDeps is everything one ladder fire sweep needs. Notifier is nil
// when no real sender is configured; the sweep then refuses to claim, because
// an execution nobody was told about must not exist.
type LadderFireDeps struct {
	Store        LadderFireStore
	Registry     *LadderInstrumentRegistry
	Notifier     ScheduledNotifier
	OwnAccountID string // external id of the AWS account the process runs in.
	Region       string
	DashboardURL string
	Now          func() time.Time
}

// ladderSwitchRefusal checks the global switches and returns why the sweep
// must not claim, or "" when they allow it.
func ladderSwitchRefusal(g *config.GlobalConfig) string {
	switch {
	case g == nil:
		return "global config unavailable"
	case !g.LadderingEnabled:
		return "laddering_enabled is off"
	case !g.LadderExecutionEnabled:
		return "ladder_execution_enabled is off"
	case g.RequireDifferentApprover:
		return "require_different_approver is on (ladder executions have no approver)"
	}
	return ""
}

// ladderNoticeRefusal checks that the revoke window is real and the notice
// deliverable: a window shorter than a day, no notifier, no recipient or no
// dashboard link would leave an execution nobody can revoke in time.
func ladderNoticeRefusal(g *config.GlobalConfig, deps LadderFireDeps) string {
	switch {
	case g.GetPurchaseDelay() < ladderMinRevokeWindow:
		return "purchase_delay_hours is under 24, so there is no revoke window"
	case deps.Notifier == nil:
		return "no notifier is configured"
	case g.NotificationEmail == nil || strings.TrimSpace(*g.NotificationEmail) == "":
		return "notification_email is empty"
	case strings.TrimSpace(deps.DashboardURL) == "":
		return "dashboard URL is empty, so the notice has no revoke link"
	}
	return ""
}

// ladderGlobalRefusal is the first refusal among the global checks, or "".
func ladderGlobalRefusal(g *config.GlobalConfig, deps LadderFireDeps) string {
	if r := ladderSwitchRefusal(g); r != "" {
		return r
	}
	return ladderNoticeRefusal(g, deps)
}

// ladderConfigRefusal checks one ladder config and its account, or returns "".
func ladderConfigRefusal(cfg *config.LadderConfigDB, acct *config.CloudAccount, deps LadderFireDeps) string {
	switch {
	case !cfg.Enabled:
		return "config is disabled"
	case cfg.Provider != string(common.ProviderAWS):
		return "provider " + cfg.Provider + " is not aws"
	case cfg.Mode != string(ladder.ModeAutoApprove):
		return "mode " + cfg.Mode + " is not auto_approve"
	case cfg.MaxHourlyCommitPerRun == nil:
		return "max_hourly_commit_per_run is not set"
	case acct == nil:
		return "cloud account not found"
	case deps.OwnAccountID == "":
		return "the account this process runs in is unknown"
	case acct.ExternalID != deps.OwnAccountID:
		return "cloud account is not the account this process runs in"
	}
	return ""
}

// ladderSweepLayers drops the Reserved Instance layer unless the offering
// class is convertible (or unset): a Standard RI cannot be exchanged, so a
// registered RI builder must never buy one through this sweep.
func ladderSweepLayers(layers []ladder.LayerType, g *config.GlobalConfig) []ladder.LayerType {
	if g.OfferingClass == "" || g.OfferingClass == "convertible" {
		return layers
	}
	out := make([]ladder.LayerType, 0, len(layers))
	for _, l := range layers {
		if l != ladder.LayerConvertibleRI {
			out = append(out, l)
		}
	}
	return out
}
