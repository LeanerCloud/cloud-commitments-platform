package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// redshiftRedriveHorizon is how long after a lineage's first dispatch a
// successor may still be stamped as within horizon.
const redshiftRedriveHorizon = 24 * time.Hour

// isRedshiftRec reports whether rec dispatches to the AWS Redshift purchase
// path. It goes through mapServiceSlug, the table the executor dispatches on.
func isRedshiftRec(rec config.RecommendationRecord) bool {
	if rec.Provider != "" && rec.Provider != string(common.ProviderAWS) {
		return false
	}
	svc, ok := mapServiceSlug(rec.Service)
	return ok && (svc == common.ServiceRedshift || svc == common.ServiceDataWarehouse)
}

func hasRedshiftRec(exec *config.PurchaseExecution) bool {
	for i := range exec.Recommendations {
		if exec.Recommendations[i].Selected && isRedshiftRec(exec.Recommendations[i]) {
			return true
		}
	}
	return false
}

// afterRedshiftStampHook runs inside the gate transaction right after the
// stamp. Tests set it to observe the lock wait; it is nil in production.
var afterRedshiftStampHook func()

// Fixed refusal texts: the NULL-account wildcard can match another account's row,
// so they never name another execution. errnotes.go allowlists each one.
const (
	redshiftConflictRefusal       = "refusing to dispatch: another open purchase of the same Redshift reservation (region, node type, term, payment, count) may still complete, so wait for it to finish and then retry"
	redshiftHorizonRefusal        = "refusing to dispatch: this Redshift purchase was first dispatched more than 24h ago, so check the account in AWS and start a new purchase"
	redshiftNoStampRefusal        = "refusing to dispatch: this Redshift purchase has no first dispatch time recorded, so check the account in AWS and start a new purchase"
	redshiftFailedConflictRefusal = "refusing to dispatch: another purchase of the same Redshift reservation (region, node type, term, payment, count) failed and may still be retried, so retry or settle that purchase, or start this one at least 24h after its first dispatch"
	redshiftNoJournalRefusal      = "refusing to dispatch: this Redshift retry needs the purchase attempt journal, which is not configured, so check the account in AWS and start a new purchase"
	redshiftRecentBuyRefusal      = "refusing to dispatch: another purchase of the same Redshift reservation (region, node type, term, payment, count) completed too close to this purchase's first dispatch for AWS to tell the two apart, so start a new purchase at least 10 minutes after the other one completed"
	redshiftSettleRefusal         = "refusing to dispatch: another attempt of this Redshift purchase ended less than 10 minutes ago and may have bought, so wait 10 minutes and then retry"
)

// redshiftAdoptionWindow mirrors cloud-commitments-go's 5m Redshift dedupe skew
// (providers/aws/services/redshift/dedupe.go redshiftClockSkew) plus a 5m clock margin.
const redshiftAdoptionWindow = 10 * time.Minute

// errRedshiftPastHorizon marks a stamp older than the horizon. It is never
// beforeSubmission: an old stamp proves an earlier dispatch may have landed.
var errRedshiftPastHorizon = errors.New(redshiftHorizonRefusal)

// redshiftTuples lists the tuples of the selected Redshift recs.
func redshiftTuples(exec *config.PurchaseExecution) []config.RedshiftTuple {
	var tuples []config.RedshiftTuple
	for i := range exec.Recommendations {
		r := &exec.Recommendations[i]
		if r.Selected && isRedshiftRec(*r) {
			tuples = append(tuples, config.RedshiftTuple{
				Region: r.Region, NodeType: r.ResourceType, Term: r.Term,
				Payment: r.Payment, Count: r.Count, CloudAccountID: r.CloudAccountID,
			})
		}
	}
	return tuples
}

// stampRedshiftDispatch is the dispatch gate for a Redshift execution. In one
// transaction it locks each tuple, stamps the lineage's first dispatch (which
// becomes RequestedAt for every dispatch and fan-out account row), and refuses
// when another open lineage holds a matching tuple; the refusal rolls the
// stamp back. Other executions pass untouched. A refusal at attempt 0 or by a
// conflict provably submitted nothing; a stamp-less or past-horizon successor
// may have, so that refusal is a plain error.
// The gate needs READ COMMITTED: each statement sees a fresh snapshot after the
// lock wait. now() is the tx start, so a waiter's stamp predates its wait.
func (m *Manager) stampRedshiftDispatch(ctx context.Context, exec *config.PurchaseExecution) error {
	if !hasRedshiftRec(exec) {
		return nil
	}
	var res config.StampResult
	err := m.config.WithTx(ctx, func(tx pgx.Tx) error {
		var gateErr error
		res, gateErr = m.redshiftGate(ctx, tx, exec)
		return gateErr
	})
	if err != nil {
		var refused *beforeSubmissionError
		if exec.RetryAttemptN == 0 && !errors.As(err, &refused) && !errors.Is(err, errRedshiftPastHorizon) {
			return beforeSubmission(err)
		}
		return err
	}
	at := res.At.UTC()
	exec.FirstDispatchAt = &at
	return nil
}

func (m *Manager) redshiftGate(ctx context.Context, tx pgx.Tx, exec *config.PurchaseExecution) (config.StampResult, error) {
	tuples := redshiftTuples(exec)
	if err := m.config.LockRedshiftTuplesTx(ctx, tx, tuples); err != nil {
		return config.StampResult{}, err
	}
	res, err := m.config.StampFirstDispatchTx(ctx, tx, exec.ExecutionID, redshiftRedriveHorizon)
	if err != nil {
		return res, err
	}
	if res.At == nil {
		logging.Warnf("Redshift gate: execution %s has no first dispatch time and none can be minted", exec.ExecutionID)
		return res, errors.New(redshiftNoStampRefusal)
	}
	if !res.WithinHorizon {
		logging.Warnf("Redshift gate: execution %s first dispatched at %s, past the horizon", exec.ExecutionID, res.At.UTC().Format(time.RFC3339))
		return res, errRedshiftPastHorizon
	}
	if afterRedshiftStampHook != nil {
		afterRedshiftStampHook()
	}
	family, _, _ := strings.Cut(idempotencyLineageKey(exec), ":")
	conflicts, err := m.config.FindOpenRedshiftLineageConflictsTx(ctx, tx, exec.ExecutionID, family, tuples, exec.CloudAccountID, redshiftRedriveHorizon, *res.At, redshiftAdoptionWindow, redshiftRetrySettle)
	if err != nil {
		return res, err
	}
	return res, conflictRefusal(exec.ExecutionID, dropPersistentFailures(conflicts))
}

// conflictRefusal wraps pickConflict's refusal as before-submission; the other id goes to the log only.
func conflictRefusal(executionID string, conflicts []config.RedshiftLineageConflict) error {
	refusal, culprit, ok := pickConflict(conflicts)
	if !ok {
		return nil
	}
	logging.Warnf("Redshift gate: execution %s refused (%s, execution %s, %s)", executionID, refusal, culprit.ExecutionID, culprit.Status)
	return beforeSubmission(errors.New(refusal))
}

// conflictRules is the refusal precedence: another family's recent buy, any open lineage, a
// same-family attempt inside the settle. Failed rows only fall through to pickConflict's last case.
var conflictRules = []struct {
	match   func(config.RedshiftLineageConflict) bool
	refusal string
}{
	{func(c config.RedshiftLineageConflict) bool { return !c.SameFamily && redshiftFinishedStatus(c.Status) }, redshiftRecentBuyRefusal},
	{func(c config.RedshiftLineageConflict) bool {
		return c.Status != config.StatusFailed && (!c.SameFamily || c.Status == "running")
	}, redshiftConflictRefusal},
	{func(c config.RedshiftLineageConflict) bool { return c.SameFamily }, redshiftSettleRefusal},
}

// pickConflict returns the refusal for the first rule any conflict matches, then failed rows only.
func pickConflict(conflicts []config.RedshiftLineageConflict) (string, config.RedshiftLineageConflict, bool) {
	for _, rule := range conflictRules {
		for _, c := range conflicts {
			if rule.match(c) {
				return rule.refusal, c, true
			}
		}
	}
	if allFailed(conflicts) {
		return redshiftFailedConflictRefusal, conflicts[0], true
	}
	return "", config.RedshiftLineageConflict{}, false
}

// allFailed reports a non-empty conflict list made only of failed rows.
func allFailed(conflicts []config.RedshiftLineageConflict) bool {
	for _, c := range conflicts {
		if c.Status != config.StatusFailed {
			return false
		}
	}
	return len(conflicts) > 0
}

// dropPersistentFailures removes failed rows Retry can never re-drive (an operator-fix error or a
// refused provider): they would hold the tuple for the whole horizon with no way out.
func dropPersistentFailures(conflicts []config.RedshiftLineageConflict) []config.RedshiftLineageConflict {
	kept := make([]config.RedshiftLineageConflict, 0, len(conflicts))
	for _, c := range conflicts {
		if !c.SameFamily && c.Status == config.StatusFailed && (ResolveOpsHint(c.Error) != "" || retryRefusedForRecs(c)) {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

func retryRefusedForRecs(c config.RedshiftLineageConflict) bool {
	return RedriveRefusalReason(&config.PurchaseExecution{Recommendations: c.Recommendations}) != ""
}

// redshiftFinishedStatus reports the statuses whose node may already sit in AWS.
func redshiftFinishedStatus(status string) bool {
	switch status {
	case config.StatusCompleted, config.StatusPartiallyCompleted, config.StatusRevocationRequested:
		return true
	}
	return false
}

// withDispatchTime defaults RequestedAt to now. A Redshift rec must arrive
// with the lineage stamp: defaulting would silently move go's dedupe window.
func withDispatchTime(rec config.RecommendationRecord, opts common.PurchaseOptions) (common.PurchaseOptions, error) {
	if !opts.RequestedAt.IsZero() {
		return opts, nil
	}
	if isRedshiftRec(rec) {
		return opts, fmt.Errorf("purchase[%s]: Redshift dispatch without a first dispatch time", opts.ExecutionID)
	}
	opts.RequestedAt = time.Now().UTC()
	return opts, nil
}
